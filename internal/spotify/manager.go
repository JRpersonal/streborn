// Package spotify runs go-librespot as a persistent Spotify Connect
// receiver on the speaker and exposes its audio so the box can play it
// over UPnP, the audio plane of the Spotify-preset feature (#78, P1).
//
// Why go-librespot (devgianlu) and not librespot-org:
//   - Hardware preset buttons 1..6 must recall a saved Spotify playlist
//     autonomously, with no phone app present. That needs the box to be
//     able to say "play URI X" by itself. librespot-org has no local
//     control API, so its only autonomous path is the Spotify Web API
//     with a refreshable OAuth token stored on the box (a security
//     surface we do not want). go-librespot ships a local HTTP API:
//     POST /player/play {uri} plays a URI using its own cached
//     credential, no token plane. See Play below.
//   - GPL-3.0 is fine here: go-librespot runs as a separate sidecar
//     process (exec + localhost HTTP). STR merely aggregates it; the
//     agent stays MIT. The binary is built, attested, audited and
//     credited separately.
//
// Audio shape:
//   - go-librespot runs with the STR Ogg-passthrough patch
//     (.github/patches/go-librespot-passthrough.patch) and
//     audio_output_pipe_passthrough. We point audio_output_pipe at
//     /dev/stdout so it writes the raw Ogg/Vorbis to its stdout (it logs
//     to stderr); the manager drains that and ServeOgg streams it to the
//     box, which decodes the Ogg natively over UPnP. This roughly halves
//     CPU on the weak A8 vs streaming decoded PCM (validated live).
//
// Credentials: zeroconf with persist_credentials. The user taps the
// device once in the Spotify app (the natural "connect to a speaker"
// flow); go-librespot persists the reusable credential under configDir
// and auto-logs-in on every later start, so API-driven recall works
// with no controller attached.
//
// Single consumer by design: one box plays one Spotify stream at a time.
// When no HTTP client is attached the audio is discarded so go-librespot
// never blocks on a full pipe.
package spotify

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/gorilla/websocket"
)

// vorbisRate is Spotify's Ogg/Vorbis sample rate; the Ogg granule position
// counts samples at this rate, which is how the drain turns bytes into a
// measured bitrate.
const vorbisRate = 44100

// flushThreshold batches Ogg pages before each write+flush to the box. The
// Bose firmware leaks memory in proportion to how many tiny HTTP chunks it
// receives, so bigger batches mean less leak. Live sweep (2026-06-05, per-value
// leak rate over a fresh boot):
//
//	  4 KB (per-page)  3.4 MB/min   stable
//	 16 KB            1.4 MB/min   stable
//	128 KB            0.6 MB/min   borderline
//	256 KB            0.38 MB/min  occasional underrun restart (~1 in 3 tracks)
//	512 KB            0.48 MB/min  frequent underrun restarts (no leak gain)
//
// The leak floors at ~0.4 MB/min (the irreducible live-streaming component);
// past ~256 KB there is no gain, only more underruns. Underruns happen because
// the single-goroutine drain blocks while writing a big batch and stops reading
// go-librespot, leaving a gap the box re-fetches over. 256 KB is the chosen
// operating point: lowest leak at the floor, with rare restarts Jens accepted
// in exchange. A lead/jitter buffer (decouple read from write) would remove the
// underruns and let it run here cleanly; tracked as a follow-up. Runtime
// override: /mnt/nv/streborn/spotify-flush-kb. Header replay is exempt.
const flushThreshold = 256 * 1024

// Manager supervises one go-librespot process and brokers its PCM output
// (as a live WAV stream) to at most one HTTP consumer (the speaker),
// plus drives playback through go-librespot's local HTTP API.
type Manager struct {
	binPath    string
	configDir  string
	fallback   string // device name used until the box's friendly name is known
	apiAddr    string // host:port of go-librespot's HTTP API
	logger     *slog.Logger
	bitr       int            // 96/160/320
	client     *http.Client   // short ops: pause/resume/volume/info
	playClient *http.Client   // /player/play: a cold playlist load can take >5s
	box        *boxapi.Client // box REST: friendly name (device_name) + volume bridge
	credStore  string         // per-account credential copies for multi-account swap

	mu           sync.Mutex
	name         string    // device name currently written to config.yml
	configVol    int       // initial_volume currently written to config.yml
	sink         io.Writer // current HTTP consumer, nil when none
	lastAttachAt time.Time // when the box last attached to the Ogg stream (re-attach storm detection)
	cmd          *exec.Cmd
	// runCancel restarts the current go-librespot process when called: it
	// cancels the per-process context so the supervise loop relaunches it.
	// Used to re-apply a changed device_name (go-librespot reads its name only
	// at start). nil while no process runs.
	runCancel context.CancelFunc
	// enginePaused holds the supervise loop idle instead of relaunching
	// go-librespot. StopEngine sets it so the binary can be removed to free its
	// NAND blocks for a tight OTA write (an unlinked-but-running binary keeps its
	// blocks until the process exits); RestartEngine clears it once the new engine
	// is delivered. Guarded by mu. nil/false in the common case.
	enginePaused bool
	// actualKbps is the bitrate measured from the live Ogg stream (body bytes
	// per granule second). 0 until enough of a track has streamed.
	actualKbps int
	// curName/curArtist/curCover hold the currently-playing track's metadata,
	// captured from go-librespot's /events so the desktop app (and later the
	// box display) can show the live artist/title/cover during Spotify playback.
	curName, curArtist, curCover string
	// onTrack fires when the playing Spotify track changes, so the recently-
	// played ring records each song under the active Spotify card (#135). nil
	// until wired; lastNotifiedTrack dedups repeated metadata/status updates.
	onTrack           func(track, artist string)
	lastNotifiedTrack string
	// Spotify account product type, used to warn that preset recall needs Premium
	// (#45). productType is cached from go-librespot's /web-api/v1/me ("premium"/
	// "free"/"open"); sawFreeAccountLog is set when go-librespot logs that it does
	// not support a free account. Either non-premium signal makes PremiumRequired
	// true. Reset on each go-librespot (re)launch so an account switch re-detects.
	productType       string
	productCheckedAt  time.Time
	productTriedAt    time.Time
	sawFreeAccountLog bool
	// onActivate is invoked when go-librespot starts playing while no box is
	// attached to the Ogg stream, i.e. the user pressed play in the Spotify app
	// (selecting this device) but the box is still on another source. The
	// callback points the box's UPnP renderer at the Spotify stream so it
	// actually plays (#14). nil until wired. lastActivate debounces it.
	onActivate   func(context.Context)
	lastActivate time.Time
	// activateBackoff grows each time the box re-attaches to the Ogg stream in a
	// rapid storm (the INVALID_SOURCE re-point loop: the box keeps dropping and
	// re-fetching, heard as the song restarting every minute). While it is set,
	// suppressActivateUntil holds maybeActivate/repointBox off so STR stops
	// re-pointing the box into the same failing state. A sustained, healthy
	// attach resets it to 0 (#136, #113).
	activateBackoff time.Duration
	// suppressActivateUntil silences maybeActivate/repointBox for a short window
	// after the user deliberately switched the box to a non-Spotify source. Without
	// it, go-librespot keeps the playlist advancing in the background and the #14
	// auto-attach yanked the box back to Spotify a second after a radio recall
	// (reported: hardware preset Spotify->radio played radio ~1s then jumped back).
	suppressActivateUntil time.Time
	// recallUntil marks a recall in progress: until this time, ServeOgg must NOT
	// resume go-librespot on a box attach. Otherwise the box's own preset
	// self-activation resumes the OLD track at its paused (mid) position before
	// our Play loads the new shuffled track, so the first song started mid-song.
	// During a recall, Play drives playback (track from its start) instead.
	recallUntil time.Time
	// lastContext is the Spotify context (playlist/album) URI go-librespot last
	// announced via will_play. When it changes (the app switched to another
	// playlist) the box is re-pointed at the stream so it drops its buffer and
	// plays the new playlist promptly instead of finishing the old buffer.
	lastContext string
	// headerPages holds the current track's Ogg header pages (the BOS page
	// with the Vorbis identification header plus the comment/setup pages).
	// The drain captures them as they stream past; ServeOgg replays them to
	// a freshly-attached box before the live data, so a box that joins
	// mid-track still gets the headers it needs to start decoding (the next
	// real BOS is a whole track away). This is the Icecast late-joiner
	// pattern.
	headerPages []byte
	// hdrPath persists one valid header set to NAND; on a cold boot (empty
	// headerPages) it is loaded so ServeOgg can hand a freshly-attaching box
	// valid Ogg immediately and let it buffer, instead of the box getting zero
	// bytes and flashing "service unavailable" before go-librespot's first track
	// loads (the real track BOS resyncs right after). hdrPersisted guards the
	// write to exactly once, so there is no per-track flash wear.
	hdrPath      string
	hdrPersisted bool
	// resume remembers, per context, the last track played from it so a default
	// (non-shuffle) recall can continue where the user left off instead of
	// restarting the context. curTrackURI is the current track's spotify: URI,
	// captured from /status and metadata events to feed the resume store.
	resume      *resumeStore
	curTrackURI string
}

