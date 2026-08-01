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
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

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

	// groupSlaveIPsFn returns the LAN IPs of the multiroom followers this box
	// leads (empty when standalone). A Spotify Connect volume change targets the
	// whole group, but go-librespot runs only on the master, so the manager
	// mirrors the volume onto each follower too. Wired by the agent from the
	// zones store; nil = no propagation.
	groupSlaveIPsFn func() []string
	// groupVolumeSetFn pushes one follower's volume. Defaults to the HTTP
	// implementation (follower agent first, then the Bose port); a field so
	// tests can count/observe fan-outs without a network.
	groupVolumeSetFn func(ctx context.Context, ip string, pct int) error
	credStore        string // per-account credential copies for multi-account swap

	mu sync.Mutex
	// selfVolUntil marks a volume change as caused by the manager itself (the
	// slider seed + nudge in syncVolumeFromBox): go-librespot echoes API
	// volume changes as "volume" events, and the group fan-out must ignore
	// those or every device activation rewrites the followers' individually
	// set levels.
	selfVolUntil time.Time
	// Connect intent (see SetConnectIntentHooks): hooks into the box-side
	// stop/play latches, plus the stamp that filters the engine's echoes of
	// STR's OWN /player commands out of the intent signal.
	connectPauseFn   func(event string)
	connectPlayFn    func()
	lastOwnPlayerCmd time.Time
	// volFanCh feeds the fan-out worker with latest-value coalescing, so the
	// go-librespot event loop never blocks on follower HTTP calls (an offline
	// follower costs seconds, and a slider drag emits event bursts).
	volFanCh     chan int
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
	// lastPlayFailLine/-At remember go-librespot's most recent "failed handling
	// request play" stderr line, so a bare /player/play 500 can carry the real
	// reason (e.g. Spotify's audio-key denial on a non-Premium account, #311).
	lastPlayFailLine string
	lastPlayFailAt   time.Time
	// lastSeekFailAt remembers when go-librespot last reported it could not seek to
	// the requested resume track (skip_to_uri) because that track is no longer in
	// the context (a volatile Radio/Daily-Mix playlist whose track set drifted).
	// Play uses it to replay the context from the top instead of leaving the box on
	// a stalled, never-loading stream (ST30 intermittent-silence recall, #recall).
	lastSeekFailAt time.Time
	// desyncAt collects recent Connect-desync markers from the engine's
	// stderr (put-state failures, dealer receive failures); lastDesyncHeal
	// rate-limits the self-heal restart. See noteDesyncSignature.
	desyncAt       []time.Time
	lastDesyncHeal time.Time
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
	// recallRestartAt is when a cross-account SwitchAccount last restarted
	// go-librespot. ServeOgg uses it to tell a cross-account recall (which leaves
	// the engine paused in the restart gap and must be resumed on re-attach) apart
	// from a same-account preset switch (where resuming replays the OLD playlist's
	// track for a few seconds until Play loads the new one: the preset-switch audio
	// overlap, ST30 2026-07-14).
	recallRestartAt time.Time
	// engineHotUntil suppresses the drain's "no sink -> pause go-librespot" during
	// a recall. On a HARDWARE Spotify preset press the box first activates its own
	// stored ContentItem, which 1036s and flaps its UPnP source through
	// INVALID_SOURCE for several seconds; each flap drops the Ogg sink, and the
	// drain paused go-librespot the instant the sink went away. The box then
	// settled into a stable attach ~10 s later but the engine was stranded paused
	// and never revived, so it buffered header-only and never played (forwardedKB=0,
	// live .79 v0.9.18 2026-07-24). The soft/app path never hit this because the
	// box does not self-activate there, so the sink stays attached and the engine
	// never pauses. Keeping the engine playing across the flap means the box gets
	// live audio the instant it re-attaches. Guarded by mu.
	engineHotUntil time.Time
	// Per-attachment sink counters. They exist to answer the one question a
	// bundle could not answer before: did the box actually RECEIVE audio, or
	// did it sit on an attached-but-silent stream until the Bose transport
	// gave up (field 2026-07-27: a preset that "plays a few seconds", another
	// on the same box that plays fine). Reset on every attach, logged on
	// detach. Guarded by m.mu like the sink itself.
	sinkAttachedAt time.Time
	// skipCutUntil arms the boundary skip-cut: a BOS arriving before this
	// moment was caused by a user skip, so the old track's unsent tail is
	// dropped instead of flushed (NoteSkip / skipCutArmed).
	skipCutUntil     time.Time
	sinkBytes        int64
	sinkPages        int64
	sinkFirstAudioAt time.Time
	sinkLastPageAt   time.Time
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
	// lowDisk is set when the configDir filesystem is below spotifyMinFreeBytes, so
	// go-librespot is not started (it cannot persist its credential on a full NAND).
	// Surfaced in ServeInfo so the desktop app shows "box NAND full" instead of
	// Spotify silently appearing unavailable. lastLowDiskLogAt throttles the warning.
	lowDisk          bool
	lowDiskFreeKB    int64
	lastLowDiskLogAt time.Time
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
