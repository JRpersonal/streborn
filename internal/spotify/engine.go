// engine.go: go-librespot process lifecycle — supervision and restarts,
// crash-loop backoff, disk-space gating, the Ogg drain loop in runOnce (the
// per-page step lives in drain.go), and stderr signal parsing (Premium/seek/
// desync markers).

package spotify

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JRpersonal/streborn/internal/clocksync"
)

// Ready reports whether go-librespot can run: the binary exists. The
// device advertises over zeroconf even before the first tap, so we start
// it whenever the binary is present; playback control just returns an
// error until a credential is cached.
func (m *Manager) Ready() bool {
	if m.binPath == "" {
		return false
	}
	if fi, err := os.Stat(m.binPath); err != nil || fi.IsDir() {
		return false
	}
	return true
}

// ReloadBinary restarts the supervised go-librespot so it re-execs from its
// binary path, activating a freshly OTA-delivered engine WITHOUT a box reboot.
// The OTA write (webui.handleAgentSidecar) overwrites the binary at m.binPath and
// then calls this; the supervise loop cancels the current run and relaunches from
// the same path, so the relaunch runs the new bytes. This closes the gap where an
// engine UPDATE on an already-running agent needed a full box reboot to take
// effect, the manual restart Pierre and Daniel had to do after an update (#240).
// A first-time delivery to a box that had no engine running is already picked up
// live by waitForBinary; this method covers the already-running case.
//
// The only side effect is a brief (~3 s) audio gap while the process relaunches;
// the box's stream buffer covers most of it. It is the same trade-off as an
// account switch, which already restarts the engine live and is unnoticeable in
// practice. Returns true when a live restart was triggered, false when
// go-librespot is not currently running (the supervise loop / waitForBinary then
// starts the new binary on its own) or no binary is present.
func (m *Manager) ReloadBinary() bool {
	if !m.Ready() {
		m.logger.Info("spotify: engine reload requested but no go-librespot binary present")
		return false
	}
	m.mu.Lock()
	restart := m.runCancel
	m.mu.Unlock()
	if restart == nil {
		// Not running yet: the supervise loop (or waitForBinary on a cold start)
		// will start the just-written binary on its own, so there is nothing to
		// swap and still no reboot needed.
		m.logger.Info("spotify: engine delivered; go-librespot not running yet, supervise loop will start the new binary (no reboot)")
		return false
	}
	m.logger.Info("spotify: hot-swapping go-librespot to the OTA-delivered engine (no box reboot)")
	restart()
	return true
}

// StopEngine stops the supervised go-librespot and waits (briefly) for the
// process to exit, so the kernel releases its executable inode and the NAND
// blocks it pinned are actually freed. This is the piece the #119 "drop the
// regenerable engine to fit a tight agent update" reclaim was missing: an
// os.Remove of a RUNNING binary only drops the directory entry, the blocks stay
// pinned until the holder exits, so dropping the engine freed nothing while it
// ran (which is the normal state during an OTA) and the update still failed with
// "no space left". The OTA write calls this before unlinking the engine; the
// engine is re-delivered after the reboot by EnsureSpotifyEngine, when free space
// is high again. Safe to call when nothing is running (returns false). The
// supervise loop relaunches go-librespot after a 3 s backoff and the caller
// removes the binary right after this returns, so it does not re-pin the inode.
func (m *Manager) StopEngine() bool {
	m.mu.Lock()
	cancel := m.runCancel
	var proc *os.Process
	if m.cmd != nil {
		proc = m.cmd.Process
	}
	m.mu.Unlock()
	if cancel == nil || proc == nil {
		return false
	}
	// Confirm it is actually alive before claiming a stop (runCancel/cmd are not
	// cleared between runs, so they can be stale when nothing is running). Signal 0
	// probes liveness without delivering a signal; it is cross-platform (so the
	// host test build keeps compiling), unlike syscall.Kill.
	if proc.Signal(syscall.Signal(0)) != nil {
		return false
	}
	cancel() // exec.CommandContext SIGKILLs the process when its context is cancelled
	// Bounded wait for the process to be reaped (runOnce calls cmd.Wait once its
	// stdout closes) so a subsequent df / nandHasRoom sees the freed blocks; the
	// executable's pages are released at exit, just before the reap.
	for i := 0; i < 40; i++ { // up to ~4 s
		if proc.Signal(syscall.Signal(0)) != nil {
			break // gone (reaped / finished): blocks released
		}
		time.Sleep(100 * time.Millisecond)
	}
	m.mu.Lock()
	m.stoppedForUpdate = true
	m.mu.Unlock()
	m.logger.Info("spotify: stopped go-librespot to free NAND for an update", "pid", proc.Pid)
	return true
}