// New returns a Manager. binPath is the go-librespot binary, configDir
// the config + credential directory (config.yml is written there on
// Run; the persisted zeroconf credential lives there after the first
// Spotify-app tap). box is the Bose REST client: the manager reads the
// speaker's friendly name from it (so the Spotify Connect device and its
// local mDNS advert carry the speaker's own name, not a hardcoded one) and
// bridges Spotify volume changes onto the box. fallbackName is used only
// until the box answers /info.
func New(binPath, configDir, fallbackName string, box *boxapi.Client, logger *slog.Logger) *Manager {
	if fallbackName == "" {
		fallbackName = "ST Reborn"
	}
	m := &Manager{
		binPath:    binPath,
		configDir:  configDir,
		fallback:   fallbackName,
		name:       fallbackName,
		box:        box,
		credStore:  filepath.Join(filepath.Dir(configDir), "sp-accounts"),
		apiAddr:    "127.0.0.1:3678",
		logger:     logger,
		bitr:       160,
		client:     &http.Client{Timeout: 5 * time.Second},
		playClient: &http.Client{Timeout: 25 * time.Second},
		hdrPath:    filepath.Join(configDir, "stream-headers.ogg"),
	}
	// Per-context resume memory lives next to the per-account credential store
	// on NAND (a sibling of configDir), so it survives reboots and OTA agent
	// swaps (which replace only the binary).
	m.resume = newResumeStore(filepath.Join(filepath.Dir(configDir), "sp-resume.json"), logger)
	// Warm the Ogg header cache from the last session so the very first box
	// attach after a cold boot gets valid Ogg (buffers) instead of nothing
	// (the "service unavailable" flash). Best-effort; absent on a fresh install.
	if b, err := os.ReadFile(m.hdrPath); err == nil && len(b) > 0 {
		m.headerPages = b
		m.hdrPersisted = true
	}
	return m
}

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

// configYAML is the go-librespot config the manager writes. /dev/stdout as
// the pipe + passthrough makes go-librespot emit the raw Ogg/Vorbis on its
// stdout (no decode); the box decodes it natively, which on the weak A8
// roughly halves CPU vs streaming decoded PCM. The API server gives us
// local playback control; zeroconf + persist gives a tap-once,
// auto-login-forever credential. Passthrough needs the STR patch
// (.github/patches/go-librespot-passthrough.patch) baked into the binary.
func (m *Manager) configYAML(name string, initialVol int) string {
	host, port := splitHostPort(m.apiAddr)
	var b strings.Builder
	fmt.Fprintf(&b, "device_name: %q\n", name)
	b.WriteString("device_type: speaker\n")
	fmt.Fprintf(&b, "bitrate: %d\n", m.bitr)
	b.WriteString("audio_backend: pipe\n")
	b.WriteString("audio_output_pipe: /dev/stdout\n")
	b.WriteString("audio_output_pipe_format: s16le\n")
	b.WriteString("audio_output_pipe_passthrough: true\n")
	// Volume bridge: the box owns the actual volume (passthrough Ogg can't be
	// scaled by go-librespot), so external_volume makes go-librespot forward
	// Connect volume changes as /events instead of applying them; the manager
	// mirrors those onto the box and back (with echo dedup, see watchVolume /
	// SetVolume). volume_steps 100 makes the value a percent; initial_volume
	// seeds it with the box's real level so the Spotify app shows it correctly.
	b.WriteString("external_volume: true\n")
	b.WriteString("volume_steps: 100\n")
	fmt.Fprintf(&b, "initial_volume: %d\n", initialVol)
	// Always honour initial_volume on start instead of the last saved volume:
	// go-librespot persists the volume and restores it next start, which made
	// the Spotify app slider start at the stale/100 value instead of the box's
	// real level. With this, initial_volume (seeded from the box) wins.
	b.WriteString("ignore_last_volume: true\n")
	b.WriteString("server:\n")
	b.WriteString("  enabled: true\n")
	fmt.Fprintf(&b, "  address: %s\n", host)
	fmt.Fprintf(&b, "  port: %s\n", port)
	b.WriteString("credentials:\n")
	b.WriteString("  type: zeroconf\n")
	b.WriteString("  zeroconf:\n")
	b.WriteString("    persist_credentials: true\n")
	return b.String()
}

// boxNameAndVolume reads the speaker's friendly name and current volume from
// the Bose REST API. It returns the fallback name and volume 100 when the box
// is not reachable yet (cold boot), so config writing never blocks on it.
func (m *Manager) boxNameAndVolume(ctx context.Context) (name string, vol int) {
	name, vol = m.fallback, 100
	if m.box == nil {
		return name, vol
	}
	st, err := m.box.LoadSettings(ctx)
	if err != nil {
		return name, vol
	}
	if n := strings.TrimSpace(st.Info.Name); n != "" {
		name = n
	}
	if st.Volume.Actual >= 0 && st.Volume.Actual <= 100 {
		vol = st.Volume.Actual
	}
	return name, vol
}

func (m *Manager) ensureConfig(ctx context.Context) error {
	if err := os.MkdirAll(m.configDir, 0o755); err != nil {
		return err
	}
	name, vol := m.boxNameAndVolume(ctx)
	m.mu.Lock()
	m.name = name
	m.configVol = vol
	m.mu.Unlock()
	// No audio cache handling needed: go-librespot does not cache audio to
	// disk (verified in its source; only the tiny config + credential files
	// land in configDir). The NAND-filling cache seen earlier was the old
	// librespot (Rust, --cache), not go-librespot.
	return os.WriteFile(filepath.Join(m.configDir, "config.yml"), []byte(m.configYAML(name, vol)), 0o644)
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
	for ctx.Err() == nil {
		m.mu.Lock()
		paused := m.enginePaused
		m.mu.Unlock()
		if paused {
			// Held stopped so its NAND blocks could be reclaimed for an update;
			// idle (do NOT relaunch the removed binary) until a fresh engine is
			// delivered and RestartEngine clears the pause, or ctx ends.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if err := m.runOnce(ctx); err != nil && ctx.Err() == nil {
			m.logger.Warn("go-librespot exited, restarting", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// StopEngine stops the supervised go-librespot and blocks (bounded) until the
// process has actually exited, so the caller can remove the binary and truly free
// its ~16 MB on a tight NAND. Removing a still-running binary frees NOTHING: an
// exec'd file's blocks stay allocated until the last open handle closes, so a
// plain rm of a live engine reclaims no space (#119). The supervise loop stays
// paused (no relaunch of the removed file) until RestartEngine resumes it after
// the engine is re-delivered. No-op when the engine is not running.
func (m *Manager) StopEngine() {
	m.mu.Lock()
	m.enginePaused = true
	restart := m.runCancel
	var proc *os.Process
	if m.cmd != nil {
		proc = m.cmd.Process
	}
	m.mu.Unlock()
	if restart == nil {
		return // not running: nothing holds the binary, the rm frees it directly
	}
	m.logger.Info("spotify: stopping go-librespot to free its NAND blocks for an update")
	restart() // cancels the process context -> SIGKILL
	if proc == nil {
		return
	}
	// Wait for the kill to land so the unlinked-but-open blocks are released
	// before the caller removes the file. SIGKILL is near-instant; cap it so a
	// wedged process never blocks the OTA write indefinitely.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return // process gone, blocks released
		}
		time.Sleep(100 * time.Millisecond)
	}
	m.logger.Warn("spotify: go-librespot did not exit within the stop window; reclaim may free less", "pid", proc.Pid)
}

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

// RestartEngine relaunches the supervised go-librespot so a freshly delivered
// engine binary (POST /api/agent/sidecar) takes effect WITHOUT a box reboot.
// go-librespot binds its binary at process start, so a running engine otherwise
// keeps the OLD binary until the box restarts — and that extra OTA reboot is what
// dropped some boxes off Wi-Fi (#119). Cancelling the per-process context makes
// the supervise loop re-exec m.binPath (the new binary) on its next iteration.
// When the engine was not running yet (binary just appeared on an OTA-only box),
// runCancel is nil and waitForBinary starts it on its own, so this safely no-ops.
func (m *Manager) RestartEngine() {
	m.mu.Lock()
	wasPaused := m.enginePaused
	m.enginePaused = false
	restart := m.runCancel
	m.mu.Unlock()
	if wasPaused {
		// The supervise loop was idled by StopEngine (binary removed to free NAND).
		// Clearing the pause is enough: the loop relaunches the now-delivered binary
		// on its next tick. runCancel still points at the already-killed process.
		m.logger.Info("spotify: resuming go-librespot with the freshly delivered engine")
		return
	}
	if restart == nil {
		return // not running yet; the supervise loop's waitForBinary will start it
	}
	m.logger.Info("spotify: restarting go-librespot to activate a freshly delivered engine binary")
	restart()
}

// refreshVolumeConfigOnce rewrites config.yml with the box's real name and
// volume once the box REST API first answers, then restarts go-librespot a
// single time. config.yml is first written at agent start, usually before the
// box is up, so device_name and initial_volume fall back (volume 100), which
// made the Spotify app slider start at 100% and jump on first touch. With
// ignore_last_volume true and a correct initial_volume, go-librespot then
// reports the box's real level. One shot only (no polling), so it cannot flap
// like the old name watcher; skips the rewrite when already correct and the
// restart when a box is streaming.
func (m *Manager) refreshVolumeConfigOnce(ctx context.Context) {
	if m.box == nil {
		return
	}
	t := time.NewTicker(8 * time.Second)
	defer t.Stop()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if time.Now().After(deadline) {
			return
		}
		st, err := m.box.LoadSettings(ctx)
		if err != nil {
			continue // box REST not up yet
		}
		name := strings.TrimSpace(st.Info.Name)
		if name == "" {
			name = m.fallback
		}
		vol := st.Volume.Actual
		if vol < 0 || vol > 100 {
			vol = 100
		}
		m.mu.Lock()
		unchanged := name == m.name && vol == m.configVol
		streaming := m.sink != nil
		restart := m.runCancel
		m.mu.Unlock()
		if unchanged {
			return // initial config was already correct
		}
		if err := os.WriteFile(filepath.Join(m.configDir, "config.yml"),
			[]byte(m.configYAML(name, vol)), 0o644); err != nil {
			m.logger.Warn("spotify: refresh config failed", "err", err)
			return
		}
		m.mu.Lock()
		m.name = name
		m.configVol = vol
		m.mu.Unlock()
		m.logger.Info("spotify: refreshed config from box", "name", name, "vol", vol, "restart", !streaming)
		if !streaming && restart != nil {
			restart()
		}
		return // one shot
	}
}

// watchDeviceName re-resolves the speaker's friendly name periodically. When
// it changes (cold boot finally answering /info, or a user rename), it
// rewrites config.yml and restarts go-librespot, but only while no box is
// streaming so playback is never interrupted. This is what makes the Spotify
// Connect device and its local mDNS advert carry the speaker's own name.
func (m *Manager) watchDeviceName(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		name, vol := m.boxNameAndVolume(ctx)
		m.mu.Lock()
		changed := name != m.name
		streaming := m.sink != nil
		restart := m.runCancel
		m.mu.Unlock()
		if !changed || streaming {
			continue
		}
		if err := os.WriteFile(filepath.Join(m.configDir, "config.yml"),
			[]byte(m.configYAML(name, vol)), 0o644); err != nil {
			m.logger.Warn("spotify: rewrite config for name change failed", "err", err)
			continue
		}
		m.mu.Lock()
		m.name = name
		m.mu.Unlock()
		m.logger.Info("spotify: device name changed, restarting go-librespot", "name", name)
		if restart != nil {
			restart()
		}
	}
}

