// engine.go: go-librespot process lifecycle — supervision and restarts,
// crash-loop backoff, disk-space gating, the Ogg drain in runOnce, and
// stderr signal parsing (Premium/seek/desync markers).

package spotify

import (
	"context"
	"encoding/binary"
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
	m.logger.Info("spotify: stopped go-librespot to free NAND for an update", "pid", proc.Pid)
	return true
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
		started := time.Now()
		err := m.runOnce(ctx)
		if err != nil && ctx.Err() == nil && !errors.Is(err, errLowDisk) {
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
	// box joins, so a mid-track joiner can still decode.
	r := newOggPageReader(stdout, m.logger)
	var hdr []byte
	capturing := false
	paused := false
	// Bitrate measurement: body bytes and the highest granule (sample count at
	// vorbisRate) seen since the current track's BOS. kbps = bytes*8 over the
	// elapsed seconds. This is the real stream rate, not the configured nominal.
	var trackBody, maxGran int64
	// pending batches pages so the box receives large chunks instead of one
	// tiny chunk per page (see flushThreshold: small chunks leak box memory).
	var pending []byte
	// trackNum + forwarded count instrument the playback so the occasional
	// "track restarts at its start" can be diagnosed: track boundaries (new
	// BOS) and box (re)attaches are logged with byte/granule context.
	trackNum := 0
	var forwarded int64
	// Realtime pacing state (see leadCapSec above). Granules reset per track,
	// so granOffset accumulates finished tracks into one continuous timeline;
	// the anchor is set once per sink attachment at the first audio page, so
	// the lead cannot creep up by one cap per track boundary.
	var granOffset, leadBaseGran int64
	var leadBaseAt time.Time
	leadAnchored := false
	// pendingSince stamps the oldest byte in the batch, for the flush-age
	// bound below.
	pendingSince := time.Now()
	for {
		page, err := r.ReadPage()
		if err != nil {
			break
		}

		// Maintain the current track's header pages: a BOS page starts a
		// track (Vorbis identification header), the following granule<=0
		// pages carry comment/setup, the first audio page (granule>0) ends
		// the header sequence.
		htype := page[5]
		gran := int64(binary.LittleEndian.Uint64(page[6:14]))
		numSegs := int(page[26])
		bodyLen := int64(len(page) - 27 - numSegs)
		switch {
		case htype&0x02 != 0: // BOS
			// New logical stream = track boundary. Log it with the previous
			// track's size so a premature/duplicate BOS (the suspected cause of
			// a track restarting at its start) is visible in the log.
			m.logger.Info("spotify: track boundary (BOS)",
				"track", trackNum+1, "prevTrackKB", trackBody/1024,
				"prevMaxGran", maxGran, "forwardedKB", forwarded/1024)
			trackNum++
			granOffset += maxGran // finished track extends the continuous timeline
			hdr = append([]byte(nil), page...)
			capturing = true
			trackBody, maxGran = 0, 0
		case capturing && gran > 0: // first audio page
			m.mu.Lock()
			m.headerPages = hdr
			persist := !m.hdrPersisted && m.hdrPath != ""
			if persist {
				m.hdrPersisted = true
			}
			m.mu.Unlock()
			capturing = false
			if persist {
				// Persist one valid header set to NAND for the next cold boot.
				// Once only (guarded above), so no per-track flash wear.
				if err := os.WriteFile(m.hdrPath, hdr, 0o644); err != nil {
					m.logger.Debug("spotify: persist stream headers failed", "err", err)
				}
			}
		case capturing:
			hdr = append(hdr, page...)
		}
		trackBody += bodyLen
		if gran > maxGran {
			maxGran = gran
		}
		if maxGran > vorbisRate { // at least one second streamed
			kbps := int(trackBody * 8 * vorbisRate / (maxGran * 1000))
			m.mu.Lock()
			m.actualKbps = kbps
			m.mu.Unlock()
		}

		m.mu.Lock()
		sink := m.sink
		haveHdr := len(m.headerPages) > 0
		m.mu.Unlock()

		if sink != nil {
			paused = false
			// Track-boundary flush: a new BOS is a new logical Vorbis stream
			// (the box must reload codebooks). If the BOS is buried mid-batch
			// behind the previous track's tail, the box re-inits on a partial
			// chunk and the new track audibly restarts (live-observed, ~1 in 3
			// tracks). Flushing the tail first makes the BOS begin on a clean
			// chunk boundary so the decoder re-inits cleanly.
			//
			// Skip cut: when this boundary was CAUSED by a user skip, the old
			// track's unsent tail is noise the user asked to get away from, so
			// it is dropped instead of flushed and the new track starts that
			// much sooner. A natural track end never has an armed skip window,
			// so song endings are never clipped. The cut disarms here and the
			// pacing re-anchors, so the new track gets its instant prefill.
			if htype&0x02 != 0 {
				if m.skipCutArmed() {
					m.logger.Info("spotify: skip cut, boundary reached; dropped the old track's unsent tail", "droppedKB", len(pending)/1024)
					pending = pending[:0]
					m.clearSkipCut()
					leadAnchored = false
				} else if len(pending) > 0 {
					m.forward(sink, pending)
					pending = pending[:0]
				}
			} else if m.skipCutArmed() && gran > 0 {
				// Stale audio between the user's skip and the new track's
				// boundary. The first version PACED these pages to realtime, so
				// the boundary reached the box up to a full lead cap late and
				// the skip felt >20 s (Jens, live 2026-08-01 00:30). They carry
				// nothing the user wants to hear: drop them outright and race
				// to the boundary.
				continue
			}
			// Realtime pacing: anchor once per attachment at the first audio
			// page, then hold each page until its position on the continuous
			// timeline is within leadCapSec of wall clock. A detach mid-wait
			// (box paused or dropped the stream) bails out immediately.
			if gran > 0 && leadCapSec > 0 {
				timeline := granOffset + gran
				if !leadAnchored {
					leadBaseGran, leadBaseAt, leadAnchored = timeline, time.Now(), true
				}
				for {
					ahead := float64(timeline-leadBaseGran)/float64(vorbisRate) - time.Since(leadBaseAt).Seconds()
					if ahead <= leadCapSec || ctx.Err() != nil {
						break
					}
					time.Sleep(min(time.Duration((ahead-leadCapSec)*float64(time.Second)), 250*time.Millisecond))
					m.mu.Lock()
					stillAttached := m.sink == sink
					m.mu.Unlock()
					if !stillAttached {
						break
					}
				}
			}
			// Batch pages into large writes (see flushThreshold) so the box
			// gets large chunks, not a tiny chunk per page. With the realtime
			// pacing above, the size threshold alone is a trap: at ~187 kbps it
			// takes ~11 s to fill 256 KB, so the box received its audio as one
			// lump per ~11 s, ran dry in between, and a skip landing in the gap
			// starved it into detaching (live 2026-08-01 01:13, followed by a
			// recovery recall that read as a double skip). A flush-age bound
			// keeps the flow continuous; the chunks stay far above the
			// per-page writes the size threshold exists to prevent.
			if len(pending) == 0 {
				pendingSince = time.Now()
			}
			pending = append(pending, page...)
			forwarded += int64(len(page))
			if len(pending) >= flushBytes || time.Since(pendingSince) > maxFlushAge {
				m.forward(sink, pending)
				pending = pending[:0]
			}
			continue
		}
		leadAnchored = false // no consumer: next attachment re-anchors fresh
		// No consumer: drop any half-filled batch so a freshly attaching box
		// starts clean, then once a track's headers are captured pause
		// go-librespot so it stops producing (no racing) until a box attaches
		// and ServeOgg resumes it.
		pending = pending[:0]
		// During a recall keep the engine playing even with no sink: a hardware
		// preset press makes the box flap its source (1036 INVALID_SOURCE) and
		// drop the sink repeatedly before it settles, and pausing here stranded
		// the engine so the settled box never got audio. engineHot() covers the
		// recall + verify window; outside it, pause as before so an idle box does
		// not keep go-librespot decoding to nothing.
		if !paused && haveHdr && !m.engineHot() {
			pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			_ = m.Pause(pctx)
			cancel()
			paused = true
		}
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
func (m *Manager) noteLibrespotLine(line string) {
	lc := strings.ToLower(line)
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