// consumeStoppedForUpdate reports whether the last engine exit was the
// deliberate StopEngine ahead of an OTA write, and clears the mark so the next
// exit is judged on its own.
func (m *Manager) consumeStoppedForUpdate() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	was := m.stoppedForUpdate
	m.stoppedForUpdate = false
	return was
}

// spotifyMinFreeBytes is the minimum free NAND below which go-librespot is not
// started. The engine binary is already on disk by this point (waitForBinary
// passed); this covers the small files the engine must still write — config.yml,
// the zeroconf credential/state, sp-resume.json, stream-headers.ogg — plus a
// margin so it can persist its login (which otherwise never sticks on a full
// /mnt/nv) without competing with the Bose firmware for the last blocks (#ST30).
const spotifyMinFreeBytes = 2 * 1024 * 1024

// errLowDisk is returned by runOnce when the NAND is too full to start the
// engine, so the supervise loop backs off and re-checks instead of treating it
// like a crash.
var errLowDisk = errors.New("insufficient NAND to start go-librespot")

// freeBytes lives in statfs_linux.go / statfs_other.go: the Linux build does a
// real statfs, other hosts fail open so the package stays testable on dev
// machines.

// diskSpaceOK reports whether configDir's filesystem has the minimum free space
// go-librespot needs. It records the low-disk state for ServeInfo and throttles
// the warning. Fails OPEN when statfs is unavailable (an unknown free figure
// never blocks), matching the OTA gate (webui.nandHasRoom).
func (m *Manager) diskSpaceOK() bool {
	free, ok := freeBytes(m.configDir)
	if !ok {
		// statfs unavailable: do not block (fail open, like webui.nandHasRoom).
		m.setLowDisk(false, 0)
		return true
	}
	if free >= spotifyMinFreeBytes {
		m.setLowDisk(false, 0)
		return true
	}
	m.setLowDisk(true, free/1024)
	return false
}

// setLowDisk updates the surfaced low-disk state and logs the warning at most
// once every 5 minutes so a tight box does not spam the log on every retry.
func (m *Manager) setLowDisk(low bool, freeKB int64) {
	m.mu.Lock()
	m.lowDisk = low
	m.lowDiskFreeKB = freeKB
	shouldLog := low && time.Since(m.lastLowDiskLogAt) > 5*time.Minute
	if shouldLog {
		m.lastLowDiskLogAt = time.Now()
	}
	m.mu.Unlock()
	if shouldLog {
		m.logger.Warn("spotify: /mnt/nv too full to start go-librespot; free space and reboot",
			"freeKB", freeKB, "needKB", spotifyMinFreeBytes/1024)
	}
}

// waitForDiskSpace blocks until configDir has the minimum free NAND space, or ctx
// ends. Returns true the moment space is available (immediately on a roomy box,
// so zero added latency normally). On a full box it idles with a throttled
// warning and a surfaced reason rather than spinning a go-librespot that cannot
// persist its credential.
func (m *Manager) waitForDiskSpace(ctx context.Context) bool {
	if m.diskSpaceOK() {
		return true
	}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			if m.diskSpaceOK() {
				m.logger.Info("spotify: NAND space recovered, starting go-librespot")
				return true
			}
		}
	}
}