func (m *Manager) runOnce(ctx context.Context) error {
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

	// Drain go-librespot's Ogg output page by page and forward whole pages to
	// the box. While no box is attached, capture the current track's header
	// pages and pause go-librespot so it does not race to the end of the
	// playlist unheard; ServeOgg resumes it and replays the headers when a
	// box joins, so a mid-track joiner can still decode.
	r := bufio.NewReaderSize(stdout, 256*1024)
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
	for {
		page, err := readOggPage(r)
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
			if htype&0x02 != 0 && len(pending) > 0 {
				m.forward(sink, pending)
				pending = pending[:0]
			}
			// Batch pages into large writes (see flushThreshold) so the box
			// gets large chunks, not a tiny chunk per page.
			pending = append(pending, page...)
			forwarded += int64(len(page))
			if len(pending) >= flushBytes {
				m.forward(sink, pending)
				pending = pending[:0]
			}
			continue
		}
		// No consumer: drop any half-filled batch so a freshly attaching box
		// starts clean, then once a track's headers are captured pause
		// go-librespot so it stops producing (no racing) until a box attaches
		// and ServeOgg resumes it.
		pending = pending[:0]
		if !paused && haveHdr {
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

// forward writes p to the box sink; on write error it drops the sink.
func (m *Manager) forward(sink io.Writer, p []byte) {
	if _, err := sink.Write(p); err != nil {
		m.mu.Lock()
		if m.sink == sink {
			m.sink = nil
		}
		m.mu.Unlock()
		return
	}
	if f, ok := sink.(http.Flusher); ok {
		f.Flush()
	}
}

// readOggPage reads one complete Ogg page from r, syncing to the "OggS"
// capture pattern. The returned slice is a whole page (27-byte header +
// segment table + body).
func readOggPage(r *bufio.Reader) ([]byte, error) {
	for { // sync to "OggS"
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != 'O' {
			continue
		}
		p, err := r.Peek(3)
		if err != nil {
			return nil, err
		}
		if p[0] == 'g' && p[1] == 'g' && p[2] == 'S' {
			if _, err := r.Discard(3); err != nil {
				return nil, err
			}
			break
		}
	}
	// 23 bytes after "OggS": version, header_type, granule(8), serial(4),
	// page_seq(4), crc(4), page_segments(1).
	rest := make([]byte, 23)
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, err
	}
	numSegs := int(rest[22])
	segs := make([]byte, numSegs)
	if _, err := io.ReadFull(r, segs); err != nil {
		return nil, err
	}
	bodyLen := 0
	for _, s := range segs {
		bodyLen += int(s)
	}
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	page := make([]byte, 0, 4+23+numSegs+bodyLen)
	page = append(page, 'O', 'g', 'g', 'S')
	page = append(page, rest...)
	page = append(page, segs...)
	page = append(page, body...)
	return page, nil
}

// PlayOptions tunes a Spotify context recall.
type PlayOptions struct {
	// Shuffle starts the context on a random track with shuffle enabled, the
	// behaviour a preset saved with "shuffle" wants. When false (the default),
	// recall RESUMES the context on the last track that played from it (the
	// Spotify Connect "continue where you left off" behaviour) with shuffle OFF,
	// so the speaker's remote next/prev walk the playlist in order.
	Shuffle bool
}

// Play asks go-librespot to start playing a Spotify context (playlist/album/
// track) on this device, using its own cached credential. This is the
// autonomous-recall path: the agent calls it on a Spotify preset press, with no
// app involved.
//
// The recall calls Play FIRST, then points the box at /spotify/stream.ogg.
// With the sink-gated drain (see runOnce) go-librespot blocks on its full
// output pipe until the box attaches, so the pipe holds the track's start
// incl. the Ogg headers; the box therefore receives the stream from the
// beginning and can decode it.
//
// Shuffle is driven by opts, NOT forced on. The previous code force-enabled
// shuffle on EVERY recall, skipped to a random track, and never turned shuffle
// off again, so every preset press landed on a random song AND the remote's
// next key jumped around a still-shuffled queue (Patrick + Jens, 2026-06-25).
// Now: a default (non-shuffle) recall resumes the context where the user left
// off and keeps shuffle off; a shuffle preset starts on a fresh random track.
func (m *Manager) Play(ctx context.Context, uri string, opts PlayOptions) error {
	// Mark a recall in progress so ServeOgg does not resume the OLD (mid) track
	// when the box attaches; this path drives the chosen track from its start.
	m.SetRecalling()
	// Point the resume tracker at the context we are loading right now and drop
	// the previous track. The will_play event that normally sets lastContext can
	// lag or be missed, so without this a metadata/status event arriving after
	// the recall window would record the NEW context against the OLD track and
	// corrupt the resume store (review, 2026-06-25).
	m.mu.Lock()
	m.lastContext = uri
	m.curTrackURI = ""
	m.mu.Unlock()
	// Default recall resumes on the last track that played from this context
	// (skip_to_uri). A shuffle preset ignores the resume point and starts random.
	resumeURI := ""
	if !opts.Shuffle {
		resumeURI = m.resume.trackFor(uri)
	}
	// Load the context PAUSED so the speaker never hears the wrong (non-resumed /
	// non-shuffled) track. skip_to_uri positions the queue on the resume track
	// before any audio flows; an empty skip_to_uri starts at the context's first
	// track. We then wait for the context to load, set the desired shuffle state,
	// and resume, so audio starts cleanly on the intended track from its start.
	// The box buffers on the cached Ogg headers during the short paused window,
	// the same way it already buffers during a cold load.
	playReq := map[string]any{"uri": uri, "paused": true}
	if resumeURI != "" {
		playReq["skip_to_uri"] = resumeURI
	}
	playBody, _ := json.Marshal(playReq)
	if err := m.apiPostC(ctx, m.playClient, "/player/play", string(playBody)); err != nil {
		return err
	}
	// Belt-and-braces: stay paused even if this go-librespot build ignores the
	// paused flag in /player/play.
	_ = m.apiPost(ctx, "/player/pause", "")
	// shuffle_context is a no-op against an unloaded context (live: cold preset 6
	// then skipped to the deterministic 2nd track), so wait for the track to load.
	m.waitContextLoaded(ctx, 5*time.Second)
	// Set shuffle EXPLICITLY to the desired state every recall. Setting it to
	// false is what clears a stale shuffle left on by a previous shuffled recall
	// (the cross-recall stickiness that made an unshuffled preset still shuffle
	// and the remote next jump to a random song).
	if err := m.apiPost(ctx, "/player/shuffle_context",
		fmt.Sprintf(`{"shuffle_context":%t}`, opts.Shuffle)); err != nil {
		m.logger.Debug("spotify: shuffle_context failed", "err", err, "shuffle", opts.Shuffle)
	}
	if opts.Shuffle {
		// shuffle_context only randomises the UPCOMING queue (the current track
		// stays the context's first), so one skip lands on a random track. Still
		// paused, so nothing reaches the speaker yet.
		if err := m.apiPost(ctx, "/player/next", ""); err != nil {
			m.logger.Debug("spotify: skip-to-random after shuffle failed", "err", err)
		}
	}
	// Resume: audio now flows, starting on the chosen track from its beginning.
	if err := m.apiPost(ctx, "/player/resume", ""); err != nil {
		m.logger.Debug("spotify: resume after recall failed", "err", err)
	}
	m.logger.Info("spotify: recall play", "uri", uri, "shuffle", opts.Shuffle, "resumeTrack", resumeURI != "")
	// Debounce the will_play context change this recall triggers (this path
	// already drives the box separately, so no extra re-point needed).
	m.mu.Lock()
	m.lastActivate = time.Now()
	m.mu.Unlock()
	return nil
}

// waitContextLoaded polls go-librespot's /status until a track is loaded (the
// context is ready) or max elapses. Used by Play before shuffle_context, which
// is a no-op against an unloaded context.
func (m *Manager) waitContextLoaded(ctx context.Context, max time.Duration) {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if data, err := m.apiGet(ctx, "/status"); err == nil {
			var st struct {
				Track *struct {
					Name string `json:"name"`
				} `json:"track"`
			}
			if json.Unmarshal(data, &st) == nil && st.Track != nil && st.Track.Name != "" {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Next and Prev skip tracks. Wired to the SoundTouch remote's next/prev keys:
// the box cannot skip a UPnP source itself (it emits QPLAY_SKIP_*_FAILED), so
// STR catches that and skips here instead. The new track reaches the box after
// its buffer drains.
func (m *Manager) Next(ctx context.Context) error { return m.apiPost(ctx, "/player/next", "") }
func (m *Manager) Prev(ctx context.Context) error { return m.apiPost(ctx, "/player/prev", "") }

// Pause and Resume mirror the obvious controls.
func (m *Manager) Pause(ctx context.Context) error {
	return m.apiPost(ctx, "/player/pause", "")
}

func (m *Manager) Resume(ctx context.Context) error {
	return m.apiPost(ctx, "/player/resume", "")
}

// SwitchedAway is called when the user deliberately points the box at a
// non-Spotify source (a radio preset, an ad-hoc station). It suppresses the #14
// auto-attach for a window so the still-connected go-librespot session does not
// yank the box back to Spotify, and pauses go-librespot so the playlist does not
// keep advancing silently in the background. Starting Spotify again from the app
// or recalling a Spotify preset un-pauses it. No-op when Spotify is not running.
func (m *Manager) SwitchedAway(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.suppressActivateUntil = time.Now().Add(10 * time.Second)
	running := m.sink != nil || m.cmd != nil
	m.mu.Unlock()
	if !running {
		return
	}
	if err := m.Pause(ctx); err != nil {
		m.logger.Debug("spotify: pause on source-switch failed", "err", err)
	}
}

// SetVolume tells go-librespot the current volume as a percent (0..100) so the
// Spotify app's slider reflects the speaker's real level. With volume_steps
// 100 the API value is the percent directly. This is the box -> Spotify
// direction; the box -> go-librespot caller is the gabbo volumeUpdated hook.
func (m *Manager) SetVolume(ctx context.Context, pct int) error {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return m.apiPost(ctx, "/player/volume", fmt.Sprintf(`{"volume":%d}`, pct))
}

// ---- Multi-account credential swap (#27) ----
//
// go-librespot is a single-user receiver: it persists ONE zeroconf credential
// (credentials.json in configDir) and logs in as the last account that tapped
// the device. To recall a preset saved by a different household account, the
// manager keeps a per-account copy of each credential as it taps (captureLoop),
// then on a cross-account recall swaps the right copy into credentials.json and
// restarts go-librespot, which re-reads it at startup (it has no runtime login
// API). The restart takes ~3s, shorter than the box's playback buffer, so the
// switch is audibly seamless. Same-account recall does not switch or restart.

// apiGet fetches a go-librespot API path (e.g. /status) and returns the body.
func (m *Manager) apiGet(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+m.apiAddr+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// noteLibrespotLine inspects a go-librespot stderr line for the non-Premium
// account signal. librespot refuses free accounts and logs that it does not
// support them; seeing that latches sawFreeAccountLog so PremiumRequired can warn
// that preset recall needs Premium (#45).
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
}

// accountProduct returns the Spotify account product type ("premium"/"free"/
// "open") via go-librespot's authenticated Web API proxy (GET /web-api/v1/me),
// cached for a few minutes. Returns "" when unknown (the zeroconf token may lack
// the user-read-private scope, in which case /v1/me omits product), so callers
// fall back to the log signal. Best-effort.
func (m *Manager) accountProduct(ctx context.Context) string {
	m.mu.Lock()
	if m.productType != "" && time.Since(m.productCheckedAt) < 5*time.Minute {
		p := m.productType
		m.mu.Unlock()
		return p
	}
	m.mu.Unlock()
	data, err := m.apiGet(ctx, "/web-api/v1/me")
	if err != nil {
		return ""
	}
	var me struct {
		Product string `json:"product"`
	}
	if json.Unmarshal(data, &me) != nil || me.Product == "" {
		return ""
	}
	m.mu.Lock()
	m.productType, m.productCheckedAt = me.Product, time.Now()
	m.mu.Unlock()
	return me.Product
}

// PremiumRequired reports whether the current Spotify account cannot do the
// autonomous on-demand playback a preset recall needs, i.e. it is a free/open
// account rather than Premium (#45). Non-blocking: it uses the cached product
// type and the go-librespot free-account log signal, kicking a background product
// refresh when the type is not yet known. Conservative: returns true only on a
// POSITIVE non-Premium signal, never on "unknown", so a Premium user is never
// wrongly blocked.
func (m *Manager) PremiumRequired() bool {
	m.mu.Lock()
	free := m.sawFreeAccountLog
	p := m.productType
	tried := m.productTriedAt
	m.mu.Unlock()
	if free {
		return true
	}
	if p == "" && time.Since(tried) > 30*time.Second {
		m.mu.Lock()
		m.productTriedAt = time.Now()
		m.mu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			m.accountProduct(ctx)
		}()
	}
	return p == "free" || p == "open"
}

// currentUsername returns the Spotify account go-librespot is currently logged
// in as, or "" if it is not reachable / not authed yet.
func (m *Manager) currentUsername(ctx context.Context) string {
	data, err := m.apiGet(ctx, "/status")
	if err != nil {
		return ""
	}
	var st struct {
		Username string `json:"username"`
	}
	if json.Unmarshal(data, &st) != nil {
		return ""
	}
	return st.Username
}

// CurrentUsername is the exported form; the preset-save path stamps it onto a
// new Spotify preset so a later recall can switch back to that account.
func (m *Manager) CurrentUsername(ctx context.Context) string {
	return m.currentUsername(ctx)
}

// ErrNoSpotifySession is returned by PlayAccount when the speaker holds no live
// Spotify session and none could be re-established (no persisted credential, or
// the credential no longer authenticates, e.g. another controller took the
// account's single live session). The caller maps it to an actionable "tap this
// speaker in Spotify once" hint rather than letting the box buffer into nothing.
var ErrNoSpotifySession = errors.New("spotify: no live device session for recall")

// SessionActive reports whether go-librespot currently holds a live, authenticated
// device session (an active Connect device that can accept /player/play), as
// opposed to merely having a persisted credential on disk (LoggedIn). go-librespot
// auto-loads the persisted zeroconf credential at process start and re-auths on
// its own, but a cold start, a dropped AP connection (the "did not receive last
// pong ack" case), or a takeover can leave it momentarily logged out even though
// credentials.json exists. Recall checks this, not just LoggedIn.
func (m *Manager) SessionActive(ctx context.Context) bool {
	return m.currentUsername(ctx) != ""
}

// ensureSession makes go-librespot hold a live device session before a recall.
// No-op (true) when a session is already active. When a persisted credential
// exists but the session is dead (cold start / dropped AP), it restarts
// go-librespot so it reloads and re-authenticates from the cached credential,
// then waits (bounded) for an active session. Returns false when the box was
// never logged in, or when no session could be re-established within the window.
//
// Validated live on a taigan box: after an AP drop go-librespot logs "loading
// previously persisted zeroconf credentials" then "authenticated AP" with no
// fresh tap, which is exactly what this restart triggers. The one case it cannot
// recover is a credential invalidated by a takeover (Spotify's single-session
// rule); there it returns false and the caller shows the tap-once hint instead of
// looping restarts.
func (m *Manager) ensureSession(ctx context.Context) bool {
	if m.SessionActive(ctx) {
		return true
	}
	if !m.LoggedIn() {
		return false // never logged in: actionable as "tap this speaker once"
	}
	m.logger.Warn("spotify: persisted credential present but no live session; restarting go-librespot to re-auth before recall")
	m.mu.Lock()
	restart := m.runCancel
	m.mu.Unlock()
	if restart != nil {
		restart() // supervise loop relaunches go-librespot, which reloads the credential
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
		if m.SessionActive(ctx) {
			m.logger.Info("spotify: live session re-established for recall")
			return true
		}
	}
	m.logger.Warn("spotify: could not re-establish a live session for recall")
	return false
}

// LoggedIn reports whether this speaker has ever completed a Spotify Connect
// login, i.e. a reusable credential is persisted on disk. Recall needs this:
// without a credential go-librespot cannot start playback on its own, so the
// preset does nothing (#45 Pierre: the saved preset had account="" and
// go-librespot was not running). It is a filesystem check (not a :3678 query), so
// it stays true while go-librespot is mid-restart, distinguishing "never logged
// in" (actionable: log the speaker into Spotify first) from "logged in but
// momentarily down" (recovers on its own).
//
// Current go-librespot persists the zeroconf credential into configDir/state.json
// (under .credentials.username); credentials.json is only its read-only LEGACY
// fallback for installs predating that merged-state layout, so a box logged in
// via a current binary has state.json and NO credentials.json. Checking only
// credentials.json reported every fresh install as not-logged-in and silently
// blocked all Spotify recall, even with an active session (a user diagnostic,
// 2026-06-23: go-librespot loaded its persisted credential and authenticated,
// yet LoggedIn() was false). So check state.json too. go-librespot
// also writes a bare state.json (device_id/last_volume only) before any login, so
// require a non-empty persisted username there rather than mere file presence.
func (m *Manager) LoggedIn() bool {
	if stateHasCredential(filepath.Join(m.configDir, "state.json")) {
		return true
	}
	if _, err := os.Stat(filepath.Join(m.configDir, "credentials.json")); err == nil {
		return true
	}
	// A per-account credential copy (multi-account swap store) also counts: the
	// active credential can be briefly absent during a SwitchAccount swap.
	if entries, err := os.ReadDir(m.credStore); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				return true
			}
		}
	}
	return false
}

