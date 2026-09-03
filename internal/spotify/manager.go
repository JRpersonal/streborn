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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// Manager supervises one go-librespot process and brokers its PCM output
// (as a live WAV stream) to at most one HTTP consumer (the speaker),
// plus drives playback through go-librespot's local HTTP API.
type Manager struct {
	binPath   string
	configDir string
	fallback  string // device name used until the box's friendly name is known
	apiAddr   string // host:port of go-librespot's HTTP API
	logger    *slog.Logger
	bitr      int // 96/160/320 (persisted preference, see quality.go)
	// bitrPending: a bitrate change arrived while the box was streaming; the
	// config is written, only the engine restart is owed. The box's next
	// detach from the Ogg stream performs it (applyPendingBitrateAfterDetach).
	bitrPending bool
	client      *http.Client   // short ops: pause/resume/volume/info
	playClient  *http.Client   // /player/play: a cold playlist load can take >5s
	box         *boxapi.Client // box REST: friendly name (device_name) + volume bridge

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
	// lastConnectPauseAt is when a REAL pause/stop/transfer from the Spotify
	// app was last seen (echoes of STR's own commands excluded). ServeOgg's
	// attach-resume gate reads it: the box starved by that pause drains its
	// buffer and re-fetches the stream, and resuming the engine for that fetch
	// restarted the very playback the user just paused (Klaus, 2026-08).
	// Cleared as soon as the engine reports playback started.
	lastConnectPauseAt time.Time
	// volFanCh feeds the fan-out worker with latest-value coalescing, so the
	// go-librespot event loop never blocks on follower HTTP calls (an offline
	// follower costs seconds, and a slider drag emits event bursts).
	volFanCh     chan int
	name         string    // device name currently written to config.yml
	configVol    int       // initial_volume currently written to config.yml
	sink         io.Writer // current HTTP consumer, nil when none
	lastAttachAt time.Time // when the box last attached to the Ogg stream (re-attach storm detection)
	lastDetachAt time.Time // when the box last detached; the warm-recall gate's "streaming until a moment ago"
	// expectReattachUntil marks the next re-attach as deliberately caused by
	// STR's own re-push (one-shot, see ExpectReattach), keeping it out of the
	// storm accounting.
	expectReattachUntil time.Time
	cmd                 *exec.Cmd
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
	// lastCtxResolveFailAt is when go-librespot last failed to resolve the
	// continuation of a context ("failed resolving station"). That happens when
	// a generated radio playlist runs out, and the engine then STOPS. Without
	// this the stop was indistinguishable from somebody pressing stop in the
	// Spotify app, so STR armed its deliberate-stop latch and the music simply
	// ended, on every speaker of the group at once (live 2026-08-15 16:56 and
	// 2026-08-16 16:58, both times reported as something else).
	lastCtxResolveFailAt time.Time
	// lastTrackLoadFailAt is when go-librespot last failed to LOAD a track
	// ("failed advancing to next track" / "failed loading current track"), a
	// transient Spotify-side failure mid-playlist. The stop that follows must
	// not arm the deliberate-stop latch (live 2026-08-21: a six-speaker group
	// fell silent after six songs), and one bounded auto-advance tries the
	// NEXT track instead. lastAutoAdvanceAt rate-limits that advance so an
	// account whose every track fails (PlayPlay cohort) cannot skip-storm.
	lastTrackLoadFailAt time.Time
	lastAutoAdvanceAt   time.Time
	// lastEngineActiveAt is the last moment the engine demonstrably played:
	// a playing/active event or a fresh Ogg track boundary. The delayed
	// auto-advance below compares it against the stop moment, because the
	// engine frequently RECOVERS from a "failed advancing" on its own a few
	// seconds later (live 2026-08-21 13:33, three times in one playlist);
	// advancing then skips the very track the engine just loaded, heard as
	// "the next song plays two seconds and jumps to the one after".
	lastEngineActiveAt time.Time
	// selfRecoveryWait overrides how long the auto-advance waits for that
	// self-recovery; 0 means the production default. Injected by tests.
	selfRecoveryWait time.Duration

	// zeroconfLabel is the bare mDNS label the engine advertises as its SRV
	// target, empty until the agent's own responder is answering for it.
	zeroconfLabel string

	// Spotify refusing the audio key for track after track. The engine logs one
	// warning per track and gives up after a run of them, which from the sofa
	// looks like a playlist racing past without a note ever playing, with
	// nothing on screen saying why. Two reports so far (#78 and an
	// eleven-speaker fleet on 2026-08-16), both with the native Bose Spotify
	// entry still playing the same account fine, because Bose is a licensed
	// client and this engine is not.
	//
	// Latched with a timestamp rather than a bare flag: a single refusal can be
	// one unavailable track, and a user who fixes the cause must not be told
	// about it forever.
	lastKeyRefusalAt   time.Time
	keyRefusalRun      int
	keyRefusalGaveUpAt time.Time
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
	lastSkipBoundary time.Time
	// lastBoundaryStaleBytes snapshots, at the moment a cut boundary was
	// stamped, how much non-header audio the current attachment had already
	// been fed. ~0 means the cut kept the box's buffer clean, so the recall
	// re-push can stand down instead of flapping the stream for nothing
	// (LastBoundaryStaleKB).
	lastBoundaryStaleBytes int64
	sinkBytes              int64
	sinkPages              int64
	sinkFirstAudioAt       time.Time
	sinkLastPageAt         time.Time
	// lastContext is the Spotify context (playlist/album) URI go-librespot last
	// announced via will_play. When it changes (the app switched to another
	// playlist) the box is re-pointed at the stream so it drops its buffer and
	// plays the new playlist promptly instead of finishing the old buffer.
	lastContext string
	// durQueueMs / streamTrackDurMs feed the app-skip detector: every engine
	// "loaded track" line queues that track's duration, and each BOS shifts
	// the queue so streamTrackDurMs names the duration of the track whose
	// pages are currently being forwarded. A BOS arriving while the forwarded
	// granule is clearly short of that duration is a mid-track cut the agent
	// did not issue (the Spotify app's own Next/Prev), and the box must drop
	// its buffered tail like the STR skip paths do; without that, an app skip
	// only became audible once the box had drained its whole pacing buffer,
	// which a listener clocked at over 20 seconds (field, 2026-08-29).
	durQueueMs       []int64
	streamTrackDurMs int64
	// pendingRepointFrom/To hold a context change announced by will_play that
	// has not been acted on yet.
	//
	// will_play says what the engine is going to play NEXT, which on a
	// generated radio playlist arrives while the current song is still
	// running. Re-pointing there tore the box off the stream mid-song, and
	// the engine then started the announced track: the song the listener was
	// hearing was cut off BY the re-point, which reads as the speaker
	// skipping on its own (live 2026-08-15 17:40:52, reported as "the
	// previous track was not over yet"). The re-point now waits for that
	// track to actually start.
	pendingRepointFrom string
	pendingRepointTo   string
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
		bitr:       loadBitrate(bitratePath(configDir)),
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
	// Only when the set was captured at the CURRENT bitrate: Vorbis codebooks
	// differ per profile, and a mismatched replay decodes to noise (live on
	// the Portable, 2026-08-26). A set without a marker (pre-marker agent) is
	// discarded once and re-persisted with one on the next play.
	if b, err := os.ReadFile(m.hdrPath); err == nil && len(b) > 0 {
		if kb, kerr := os.ReadFile(m.hdrPath + ".kbps"); kerr == nil &&
			strings.TrimSpace(string(kb)) == strconv.Itoa(m.bitr) {
			m.headerPages = b
			m.hdrPersisted = true
		} else {
			_ = os.Remove(m.hdrPath)
			_ = os.Remove(m.hdrPath + ".kbps")
		}
	}
	return m
}

// SetZeroconfHost gives the engine the bare mDNS label this speaker answers
// for. The caller must only supply it once its own responder is live: an engine
// pointed at a name nobody answers is worse off than one pointed at the chassis
// codename, because at least the codename is sometimes still in a router's
// cache.
//
// Takes effect at the next engine start. Nothing here restarts it: the engine
// is started once at boot, and forcing an extra restart to apply a name would
// cost audio for a cosmetic-looking reason.
func (m *Manager) SetZeroconfHost(label string) {
	m.mu.Lock()
	m.zeroconfLabel = label
	m.mu.Unlock()
}

func (m *Manager) zeroconfHost() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.zeroconfLabel
}