// Run supervises go-librespot until ctx is cancelled, restarting it with
// a short backoff if it exits. It returns immediately (idles) when not
// Ready, so callers can start it unconditionally.
func (m *Manager) Run(ctx context.Context) {
	// The go-librespot binary can be absent at agent start: an OTA-only box that
	// never received it from a USB stick (#45/#105), to be delivered later over
	// the air (POST /api/agent/sidecar). Rather than idle forever after a single
	// start-time check, wait for the binary so a late delivery is picked up live,
	// with no extra reboot. Returns only when the binary appears or ctx ends.
	if !m.waitForBinary(ctx) {
		return
	}
	// Gate on free NAND before writing config or launching the engine: on a full
	// /mnt/nv go-librespot cannot persist its zeroconf credential (login never
	// sticks) and the config WriteFile itself can ENOSPC, leaving the manager
	// silently idle. Wait for space (a reboot's cleanup_nand, an OTA reclaim, or
	// the user clearing room) and surface the reason meanwhile (#ST30 Daniel).
	if !m.waitForDiskSpace(ctx) {
		return
	}
	if err := m.ensureConfig(ctx); err != nil {
		m.logger.Warn("spotify: cannot write config, manager idle", "err", err)
		return
	}
	// watchDeviceName stays DISABLED: it flapped the device name on transient
	// /info failures and restarted go-librespot, churning the box.
	// watchVolume is re-enabled now that its goroutine leak is fixed (per-call
	// ctx in volumeStream): it mirrors Spotify-app volume changes onto the box
	// so the Connect remote controls the speaker volume. The box -> Spotify
	// feedback direction is added separately with echo dedup to avoid a loop.
	go m.watchVolume(ctx)
	// captureLoop snapshots each account's credential as it taps the device,
	// building the per-account library that SwitchAccount swaps between for
	// multi-account preset recall.
	go m.captureLoop(ctx)
	// Debounced writer for the per-context resume memory.
	go m.resume.run(ctx)
	// One-shot: rewrite config with the box's real name + volume once the box
	// REST API answers (it is usually not up when config is first written), then
	// restart go-librespot so the Spotify app sees the right volume, not 100%.
	go m.refreshVolumeConfigOnce(ctx)
	var rapidCrashes int
	var lastClockSync time.Time
	for ctx.Err() == nil {
		// The binary can vanish while the loop runs: the OTA write drops the
		// engine to fit a tight agent update (StopEngine + unlink) and the app
		// re-delivers it after the reboot. Without this check the loop kept
		// fork/exec-ing a file that was gone, counted every attempt as a crash
		// and, three attempts later, told the log to "check the speaker's
		// network/DNS" on every box of a fleet roll (2026-09-06). A dropped
		// engine is not a crash: wait for the sidecar delivery instead.
		if !m.Ready() {
			m.logger.Info("spotify: engine binary is gone (dropped for an update), waiting for it to be delivered again")
			if !m.waitForBinary(ctx) {
				return
			}
			rapidCrashes = 0
		}
		started := time.Now()
		err := m.runOnce(ctx)
		if err != nil && ctx.Err() == nil && !errors.Is(err, errLowDisk) {
			if m.consumeStoppedForUpdate() {
				// The exit was ours (StopEngine ahead of an OTA write), so it
				// is neither a crash nor worth a restart warning.
				m.logger.Info("spotify: go-librespot stopped for an update, not counting it as a crash")
				continue
			}
			m.logger.Warn("go-librespot exited, restarting", "err", err)
		}
		// Crash-loop detection: a run that ended almost immediately is a crash,
		// not real playback (time.Since uses the monotonic clock, so it is
		// correct even when the wall clock is wrong). A cold-booted box with no
		// RTC often has a 2015 clock, which makes go-librespot's own TLS to
		// Spotify fail on every launch; the one-shot clock sync in run.sh may
		// have missed the network. After a couple of rapid crashes, re-attempt
		// an HTTP-Date clock correction (rate-limited) so the next launch can
		// authenticate (#296 root cause).
		if err != nil && ctx.Err() == nil && time.Since(started) < 20*time.Second {
			rapidCrashes++
			if rapidCrashes >= 2 && clocksync.Implausible(time.Now()) && time.Since(lastClockSync) > 5*time.Minute {
				lastClockSync = time.Now()
				if synced, serr := clocksync.SyncIfImplausible(ctx, m.client, m.logger); serr != nil {
					m.logger.Warn("spotify: clock resync after crash-loop failed", "err", serr)
				} else if synced {
					rapidCrashes = 0
				}
			}
		} else {
			rapidCrashes = 0
		}
		// Back off when the engine keeps dying instantly. A box with a broken
		// network (no DNS: go-librespot cannot resolve apresolve.spotify.com,
		// #487) otherwise relaunches every 3 s forever, and each crash writes
		// several log lines: the 32 KB NAND forensic buffer was burned down to
		// ~84 s of history, destroying exactly the evidence such a box needs.
		// Healthy playback resets rapidCrashes, so a normal restart still
		// takes the fast 3 s path.
		wait := 3 * time.Second
		if rapidCrashes >= 3 {
			wait = time.Duration(rapidCrashes) * 5 * time.Second
			if wait > spotifyMaxRestartBackoff {
				wait = spotifyMaxRestartBackoff
			}
			if rapidCrashes == 3 || rapidCrashes%10 == 0 {
				m.logger.Warn("spotify: engine keeps failing right after launch, slowing the restarts down (check the speaker's network/DNS)",
					"rapidCrashes", rapidCrashes, "retryInS", int(wait.Seconds()))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// spotifyMaxRestartBackoff caps the crash-loop backoff. One minute keeps a
// recovered network unnoticeably fast to pick up while cutting the log churn
// of a permanently offline box by a factor of twenty.
const spotifyMaxRestartBackoff = 60 * time.Second

// waitForBinary blocks until the go-librespot binary is present (m.Ready) or ctx
// is cancelled, returning true the moment it appears. It returns immediately when
// the binary is already there (the normal, stick-synced case), so it adds zero
// latency to a box that has the engine. For an OTA-only box the binary lands
// later via the sidecar push (webui.handleAgentSidecar); polling here makes the
// manager start go-librespot as soon as it appears instead of needing another
// reboot, closing the gap where a box upgraded to a sidecar-capable agent still
// had no engine (the manager used to check exactly once and idle forever).
func (m *Manager) waitForBinary(ctx context.Context) bool {
	if m.Ready() {
		return true
	}
	m.logger.Info("spotify manager: no go-librespot binary yet, waiting for an OTA sidecar delivery")
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			if m.Ready() {
				m.logger.Info("spotify manager: go-librespot binary now present, starting")
				return true
			}
		}
	}
}

func (m *Manager) runOnce(ctx context.Context) error {
	// Re-check free NAND per launch: if the box filled up after the manager
	// started, do not relaunch a go-librespot that cannot persist its credential;
	// back off and let the supervise loop retry when space returns.
	if !m.diskSpaceOK() {
		return errLowDisk
	}
	// go-librespot uses pflag: the long flag needs two dashes (-config_dir
	// is misparsed as a shorthand cluster). HOME is forced into the
	// writable config dir because the box rootfs is read-only and
	// go-librespot otherwise tries to create ~/.config.
	// Per-process context so watchDeviceName can restart just this run (to
	// re-apply a changed device_name) without tearing down the manager.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	// Fresh process = re-detect the account product (an account switch relaunches
	// go-librespot), so the #45 Premium warning reflects the current login.
	m.mu.Lock()
	m.productType, m.sawFreeAccountLog, m.productTriedAt = "", false, time.Time{}
	m.mu.Unlock()
	cmd := exec.CommandContext(runCtx, m.binPath, "--config_dir", m.configDir)
	cmd.Env = append(os.Environ(), "HOME="+m.configDir)
	// Point the engine's Spotify Connect advert at a name this speaker actually
	// answers for. Without it the advert names the box's Linux hostname, which
	// on this hardware is the Bose chassis codename (mojo, rhino, taigan): a
	// name no responder on the network answers, and one that three SoundTouch
	// 10s on the same network all claim at once.
	//
	// This is only ever set once the agent's own responder is up and answering
	// for the label, so the worst case is exactly the behaviour without it. The
	// value is the bare label on purpose: the engine's mDNS library appends the
	// domain itself, and handing it "name.local" yields "name.local.local".
	if host := m.zeroconfHost(); host != "" {
		cmd.Env = append(cmd.Env, "PULSAR_ZEROCONF_HOST="+host)
		m.logger.Info("spotify: the engine will advertise a name this speaker answers for",
			"host", host+".local")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = newLogWriter(m.logger, m.noteLibrespotLine)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Phase marker at INFO so a diagnostic distinguishes "go-librespot launched
	// and is running" from "idle: no binary" (#45/#105: on a box the binary was
	// never delivered to, the only line is the idle one above; this confirms a
	// live sidecar). Pairs with the syscheck go_librespot=present/MISSING report.
	m.logger.Info("go-librespot started", "pid", cmd.Process.Pid, "bin", m.binPath)
	m.mu.Lock()
	m.cmd = cmd
	m.runCancel = runCancel
	m.mu.Unlock()

	// flushBytes is how much is batched before each write+flush to the box
	// (see flushThreshold). Tunable at runtime via a NAND file so the leak can
	// be swept without rebuilding: write the KB value there and restart
	// go-librespot. Falls back to the compiled default.
	flushBytes := flushThreshold
	if b, err := os.ReadFile("/mnt/nv/streborn/spotify-flush-kb"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n >= 1 && n <= 1024 {
			flushBytes = n * 1024
			m.logger.Info("spotify: flush batch overridden", "kb", n)
		}
	}

	// leadCapSec bounds how far AHEAD of realtime the box is fed. go-librespot's
	// passthrough emits a track as fast as Spotify's CDN serves it, and the box
	// happily buffers MINUTES of audio, so a track skip only became audible once
	// all that old audio had played out (live Portable 2026-08-01: skip executed
	// at 00:03:49, heard minutes later). Radio streams prove the box plays
	// perfectly at realtime, so after the initial lead the forward loop paces
	// pages to realtime + this lead. Tunable via NAND for field sweeps; 0
	// disables the pacing entirely.
	leadCapSec := float64(oggLeadCapSec)
	if b, err := os.ReadFile("/mnt/nv/streborn/spotify-lead-sec"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n >= 0 && n <= 300 {
			leadCapSec = float64(n)
			m.logger.Info("spotify: stream lead cap overridden", "sec", n)
		}
	}

	// Drain go-librespot's Ogg output page by page and forward whole pages to
	// the box. While no box is attached, capture the current track's header
	// pages and pause go-librespot so it does not race to the end of the
	// playlist unheard; ServeOgg resumes it and replays the headers when a
	// box joins, so a mid-track joiner can still decode. The per-page work
	// lives in oggDrain.page (drain.go); the chain cutter (oggchain.go) splits
	// a long track into logical streams so the box frees its stream buffer.
	r := newOggPageReader(stdout, m.logger)
	d := m.newOggDrain(flushBytes, leadCapSec, m.newChain())
	defer d.finish()
	for {
		page, err := r.ReadPage()
		if err != nil {
			break
		}
		d.page(ctx, page)
		if ctx.Err() != nil {
			break
		}
	}
	return cmd.Wait()
}

// noteLibrespotLine inspects a go-librespot stderr line for the non-Premium
// account signal. librespot refuses free accounts and logs that it does not
// support them; seeing that latches sawFreeAccountLog so PremiumRequired can warn
// that preset recall needs Premium (#45).
//
// It also remembers the most recent play-request failure line: Spotify's
// audio-key denial ("failed retrieving aes key with code 1") surfaces ONLY
// here while the API answers a bare 500 — a field report (#311) needed a full
// diagnostic round just to see it. playDenialHint turns that memory into a
// human-readable suffix on the play error.
const (
	// A run of refusals is only a run while they keep arriving. The engine
	// walks a playlist in well under a second per refused track, so anything
	// with a gap this large is a new episode.
	keyRefusalRunGap = 30 * time.Second
	// Enough in a row that a single unavailable track cannot trip it.
	keyRefusalRunTrips = 5
)

// parseLoadedTrackDurMs pulls the millisecond duration out of a lowercased
// go-librespot "loaded track" line (`... duration: 236400ms, ...`). Zero when
// the line carries none.
func parseLoadedTrackDurMs(lc string) int64 {
	i := strings.Index(lc, "duration: ")
	if i < 0 {
		return 0
	}
	var ms int64
	for _, r := range lc[i+len("duration: "):] {
		if r < '0' || r > '9' {
			break
		}
		ms = ms*10 + int64(r-'0')
	}
	return ms
}

func (m *Manager) noteLibrespotLine(line string) {
	lc := strings.ToLower(line)
	// "loaded track" carries the duration the app-skip detector needs (see
	// durQueueMs). The prefetch line names a duration too, but describes a
	// track that may never play, so it must not enter the queue.
	if strings.Contains(lc, `msg="loaded track`) {
		if ms := parseLoadedTrackDurMs(lc); ms > 0 {
			m.mu.Lock()
			m.durQueueMs = append(m.durQueueMs, ms)
			// Bounded: a load that never produces a boundary (aborted play)
			// must not let the queue drift away from the stream for good.
			if len(m.durQueueMs) > 4 {
				m.durQueueMs = m.durQueueMs[len(m.durQueueMs)-4:]
			}
			m.mu.Unlock()
		}
	}
	if strings.Contains(lc, "free") && (strings.Contains(lc, "not support") || strings.Contains(lc, "premium")) {
		m.mu.Lock()
		already := m.sawFreeAccountLog
		m.sawFreeAccountLog = true
		m.mu.Unlock()
		if !already {
			m.logger.Warn("spotify: go-librespot reports a non-Premium account; preset recall needs Premium (#45)", "line", line)
		}
	}
	if strings.Contains(lc, "failed handling request play") {
		m.mu.Lock()
		m.lastPlayFailLine = lc
		m.lastPlayFailAt = time.Now()
		m.mu.Unlock()
	}
	// A resume-point recall passes skip_to_uri to seek to the last-played track; on
	// a volatile context that track is often gone and go-librespot logs "failed
	// seeking to track in context ... could not find track" and loads nothing.
	// Remember it so Play can fall back to replaying the context from the top.
	if strings.Contains(lc, "could not find track") || strings.Contains(lc, "failed seeking to track") {
		m.mu.Lock()
		m.lastSeekFailAt = time.Now()
		m.mu.Unlock()
	}
	// The engine could not work out what to play after the current context ran
	// out. It stops afterwards, and that stop must not be read as the listener
	// having stopped it.
	if strings.Contains(lc, "failed resolving station") || strings.Contains(lc, "context resolve") {
		m.mu.Lock()
		m.lastCtxResolveFailAt = time.Now()
		m.mu.Unlock()
		m.logger.Warn("spotify: the engine could not resolve what to play after this playlist, so playback is about to stop", "line", line)
	}
	// The engine could not LOAD the next track (a transient Spotify-side
	// failure mid-playlist, distinct from the context running out above). It
	// stops afterwards, and that stop must not arm the deliberate-stop latch:
	// live 2026-08-21 a group of six speakers fell silent after six songs
	// because "failed advancing to next track" was read as the listener
	// stopping playback in the Spotify app.
	if strings.Contains(lc, "failed advancing to next track") || strings.Contains(lc, "failed loading current track") {
		m.mu.Lock()
		m.lastTrackLoadFailAt = time.Now()
		m.mu.Unlock()
		m.logger.Warn("spotify: the engine failed loading the next track, so playback is about to stop mid-playlist", "line", line)
	}
	// Spotify refused the audio key for a track. One is unremarkable: a single
	// track can be unavailable in a region. A run of them is the account being
	// refused wholesale, and that is what a listener experiences as a playlist
	// racing past in silence.
	if strings.Contains(lc, "audio key") && (strings.Contains(lc, "refused") || strings.Contains(lc, "failed retrieving")) {
		m.mu.Lock()
		if time.Since(m.lastKeyRefusalAt) > keyRefusalRunGap {
			m.keyRefusalRun = 0
		}
		m.keyRefusalRun++
		m.lastKeyRefusalAt = time.Now()
		run := m.keyRefusalRun
		m.mu.Unlock()
		if run == keyRefusalRunTrips {
			m.logger.Warn("spotify: Spotify is refusing the audio key for track after track, so nothing plays through this engine; the speaker's own Spotify entry is unaffected",
				"tracksInARow", run)
		}
	}
	// The engine gave up on a whole run. This is the moment the user is left
	// with silence, so it is the one worth showing.
	if strings.Contains(lc, "consecutive unplayable tracks") {
		m.mu.Lock()
		m.keyRefusalGaveUpAt = time.Now()
		m.mu.Unlock()
	}

	// Connect-desync markers (upstream go-librespot #300): a "put connect
	// state" timeout desyncs the device from the Spotify cluster - the app's
	// buttons go dead/laggy, the device flaps in the picker, the engine can
	// enter a rapid-skip loop, and upstream never recovers without a restart.
	// Live-observed on a Portable 2026-07-10 (both engine builds).
	if strings.Contains(lc, "failed put state") ||
		strings.Contains(lc, "put state request failed") ||
		strings.Contains(lc, "failed receiving dealer message") {
		m.noteDesyncSignature()
	}
}

// noteDesyncSignature counts Connect-desync markers and, at three within two
// minutes, restarts the engine once (rate-limited to one heal per ten
// minutes): the relaunch re-registers the device with the cluster and heals
// in seconds what upstream #300 says needs a manual reboot. Sporadic single
// markers (the dealer's routine reconnects) never reach the threshold. The
// box's attached Ogg stream survives an engine restart by design (ServeOgg
// buffering), so a heal during playback costs at most a short dropout.
func (m *Manager) noteDesyncSignature() {
	now := time.Now()
	m.mu.Lock()
	keep := m.desyncAt[:0]
	for _, t := range m.desyncAt {
		if now.Sub(t) < 2*time.Minute {
			keep = append(keep, t)
		}
	}
	m.desyncAt = append(keep, now)
	healedRecently := !m.lastDesyncHeal.IsZero() && now.Sub(m.lastDesyncHeal) < 10*time.Minute
	var cancel context.CancelFunc
	if len(m.desyncAt) >= 3 && !healedRecently {
		m.lastDesyncHeal = now
		m.desyncAt = m.desyncAt[:0]
		cancel = m.runCancel
	}
	m.mu.Unlock()
	if cancel != nil {
		m.logger.Warn("spotify: Connect desync detected (put-state/dealer failures piling up, upstream go-librespot #300); restarting the engine to re-register the device")
		cancel()
	}
}

// playDenialHint translates a just-logged play failure into a hint the app can
// show verbatim. Only the audio-key denial is translated (the signature of a
// non-Premium account, occasionally an unlicensed item); anything else returns
// "" so unrelated failures keep their original error. Deliberately NOT latched
// into PremiumRequired: a single denial can be item-specific, and a Premium
// user must never be blocked on a false positive (#45 contract).
func (m *Manager) playDenialHint() string {
	// The stderr line usually lands just before the API's 500, but give a
	// slow pipe one beat before concluding there is no detail.
	for attempt := 0; attempt < 2; attempt++ {
		m.mu.Lock()
		line, at := m.lastPlayFailLine, m.lastPlayFailAt
		m.mu.Unlock()
		if !at.IsZero() && time.Since(at) < 15*time.Second &&
			(strings.Contains(line, "aes key") || strings.Contains(line, "audio key")) {
			// A key request that failed because the connection to Spotify was
			// gone is NOT a denial. The engine authenticates first and its
			// session to Spotify's access point comes up a moment later, so the
			// first press right after the engine starts can ask for a key over a
			// connection that is already closed, while the same press seconds
			// later works. Blaming a missing Premium subscription there sends the
			// user after a problem they do not have (#512: "the first preset
			// press plays no Spotify", engine authenticated one second earlier).
			if strings.Contains(line, "accesspoint closed") ||
				strings.Contains(line, "connection reset") ||
				strings.Contains(line, "use of closed network connection") {
				return "Spotify's connection was not ready yet, so the audio could not be fetched. " +
					"This usually only hits the first press after the speaker's Spotify engine has started; " +
					"press the button again in a few seconds."
			}
			return "Spotify refused to deliver the audio for this item (audio key denied). " +
				"This usually means the Spotify account on the speaker has no Premium subscription, " +
				"which STR's Spotify engine requires; occasionally the item itself is not licensed for streaming."
		}
		if attempt == 0 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	return ""
}

// logWriter forwards go-librespot stderr lines to the agent logger.
type logWriter struct {
	logger *slog.Logger
	onLine func(string) // optional per-line hook (e.g. free-account detection)
}

func newLogWriter(l *slog.Logger, onLine func(string)) *logWriter {
	return &logWriter{logger: l, onLine: onLine}
}

func (w *logWriter) Write(p []byte) (int, error) {
	line := trimEOL(string(p))
	w.logger.Info("go-librespot", "line", line)
	if w.onLine != nil {
		w.onLine(line)
	}
	return len(p), nil
}

func trimEOL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