// CanRecall reports whether a Spotify preset recall can proceed: either
// go-librespot holds a LIVE session right now (SessionActive, e.g. the user just
// streamed to the box from their phone) OR a reusable credential is persisted on
// disk (LoggedIn, so ensureSession can restart go-librespot and re-auth from it).
//
// Recall must gate on this, NOT on LoggedIn alone. A box with a live-but-never-
// persisted zeroconf session (go-librespot authenticated the phone but wrote no
// credential to state.json) reports LoggedIn()==false yet plays Spotify fine;
// gating on LoggedIn alone refused recall on exactly such a box (Patrick, ST10
// rhino, 2026-06-24: streamed Spotify, go-librespot running, box flipped to
// source=SPOTIFY, yet the recall bailed "speaker not logged into Spotify"). Only
// when BOTH are false is the recall genuinely impossible, so the "tap this
// speaker in Spotify once" hint is correct. PlayAccount->ensureSession then
// handles the live vs cold-restart decision from here.
func (m *Manager) CanRecall(ctx context.Context) bool {
	return m.SessionActive(ctx) || m.LoggedIn()
}

// stateHasCredential reports whether go-librespot's state.json at path holds a
// persisted zeroconf credential (a non-empty credentials.username). go-librespot
// writes state.json with only device_id/last_volume before any login, so file
// presence alone is not proof of a credential.
func stateHasCredential(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var st struct {
		Credentials struct {
			Username string `json:"username"`
		} `json:"credentials"`
	}
	if json.Unmarshal(data, &st) != nil {
		return false
	}
	return st.Credentials.Username != ""
}

// sanitizeUser maps a Spotify username to a filesystem-safe credential filename.
func sanitizeUser(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// storedCredential is the on-disk shape STR uses for a captured, exported, or
// per-account-stored Spotify credential. It is exactly go-librespot's
// AppState.Credentials, so the same blob loads whether it lands in state.json
// (.credentials, the active account) or credentials.json (go-librespot's legacy
// read-only fallback). Data marshals as base64, matching go-librespot.
type storedCredential struct {
	Username string `json:"username"`
	Data     []byte `json:"data"`
}

// readStateCredential returns the credential go-librespot has persisted in
// state.json (its current store; credentials.json is only a legacy read-fallback
// and is never written by go-librespot). ok is false when none is present.
func (m *Manager) readStateCredential() (storedCredential, bool) {
	b, err := os.ReadFile(filepath.Join(m.configDir, "state.json"))
	if err != nil {
		return storedCredential{}, false
	}
	var s struct {
		Credentials storedCredential `json:"credentials"`
	}
	if json.Unmarshal(b, &s) != nil || s.Credentials.Username == "" || len(s.Credentials.Data) == 0 {
		return storedCredential{}, false
	}
	return s.Credentials, true
}

// writeActiveCredential makes cred the active account by setting .credentials in
// state.json, preserving every other field (device_id, last_volume, ...) so
// go-librespot logs in as that account on its next start. The restart SIGKILLs
// go-librespot (exec.CommandContext cancel), so the outgoing process never saves
// state on the way out and cannot clobber this write. Writing credentials.json
// instead would be ignored whenever the target's state.json already names an
// account, which is why an account switch must land here.
func (m *Manager) writeActiveCredential(cred storedCredential) error {
	if err := os.MkdirAll(m.configDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(m.configDir, "state.json")
	st := map[string]json.RawMessage{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &st) // best-effort merge; a missing/corrupt file just starts fresh
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	st["credentials"] = raw
	out, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

// captureCredential snapshots go-librespot's current persisted credential into
// the per-account store keyed by username, so a later recall of a preset stamped
// with that account can switch back to it. This is the multi-account case: two
// people each Connect to the same box, go-librespot persists whoever logged in
// last, and each must be saved so each preset replays under its own account.
// Reads state.json (go-librespot's current credential store).
func (m *Manager) captureCredential(user string) error {
	if user == "" {
		return nil
	}
	cred, ok := m.readStateCredential()
	if !ok {
		return fmt.Errorf("no spotify credential to capture")
	}
	blob, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.credStore, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.credStore, sanitizeUser(user)+".json"), blob, 0o600)
}

// captureLoop watches the active account and snapshots its credential whenever a
// new account taps the device (go-librespot rewrites state.json on each tap). Low
// NAND wear: it only writes on an account change or a missing copy.
func (m *Manager) captureLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		user := m.currentUsername(ctx)
		if user == "" {
			continue
		}
		if user == last {
			if _, err := os.Stat(filepath.Join(m.credStore, sanitizeUser(user)+".json")); err == nil {
				continue // already captured
			}
		}
		if err := m.captureCredential(user); err != nil {
			m.logger.Debug("spotify: capture credential failed", "user", user, "err", err)
			continue
		}
		last = user
		m.logger.Info("spotify: captured account credential", "user", user)
	}
}

// SwitchAccount makes go-librespot log in as username if it is not already. It
// returns (false, nil) with no restart when username is empty, already active,
// or has no stored credential (the recall then plays with the current account;
// public playlists still work). Otherwise it swaps the credential and restarts
// go-librespot, waiting until it re-auths as the target.
func (m *Manager) SwitchAccount(ctx context.Context, username string) (bool, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return false, nil
	}
	if cur := m.currentUsername(ctx); cur == username {
		return false, nil // already this account: no switch, no restart
	}
	data, err := os.ReadFile(filepath.Join(m.credStore, sanitizeUser(username)+".json"))
	if err != nil {
		m.logger.Info("spotify: no stored credential for account, playing with current", "want", username)
		return false, nil
	}
	var cred storedCredential
	if err := json.Unmarshal(data, &cred); err != nil || len(cred.Data) == 0 {
		m.logger.Info("spotify: stored credential unreadable, playing with current", "want", username, "err", err)
		return false, nil
	}
	if cred.Username == "" {
		cred.Username = username
	}
	if err := m.writeActiveCredential(cred); err != nil {
		return false, err
	}
	start := time.Now()
	m.mu.Lock()
	restart := m.runCancel
	m.mu.Unlock()
	if restart != nil {
		restart() // supervise loop relaunches go-librespot, which reads the swapped credential
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		cur := m.currentUsername(ctx)
		if cur == username {
			m.logger.Info("spotify: switched account", "user", username, "tookMs", time.Since(start).Milliseconds())
			return true, nil
		}
		// If go-librespot re-authed as a DIFFERENT account after the restart,
		// that account's app is still connected and overrides the credential
		// swap. Give up fast; the recall then plays with the active account
		// (public playlists still play). Spotify allows only one live session.
		if cur != "" && cur != username && time.Since(start) > 4*time.Second {
			m.logger.Warn("spotify: account switch overridden by a connected app", "want", username, "got", cur)
			return true, fmt.Errorf("account switch to %q overridden by connected app %q", username, cur)
		}
	}
	m.logger.Warn("spotify: account switch timed out", "want", username)
	return true, fmt.Errorf("account switch to %q timed out", username)
}

// PlayAccount switches to the preset's account (if needed) then plays the URI
// with the given options (shuffle vs resume). This is the recall entry point
// used by both the hardware-button and the desktop/API paths, so both honour
// the preset's shuffle flag and the per-context resume point identically.
func (m *Manager) PlayAccount(ctx context.Context, uri, account string, opts PlayOptions) error {
	// Diagnostic: log the live session state at the recall boundary so a bundle
	// disambiguates "never logged in" vs "dead session" vs "playing fine" without
	// guesswork (every Spotify-recall investigation hit this blind spot).
	m.logger.Info("spotify: recall start", "uri", uri, "wantAccount", account,
		"sessionUser", m.currentUsername(ctx), "loggedIn", m.LoggedIn())
	if account != "" {
		if _, err := m.SwitchAccount(ctx, account); err != nil {
			m.logger.Warn("spotify: account switch failed, playing with current account", "account", account, "err", err)
		}
	}
	// Even with no account switch (single-account / already-active case), make sure
	// go-librespot actually holds a live session before /player/play. Otherwise the
	// box buffers forever and detaches: the "recall finds an empty account" failure
	// that works on a box with a live session but fails on one whose session went
	// cold. ensureSession restarts go-librespot to reload the persisted credential
	// for the cold/dropped case; a box that is genuinely not logged in (or whose
	// credential a takeover invalidated) yields ErrNoSpotifySession so the caller
	// shows the tap-once hint instead of silently playing nothing.
	if !m.ensureSession(ctx) {
		return ErrNoSpotifySession
	}
	return m.Play(ctx, uri, opts)
}

// noteResume records the current track as the resume point for the current
// context, so a later default (non-shuffle) recall of that context continues on
// it. No-op during an in-flight recall (the track is still settling) and when
// the context or track URI is unknown. Called after every metadata/status
// update; the resume store itself ignores unchanged tracks, so this is cheap.
func (m *Manager) noteResume() {
	if m.resume == nil {
		return
	}
	// Take the recall-in-flight check and the context/track snapshot under one
	// lock, so the pair recorded is exactly the state at this instant (no
	// recheck-then-relock window). note() ignores an empty or non-spotify pair.
	m.mu.Lock()
	if time.Now().Before(m.recallUntil) {
		m.mu.Unlock()
		return
	}
	ctxURI, trackURI := m.lastContext, m.curTrackURI
	m.mu.Unlock()
	if ctxURI == "" || trackURI == "" {
		return
	}
	m.resume.note(ctxURI, trackURI)
}

// ExportCredential returns the active go-librespot credential (credentials.json)
// so it can be copied to another speaker. The blob is a reusable Spotify Connect
// credential for whatever account last logged in here; copying it to another box
// lets that box log into the SAME Spotify account without the user picking it in
// Spotify again. Returns an error when no credential is stored yet (the box was
// never logged in). LAN-only, same trust model as the rest of the agent API.
func (m *Manager) ExportCredential() ([]byte, error) {
	// The credential lives in state.json on a current go-librespot; export it as a
	// {username,data} blob the receiving box stages back in. Fall back to a legacy
	// credentials.json (same shape) for a box that predates the state.json layout.
	if cred, ok := m.readStateCredential(); ok {
		return json.Marshal(cred)
	}
	data, err := os.ReadFile(filepath.Join(m.configDir, "credentials.json"))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("no spotify credential stored")
	}
	return data, nil
}

// ImportCredential writes a credential blob exported from another speaker into
// this box's go-librespot config and restarts go-librespot so it logs in as that
// account. This is the receiving half of "log in once, sync to all speakers":
// the user logs into Spotify on one box, and STR copies that credential to the
// others so recall works everywhere without tapping each box in Spotify.
func (m *Manager) ImportCredential(ctx context.Context, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("empty credential")
	}
	if err := os.MkdirAll(m.configDir, 0o755); err != nil {
		return err
	}
	// The blob is go-librespot's {username,data} credential (from another box's
	// state.json, or a legacy credentials.json of the same shape). Set it as the
	// active account in state.json so go-librespot logs in as it on restart, even
	// when the target already named a different account (writing credentials.json
	// would then be ignored). A blob we cannot parse is staged as-is to
	// credentials.json (legacy fallback).
	var cred storedCredential
	if json.Unmarshal(data, &cred) == nil && cred.Username != "" && len(cred.Data) > 0 {
		if err := m.writeActiveCredential(cred); err != nil {
			return err
		}
	} else if err := os.WriteFile(filepath.Join(m.configDir, "credentials.json"), data, 0o600); err != nil {
		return err
	}
	m.logger.Info("spotify: imported credential from another speaker, restarting go-librespot")
	m.mu.Lock()
	restart := m.runCancel
	m.mu.Unlock()
	if restart != nil {
		restart() // supervise loop relaunches go-librespot, which reads the imported credential
	}
	return nil
}

// PlaylistMeta returns a stable cover image URL and the human title for a
// Spotify context URI (playlist, album, ...) via Spotify's public oEmbed
// endpoint, which needs no token. A saved preset uses the cover as its tile logo
// (#24) and the title as its name, so the box display and the tile show e.g.
// "Jens Chill" instead of a bare "Spotify". Returns "","" on any failure.
// Best-effort, called off the play path (on preset save).
func (m *Manager) PlaylistMeta(ctx context.Context, uri string) (cover, title string) {
	page := spotifyURItoURL(uri)
	if page == "" {
		return "", ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://open.spotify.com/oembed?url="+url.QueryEscape(page), nil)
	if err != nil {
		return "", ""
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ""
	}
	var od struct {
		ThumbnailURL string `json:"thumbnail_url"`
		Title        string `json:"title"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&od); err != nil {
		return "", ""
	}
	return od.ThumbnailURL, od.Title
}

// spotifyURItoURL converts spotify:playlist:ID (or album/track/artist) to its
// open.spotify.com page URL, or "" for an unrecognised URI.
func spotifyURItoURL(uri string) string {
	parts := strings.Split(uri, ":")
	if len(parts) != 3 || parts[0] != "spotify" {
		return ""
	}
	switch parts[1] {
	case "playlist", "album", "track", "artist":
		return "https://open.spotify.com/" + parts[1] + "/" + parts[2]
	}
	return ""
}

// Bitrate returns the bitrate measured from the live stream (kbit/s), or the
// configured nominal when nothing has streamed yet.
func (m *Manager) Bitrate() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.actualKbps > 0 {
		return m.actualKbps
	}
	return m.bitr
}

// Streaming reports whether a box is currently attached to the Ogg stream
// (i.e. Spotify is actively playing to the speaker). The memory guard uses
// this to avoid rebooting the box mid-playback.
func (m *Manager) Streaming() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sink != nil
}

// syncVolumeFromBox seeds go-librespot's volume with the box's real level so the
// Spotify app slider starts at the correct value. Without it go-librespot
// defaults to 100% (external_volume ignores initial_volume), so the first slider
// touch jumped the speaker to 100 and then back down to the chosen value.
func (m *Manager) syncVolumeFromBox(ctx context.Context) {
	if m.box == nil {
		return
	}
	st, err := m.box.LoadSettings(ctx)
	if err != nil {
		return
	}
	vol := st.Volume.Actual
	if vol < 0 || vol > 100 {
		return
	}
	set := func(v int) {
		vctx, c := context.WithTimeout(ctx, 4*time.Second)
		_ = m.SetVolume(vctx, v)
		c()
	}
	set(vol)
	// The Spotify app caches go-librespot's default (100) and only updates the
	// slider when it sees a volume CHANGE. Nudge to an adjacent value and back
	// so the app picks up the real level instead of showing 100 until the user
	// first touches the slider (Jens' idea).
	time.Sleep(1500 * time.Millisecond)
	nudge := vol - 1
	if nudge < 0 {
		nudge = vol + 1
	}
	set(nudge)
	time.Sleep(250 * time.Millisecond)
	set(vol)
	m.logger.Info("spotify: seeded + nudged app volume slider from box", "vol", vol)
}

// SetRecalling marks a preset recall as in progress for the next few seconds, so
// ServeOgg does not resume the old (mid-position) track when the box attaches;
// Play drives the new track from its start instead. Called at the very start of
// a recall, before the box attaches.
func (m *Manager) SetRecalling() {
	m.mu.Lock()
	m.recallUntil = time.Now().Add(8 * time.Second)
	m.mu.Unlock()
}

// recalling reports whether a recall is currently in progress.
func (m *Manager) recalling() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Now().Before(m.recallUntil)
}

// SetOnActivate wires the callback that points the box at the Spotify stream
// when the user starts playback from the Spotify app while the box is on
// another source (#14).
func (m *Manager) SetOnActivate(f func(context.Context)) {
	m.mu.Lock()
	m.onActivate = f
	m.mu.Unlock()
}

// maybeActivate fires onActivate when go-librespot has become active/playing but
// no box is attached to the Ogg stream (the box is on another source). Debounced
// so a burst of events triggers at most one box switch. No-op when a box is
// already attached (e.g. a normal preset recall already pointed it here).
func (m *Manager) maybeActivate() {
	m.mu.Lock()
	cb := m.onActivate
	if cb == nil || m.sink != nil || time.Since(m.lastActivate) < 5*time.Second ||
		time.Now().Before(m.suppressActivateUntil) {
		m.mu.Unlock()
		return
	}
	m.lastActivate = time.Now()
	m.mu.Unlock()
	m.logger.Info("spotify: app playback detected with box on another source, switching box to Spotify stream")
	go cb(context.Background())
}

// repointBox re-points the box at the Spotify stream even if it is already
// attached, so a playlist switch from the app flushes the box buffer and plays
// the new stream promptly. Debounced and shares lastActivate with maybeActivate.
func (m *Manager) repointBox() {
	m.mu.Lock()
	cb := m.onActivate
	if cb == nil || time.Since(m.lastActivate) < 5*time.Second ||
		time.Now().Before(m.suppressActivateUntil) {
		m.mu.Unlock()
		return
	}
	m.lastActivate = time.Now()
	m.mu.Unlock()
	m.logger.Info("spotify: playlist context changed, re-pointing box to play the new stream")
	go cb(context.Background())
}

// DeviceName returns the name currently advertised to Spotify.
func (m *Manager) DeviceName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.name
}

// liveNowPlaying pulls the current track straight from go-librespot's /status,
// the authoritative source, and refreshes the cache with it. The cached values
// come from pushed "metadata" events, which lag (and can be missed entirely):
// a live capture showed /spotify/info still reporting an earlier track while
// go-librespot had advanced several (#136). Pulling /status on demand keeps the
// desktop now-playing line in step with what is actually playing. Best-effort:
// returns false and leaves the cache untouched if /status is unreachable or
// carries no track, so the caller falls back to the cached values.
func (m *Manager) liveNowPlaying(ctx context.Context) (track, artist, cover string, ok bool) {
	data, err := m.apiGet(ctx, "/status")
	if err != nil {
		return "", "", "", false
	}
	var st struct {
		Track *struct {
			URI           string   `json:"uri"`
			Name          string   `json:"name"`
			ArtistNames   []string `json:"artist_names"`
			AlbumCoverURL string   `json:"album_cover_url"`
		} `json:"track"`
	}
	if json.Unmarshal(data, &st) != nil || st.Track == nil || st.Track.Name == "" {
		return "", "", "", false
	}
	track = st.Track.Name
	artist = strings.Join(st.Track.ArtistNames, ", ")
	cover = st.Track.AlbumCoverURL
	m.mu.Lock()
	m.curName, m.curArtist, m.curCover = track, artist, cover
	if st.Track.URI != "" {
		m.curTrackURI = st.Track.URI
	}
	m.mu.Unlock()
	m.notifyTrack()
	m.noteResume()
	return track, artist, cover, true
}

// SetOnTrack registers the recently-played hook (webui.NoteRecentSpotifyTrack).
func (m *Manager) SetOnTrack(fn func(track, artist string)) {
	m.mu.Lock()
	m.onTrack = fn
	m.mu.Unlock()
}

// notifyTrack fires onTrack when the current Spotify track changed since the
// last notification, so each song is recorded once. Called after every
// metadata/status update; the dedup on the track name keeps a repeated /status
// poll from re-recording. The callback runs outside the lock. Cheap (#135).
func (m *Manager) notifyTrack() {
	m.mu.Lock()
	cb, track, artist := m.onTrack, m.curName, m.curArtist
	if cb == nil || track == "" || track == m.lastNotifiedTrack {
		m.mu.Unlock()
		return
	}
	m.lastNotifiedTrack = track
	m.mu.Unlock()
	cb(track, artist)
}

// ServeInfo answers GET /spotify/info with the live state the UI needs: whether
// Spotify is available, the measured bitrate, and the advertised device name.
func (m *Manager) ServeInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	m.mu.Lock()
	track, artist, cover, context := m.curName, m.curArtist, m.curCover, m.lastContext
	m.mu.Unlock()
	// Prefer the live track from /status over the laggy cached metadata events.
	if lt, la, lc, ok := m.liveNowPlaying(r.Context()); ok {
		track, artist, cover = lt, la, lc
	}
	resp := struct {
		Ready   bool   `json:"ready"`
		Bitrate int    `json:"bitrate"`
		Name    string `json:"name"`
		Track   string `json:"track"`
		Artist  string `json:"artist"`
		Cover   string `json:"cover"`
		Context string `json:"context"` // current playlist/album URI (for saving a Spotify preset)
		Account string `json:"account"` // current go-librespot login (for the preset)
		// PremiumRequired is true when the logged-in Spotify account is free/open,
		// which cannot do the autonomous on-demand playback a preset recall needs
		// (#45). The UI shows a "recall needs Premium" note when set.
		PremiumRequired bool `json:"premiumRequired"`
	}{
		Ready:           m.Ready(),
		Bitrate:         m.Bitrate(),
		Name:            m.DeviceName(),
		Track:           track,
		Artist:          artist,
		Cover:           cover,
		Context:         context,
		Account:         m.currentUsername(r.Context()),
		PremiumRequired: m.PremiumRequired(),
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// watchVolume subscribes to go-librespot's /events WebSocket and mirrors every
// Spotify-app volume change onto the box. go-librespot runs with
// external_volume, so a Connect volume command does not touch its audio; it
// surfaces here as a "volume" event {value, max} which we scale to a percent
// and push to the box over the Bose REST API. Reconnects with a short backoff.
func (m *Manager) watchVolume(ctx context.Context) {
	url := "ws://" + m.apiAddr + "/events"
	for ctx.Err() == nil {
		if err := m.volumeStream(ctx, url); err != nil && ctx.Err() == nil {
			m.logger.Debug("spotify: events stream ended", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (m *Manager) volumeStream(ctx context.Context, url string) error {
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := d.DialContext(ctx, url, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Closer goroutine is bound to a per-call context cancelled on return, so
	// it never outlives this stream. The earlier version waited on the
	// long-lived parent ctx and leaked one goroutine (holding conn) per
	// reconnect; with frequent go-librespot restarts that fed an OOM.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-sctx.Done(); conn.Close() }()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var ev struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "active":
			// The Spotify app selected this device. Switch the box to the Spotify
			// stream if it is on another source (#14), and seed the app's volume
			// slider with the box's real level so the first slider touch does not
			// jump the speaker to 100% first.
			m.maybeActivate()
			go m.syncVolumeFromBox(context.Background())
		case "playing":
			m.maybeActivate()
		case "will_play":
			// A track is about to play; if its context (playlist/album) differs
			// from the last one, the app switched playlists. Re-point the box so
			// it drops the old buffer and plays the new stream promptly.
			var wp struct {
				ContextURI string `json:"context_uri"`
			}
			if json.Unmarshal(ev.Data, &wp) == nil && wp.ContextURI != "" {
				m.mu.Lock()
				changed := m.lastContext != "" && wp.ContextURI != m.lastContext
				m.lastContext = wp.ContextURI
				if changed {
					// New context: drop the previous track so noteResume cannot
					// pair this context with the old track before its own
					// metadata lands (review, 2026-06-25).
					m.curTrackURI = ""
				}
				m.mu.Unlock()
				if changed {
					m.repointBox()
				}
			}
		case "metadata":
			// Current track info for the desktop (and later box) display.
			var md struct {
				URI           string   `json:"uri"`
				Name          string   `json:"name"`
				ArtistNames   []string `json:"artist_names"`
				AlbumCoverURL string   `json:"album_cover_url"`
			}
			if err := json.Unmarshal(ev.Data, &md); err != nil {
				continue
			}
			m.mu.Lock()
			m.curName = md.Name
			m.curArtist = strings.Join(md.ArtistNames, ", ")
			m.curCover = md.AlbumCoverURL
			if md.URI != "" {
				m.curTrackURI = md.URI
			}
			m.mu.Unlock()
			m.notifyTrack()
			// Remember this track as the resume point for its context, so a
			// later default recall continues here instead of restarting the
			// playlist (the events stream covers the no-desktop-app case).
			m.noteResume()
		case "volume":
			if m.box == nil {
				continue // no box client: metadata only, no volume mirror
			}
			var vd struct {
				Value int `json:"value"`
				Max   int `json:"max"`
			}
			if err := json.Unmarshal(ev.Data, &vd); err != nil {
				continue
			}
			pct := 100
			if vd.Max > 0 {
				pct = vd.Value * 100 / vd.Max
			}
			sctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			if err := m.box.SetVolume(sctx, pct); err != nil {
				m.logger.Debug("spotify: box SetVolume from Spotify event failed", "err", err, "pct", pct)
			}
			cancel()
			m.logger.Info("spotify: volume mirrored to box", "pct", pct)
		}
	}
}

// jsonString quotes a string as a JSON value.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (m *Manager) apiPost(ctx context.Context, path string, body string) error {
	return m.apiPostC(ctx, m.client, path, body)
}

func (m *Manager) apiPostC(ctx context.Context, client *http.Client, path string, body string) error {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+m.apiAddr+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("go-librespot %s: %w", path, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("go-librespot %s: status %d", path, resp.StatusCode)
	}
	return nil
}

// ServeOgg streams go-librespot's live Ogg/Vorbis passthrough output to the
// HTTP client (the box's UPnP fetch) until it disconnects. It registers as
// the single consumer; a new request replaces any previous one. No header is
// prepended: the box decodes the raw Ogg directly.
// Re-attach storm damping (#136, #113). A re-attach closer together than the
// window counts toward a storm and grows the box-re-point backoff from the base
// up to the cap; anything more spaced out is treated as a normal switch and
// clears the backoff.
const (
	spotifyStormWindow         = 20 * time.Second
	spotifyActivateBackoffBase = 5 * time.Second
	spotifyActivateBackoffMax  = 60 * time.Second
)

func (m *Manager) ServeOgg(w http.ResponseWriter, r *http.Request) {
	if !m.Ready() {
		http.Error(w, "spotify not configured", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "audio/ogg")
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)

	// Replay the current track's cached Ogg header pages first so a box that
	// joins mid-track has the identification/comment/setup headers it needs
	// to initialise the decoder; the live pages (forwarded by the drain)
	// then follow and are decodable even though they start mid-track.
	m.mu.Lock()
	hdr := append([]byte(nil), m.headerPages...)
	m.mu.Unlock()
	if len(hdr) > 0 {
		if _, err := w.Write(hdr); err != nil {
			return
		}
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	done := make(chan struct{})
	cw := &closeNotifyWriter{w: w, done: done}
	m.mu.Lock()
	oldSink, _ := m.sink.(*closeNotifyWriter) // previous consumer, if any
	reattach := m.sink != nil                 // a consumer was already attached = box re-fetched
	m.sink = cw
	sinceLast := time.Duration(0)
	if !m.lastAttachAt.IsZero() {
		sinceLast = time.Since(m.lastAttachAt)
	}
	m.lastAttachAt = time.Now()
	m.mu.Unlock()
	// Single-connection invariant: tear down the previous box connection now.
	// A box stuck in INVALID_SOURCE re-fetches the stream repeatedly; if the old
	// connections are left open they pile up and the box leaks decode/socket
	// buffers per connection until it OOMs (garbled audio then reboot, live
	// 2026-06-10). Closing the old sink makes its ServeOgg return and drop the
	// stale connection, so the box only ever holds one Ogg stream at a time.
	if oldSink != nil && oldSink != cw {
		oldSink.closeConn()
	}
	// Surface and damp a re-attach storm (the box re-fetching every few seconds,
	// the INVALID_SOURCE re-point loop heard as the song restarting). The
	// single-connection invariant above already prevents the per-connection
	// buffer pile-up that used to OOM the box; here we also back off STR's own
	// re-pointing so it stops shoving the box back into the same failing state.
	// A rapid re-attach grows the backoff (capped); a healthy, spaced-out attach
	// resets it so normal playlist switches stay responsive.
	if reattach && sinceLast > 0 && sinceLast < spotifyStormWindow {
		m.mu.Lock()
		if m.activateBackoff < spotifyActivateBackoffBase {
			m.activateBackoff = spotifyActivateBackoffBase
		} else {
			m.activateBackoff *= 2
			if m.activateBackoff > spotifyActivateBackoffMax {
				m.activateBackoff = spotifyActivateBackoffMax
			}
		}
		backoff := m.activateBackoff
		if t := time.Now().Add(backoff); t.After(m.suppressActivateUntil) {
			m.suppressActivateUntil = t
		}
		m.mu.Unlock()
		m.logger.Warn("spotify: rapid Ogg re-attach (INVALID_SOURCE re-point storm); backing off box re-point",
			"sinceLastMs", sinceLast.Milliseconds(), "backoff", backoff.String())
	} else if reattach && sinceLast >= spotifyStormWindow {
		// A spaced-out re-attach is normal (a deliberate playlist switch): the
		// storm has cleared, so drop the accumulated backoff.
		m.mu.Lock()
		m.activateBackoff = 0
		m.mu.Unlock()
	}
	// reattach=true means the box dropped and re-fetched the stream (the prime
	// suspect for a track appearing to restart): it then gets the cached
	// granule-0 headers again. Logged so the restart can be correlated.
	m.logger.Info("spotify: box attached to Ogg stream", "remote", r.RemoteAddr, "headerBytes", len(hdr), "reattach", reattach)

	// A fresh (non-reattach) attach is a clean recall start, not a storm: clear
	// any accumulated re-point backoff so the next genuine playlist switch is
	// handled promptly.
	if !reattach {
		m.mu.Lock()
		m.activateBackoff = 0
		m.mu.Unlock()
	}

	// On a FRESH attach (not a re-fetch), the box's own preset self-activation
	// can reach ServeOgg a beat BEFORE the gabbo press event flags the recall
	// (race, seen when switching from radio to a Spotify preset). Wait briefly
	// for the flag so we don't resume the old (mid) track before Play loads the
	// new shuffled one.
	if !reattach {
		for i := 0; i < 10 && !m.recalling(); i++ {
			time.Sleep(50 * time.Millisecond)
		}
	}
	// The drain pauses go-librespot while no box is attached; resume it so the
	// live stream flows to this box. Skip the resume ONLY on a FRESH recall
	// attach (reattach == false): there, resuming would replay the old track at
	// its mid position before Play loads the new shuffled track, and Play drives
	// playback instead. On a RE-attach (reattach == true) always resume: a recall
	// that restarted go-librespot (a cross-account switch) leaves it paused in
	// the restart gap, and without this resume the box would stay buffering on a
	// paused stream (observed: preset stuck after playing another account).
	if reattach || !m.recalling() {
		rctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		_ = m.Resume(rctx)
		cancel()
	}

	select {
	case <-r.Context().Done():
	case <-done:
	}
	m.mu.Lock()
	if m.sink == cw {
		m.sink = nil
	}
	m.mu.Unlock()
	m.logger.Info("spotify: box detached from Ogg stream")
}

// closeNotifyWriter signals done on the first failed write so ServeOgg
// returns when the box drops the connection.
type closeNotifyWriter struct {
	w    io.Writer
	done chan struct{}
	once sync.Once
}

func (c *closeNotifyWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if err != nil {
		c.once.Do(func() { close(c.done) })
	}
	return n, err
}

// closeConn tears the connection down from the manager side, used to enforce
// the single-connection invariant when a new box attaches. Idempotent.
func (c *closeNotifyWriter) closeConn() {
	c.once.Do(func() { close(c.done) })
}

func (c *closeNotifyWriter) Flush() {
	if f, ok := c.w.(http.Flusher); ok {
		f.Flush()
	}
}

func splitHostPort(addr string) (host, port string) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "127.0.0.1", "3678"
	}
	return h, p
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
