// Package webui provides the config web interface on port 8888.
// Contains the HTML UI plus a REST API that is later also used by the
// Wails desktop app.
package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JRpersonal/streborn/internal/autopair"
	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/netutil"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
	"github.com/JRpersonal/streborn/internal/streamproxy"
	"github.com/JRpersonal/streborn/internal/upnp"
	"github.com/JRpersonal/streborn/internal/webhooks"
	"github.com/JRpersonal/streborn/internal/zones"
)

// Server kapselt den Webui HTTP Server.
type Server struct {
	addr    string
	boxHost string
	logger  *slog.Logger
	presets *presets.Store
	// snapshotPath is the NAND file where the agent persisted the box's
	// pre-takeover presets + sources (internal/boxsnapshot). Served verbatim
	// by GET /api/box/snapshot so the app can warn about account-linked cloud
	// sources (Deezer, ...) STR cannot carry over. Empty = feature off.
	snapshotPath string
	// reflectPath is the reflect-sources file the experimental restore endpoint
	// appends to so the marge stub keeps advertising restored cloud sources.
	reflectPath string
	// zones persists this box's multiroom membership so a zone auto-reforms
	// after reboot/standby (#70). nil when not wired; zone write endpoints
	// then still drive the box but do not persist.
	zones *zones.Store
	// mediaServers remembers which DLNA/UPnP media servers the user turned into
	// native music sources, because the speaker forgets them on reboot. nil when
	// not wired; the endpoints then still drive the box but nothing is restored
	// after a restart.
	mediaServers *mediaservers.Store
	// publishStoredMusic hands the enabled media servers to the marge account
	// responses so the box picks them up on its own poll. nil when not wired.
	publishStoredMusic func([]StoredMusicSource)
	renderer           *upnp.Renderer
	// sleep is the armed sleep timer, if any. See sleeptimer.go.
	sleep       sleepState
	autoPair    *autopair.Manager
	regionMu    sync.RWMutex
	region      string // ISO 3166-1 alpha-2 from the setup wizard, empty if unknown
	regionFile  string // path for persistent storage
	streamProxy *streamproxy.Server
	// webhooks holds the user-configured HTTP requests (thumbs trigger). nil
	// when not wired; endpoints then report unavailable.
	webhooks *webhooks.Store
	// spotifySwitchedAway tells the Spotify manager the box was pointed at a
	// non-Spotify source, so its #14 auto-attach does not yank the box back.
	// nil when Spotify is not configured.
	spotifySwitchedAway func(ctx context.Context)
	// spotifyStream serves the live Ogg from the go-librespot manager to
	// the box over HTTP (registered at /spotify/stream). nil when Spotify
	// is not configured. Injected as a handler so webui need not import
	// the spotify package.
	spotifyStream http.HandlerFunc
	// spotifyPlay tells go-librespot to play a Spotify URI on the given
	// account (the control side of a Spotify preset recall). The account is
	// the username the preset was saved under; an empty account plays with
	// go-librespot's current login (see manager PlayAccount / SwitchAccount).
	// nil when Spotify is not configured. Injected as a func for decoupling.
	// shuffle selects a fresh random start vs the default resume-where-left-off.
	spotifyPlay func(ctx context.Context, uri, account string, shuffle bool) error
	// peerSeedFn accepts speakers pushed from the desktop app (see WithPeerSeed).
	peerSeedFn func([]PeerSeed)
	// peerForgetFn removes one sticky-picker entry (see WithPeerForget).
	peerForgetFn func(host string) bool
	// peersFn lists the other STR speakers on the LAN for the on-box page's
	// "Other speakers" section. nil hides the section.
	peersFn func(ctx context.Context) []PeerLink
	// spotifyUser returns go-librespot's currently logged-in account, used to
	// stamp the account onto a newly saved Spotify preset. nil when Spotify
	// is not configured.
	spotifyUser func(ctx context.Context) string
	// spotifyContext returns the Spotify context URI go-librespot is currently
	// playing, used by the preset-save path to stamp the LIVE account when the
	// saved preset is the content that is playing right now (so a preset saved
	// from another household member's session gets that member's account, not a
	// stale one). nil when Spotify is not configured.
	spotifyContext func() string
	// spotifyMeta resolves a stable cover image URL and the human title for a
	// Spotify context URI (the playlist image + name), stamped onto a newly
	// saved Spotify preset so its tile has a steady logo and a real name (not a
	// bare "Spotify"). nil when Spotify is not configured.
	spotifyMeta func(ctx context.Context, uri string) (cover, title string)
	// spotifyStreaming reports whether the box is currently pulling the Ogg
	// stream, the definitive "Spotify is playing" signal for verifyRecall.
	// nil when Spotify is not configured.
	spotifyStreaming func() bool
	// spotifySkip advances go-librespot to the next/previous track for the phone
	// remote's Previous/Next controls (forward = next), the same skip the hardware
	// remote keys perform during Spotify playback (the box cannot skip a UPnP
	// source itself). nil when Spotify is not configured; the transport handler
	// then falls back to skipping the STR play queue.
	spotifySkip func(ctx context.Context, forward bool) error
	// spotifySkipCh/spotifySkipOnce back the async skip queue: presses are
	// acknowledged immediately and drained back-to-back by one worker, so a
	// slow go-librespot ack can never stack button presses into a minute-long
	// serial wait (live Portable, 2026-07-31). See enqueueSpotifySkip.
	spotifySkipCh   chan bool
	spotifySkipOnce sync.Once
	// spotifyReady reports whether go-librespot has finished authenticating, so
	// a soft Spotify recall can wait out a cold start instead of pointing the box
	// at a not-yet-flowing stream (which starves and detaches). nil when Spotify
	// is not configured.
	spotifyReady func() bool
	// spotifyCanRecall reports whether a Spotify recall can proceed: go-librespot
	// holds a live session right now OR a reusable credential is persisted (so it
	// can re-auth from it). Gating on a persisted credential ALONE wrongly refused
	// recall on a box with a live-but-never-persisted zeroconf session that played
	// Spotify fine (Patrick, ST10, 2026-06-24). Only when this is false does the
	// handler return the "log this speaker into Spotify first" hint instead of
	// optimistically reporting "playing" and failing silently (#45 Pierre). nil
	// until wired.
	spotifyCanRecall func(ctx context.Context) bool
	// spotifyPremiumRequired reports whether the logged-in Spotify account is
	// free/open and so cannot do the autonomous recall playback (#45). nil until
	// wired; the recall handler uses it to return a clear "needs Premium" error.
	spotifyPremiumRequired func() bool
	// spotifyExportCred / spotifyImportCred move the go-librespot login between
	// speakers so a user logs into Spotify ONCE and STR copies the credential to
	// the other boxes (#45 root cause: account=""). nil until wired.
	spotifyExportCred func() ([]byte, error)
	spotifyImportCred func(ctx context.Context, data []byte) error
	// margeGroupGet/Set/Clear bridge the marge stereo-pair record so the
	// pairing flow can install the SAME canonical pair document on both
	// members' marges (each box's firmware otherwise re-creates the record
	// from its own point of view and the RIGHT box stores itself as master).
	// The desktop app relays the document to the partner because agent-to-
	// agent HTTP is blocked between series-I boxes. nil until wired.
	margeGroupGet   func() (xmlDoc string, canonical bool, ok bool)
	margeGroupSet   func(xmlDoc string) error
	margeGroupClear func(reason string)
	// margeForward registers (or clears) a developer machine the box's cloud
	// conversation is relayed to. nil disables the endpoint. See margelab.go.
	margeForward func(target string) error
	// spotifySetRecalling marks an in-flight recall so ServeOgg drives the new
	// track from its start instead of resuming mid-position. nil when Spotify is
	// not configured.
	spotifySetRecalling func()
	// spotifySuppressActivate holds go-librespot's auto-repoint (maybeActivate/
	// repointBox) off for the given window. The hardware-skip recovery calls it so
	// the competing #14 auto-attach cannot race the clean slot recall while the box
	// is tearing its UPnP source down. nil when Spotify is not configured.
	spotifySuppressActivate func(time.Duration)
	// spotifyInfo answers GET /spotify/info with the live Spotify state
	// (ready, measured bitrate, device name) the UI reads to show the real
	// stream bitrate on a Spotify preset tile. nil when not configured.
	spotifyInfo http.HandlerFunc
	// spotifyReload restarts the supervised go-librespot so it re-execs from its
	// (just-overwritten) binary path, activating a freshly OTA-delivered engine
	// WITHOUT a box reboot. Called from handleAgentSidecar after the sidecar
	// write; returns whether a running engine was restarted. nil when Spotify is
	// not configured. See Manager.ReloadBinary (#240).
	spotifyReload func() bool
	// spotifyStop stops the supervised go-librespot and waits for it to exit, so
	// the space-pressed OTA write can actually free the engine's NAND blocks before
	// dropping it (a running binary's blocks stay pinned through an unlink). nil
	// when Spotify is not configured. See Manager.StopEngine (#119).
	spotifyStop func() bool
	// wifiSignalFn returns the latest Wi-Fi signal class observed on the
	// gabbo WebSocket (set from cmd/agent's boxws client). Used to fill
	// the signal for BCO boxes, whose /networkInfo reports none.
	wifiSignalFn func() string

	// boxNameFn returns the box display name and model the agent currently
	// knows (from its mDNS announcer cache). Exposed through the version
	// endpoint so the desktop app can read a flashed speaker's name straight
	// from the running agent, instead of falling back to "str-<ip>" whenever
	// the cross-LAN /info probe is slow right after an OTA restart (#108).
	// nil until wired by cmd/agent.
	boxNameFn func() (name, model string)

	// nativePresetLocator decides whether a preset can be stored as a native
	// LOCAL_INTERNET_RADIO station (returning its orion location) instead of a
	// UPnP stream that the box refuses to activate on its own. nil until wired
	// by cmd/agent, and nil on the desktop side, where "" keeps the UPnP form.
	nativePresetLocator func(name, streamURL, art string) string

	// now_playing micro-cache. The Bose firmware app (:8090) on BCO
	// speakers cannot sustain a high request rate, so /api/status caches
	// the last good now_playing body for statusCacheTTL and serves repeat
	// polls from it. This caps how often the box itself is hit no matter
	// how fast or how many clients poll: defense in depth behind the
	// desktop app's adaptive poll cadence.
	statusMu   sync.Mutex
	statusBody []byte
	statusCode int
	statusAt   time.Time
	// statusStaleWarned dedupes the "serving a stale status" WARN so a box
	// that stays unreachable logs once per outage, not once per client poll.
	// Guarded by statusMu; reset whenever a fresh body is cached.
	statusStaleWarned bool

	// boxCmdMu serializes state-changing commands sent to the speaker
	// (play, volume, pause, stop, source, bass). The Bose firmware's tiny
	// HTTP/UPnP server mishandles concurrent commands: a volume PUT landing
	// during the wake+play of a station made the play itself fail (reported
	// live: rapid volume slides right before a preset press killed the
	// start). Serializing the writes makes a volume wait for the play to
	// finish instead of colliding with it. Reads (now_playing/info) are not
	// gated; they have their own micro-cache.
	boxCmdMu sync.Mutex

	// wlanMu serializes the background Wi-Fi change so two PUT /api/box/wlan
	// requests cannot run applyWLANChange concurrently and interleave their
	// writes to wlan-creds / wpa_supplicant.conf.
	wlanMu sync.Mutex

	// lastPlay remembers the stream STR last told the box to play, so the
	// auto-re-push (#4) can resume it when the Bose renderer drops a long
	// stream on its own (reported: radio stops after ~11 min, no STR error).
	// boxSourceFn seams the now_playing source read (boxSourceNow) so the
	// standby-vs-awake decision is assertable without a live box.
	boxSourceFn func() string
	// deferred holds a resume waiting for the user to switch the box on; STR
	// never powers a speaker on by itself (#487). Guarded by deferredMu.
	deferredMu sync.Mutex
	deferred   *deferredResume

	lastPlayMu sync.Mutex
	lastPlay   *lastPlayInfo
	// rePushInFlight coalesces stream-drop resumes (see HandleStreamDisconnect).
	// A SERVER-level latch on purpose, guarded by lastPlayMu: it used to live on
	// lastPlayInfo, but setLastPlay replaces that struct on every fresh play, so
	// a drop of the NEW stream while an older maybeRePush was still waiting out
	// the recall-ownership window spawned a second concurrent re-pusher, and the
	// first one's deferred release then cleared the latch the second one owned -
	// the box got double SetURI+Play (two audible re-buffers) and a third
	// goroutine could stack. One latch per Server survives the struct swap.
	rePushInFlight bool
	// recallGen counts every stream push recorded via setLastPlay. Each
	// recall's verify loop captures the generation of its own play; any later
	// play bumps it, telling the older verify it was superseded so it stands
	// down instead of re-pushing its now-unwanted stream over the user's newer
	// choice (two rapid preset presses used to ping-pong stations for ~15s).
	// Guarded by lastPlayMu.
	recallGen uint64
	// resumeAttempts counts consecutive AUTOMATIC resume attempts (power-on
	// resume / reconnect recovery) that never reached stable playback, and
	// lastResumeAt is when the newest one was pushed. They drive the
	// auto-resume crash-loop guard (#381, see resume_guard.go): a stream that
	// crashes the box on playback otherwise loops boot -> auto-resume ->
	// crash -> watchdog reboot forever. Guarded by lastPlayMu; persisted
	// inside last-play.json so the count survives the very reboot it counts.
	resumeAttempts int
	lastResumeAt   time.Time
	// lastPlayPath is the NAND file the last-played stream is persisted to, so
	// the power-on resume survives an agent restart across a long/overnight
	// standby (in-RAM only lost the station and the box fell back to its native
	// "Preset not assigned", #119 Klaus). Empty disables persistence (tests).
	lastPlayPath string

	// mirrorSkips remembers, per zone member, why the last mirror reconcile
	// tick skipped it, so the skip is logged at INFO only on a state change
	// (#342). Touched only by the single reconcile goroutine — no lock.
	mirrorSkips map[string]string

	// mirrorKick asks the reconcile goroutine for an out-of-turn round right
	// after a fresh play, so a group re-forms in seconds instead of on the next
	// 5-minute tick. Capacity 1 and non-blocking sends: several plays in quick
	// succession collapse into one round. Going through the SAME goroutine is
	// deliberate — it keeps mirrorSkips lock-free and makes two mirror pushes
	// at once impossible.
	mirrorKick chan struct{}
	// mirrorKickPending is true between scheduling a kick and sending it, so a
	// burst of plays produces one reconcile rather than one per play.
	mirrorKickPending atomic.Bool

	// wedge tracks the "box accepts transport pushes but never plays" state
	// that only a power-cycle clears; streamActivityFn (the stream proxy's
	// LastActivity) tells it apart from a failing station. See wedge.go.
	wedge            wedgeState
	streamActivityFn func() (lastFetch, lastFailure time.Time)

	// refusal tracks the silent variant of the not-logged-in refusal family:
	// recalls that exhaust while the box drops its source to STANDBY on its
	// own, without ever sending a 1036. See wedge.go / RecallRefusal.
	refusal refusalState

	// loginErr tracks the last time the box rejected a source as not-logged-in
	// (errorUpdate 1036), so verifyRecall stands its retry down while a forced
	// re-login runs instead of thrashing the box. See wedge.go / NoteBoxLoginError.
	loginErr loginErrState

	// lastUserStop is when the user last DELIBERATELY stopped playback, so the
	// auto-re-push does not fight a wanted stop (v0.7.0: a single Stop
	// did not hold because the proxy disconnect that a stop causes looks
	// identical to a box-side drop). Set from the STR Stop/Pause endpoints
	// (definite intent) and from a gabbo STOP_STATE frame (the physical
	// remote / box button). maybeRePush suppresses a resume within
	// userStopWindow of this.
	lastUserStopMu sync.Mutex
	lastUserStop   time.Time

	// lastExplicitStop is the narrower signal: a stop or pause that came in as a
	// REQUEST (the app, the phone remote, a webhook), never the box's own
	// STOP_STATE. The two must be told apart, because a speaker that collapses
	// while resuming emits a STOP_STATE on the way down, and lastUserStop
	// therefore says "the user stopped this" exactly when the user did not. Any
	// recovery that reads lastUserStop as intent stands down at the moment it is
	// needed, which is how the paused library track stayed silent (2026-08-11).
	lastExplicitStopMu sync.Mutex
	lastExplicitStop   time.Time

	// pausePos is how far into the track the speaker was when the user paused,
	// read before the pause is issued. It is what lets a resume that has to
	// re-push the file continue where the listener stopped instead of starting
	// the track over. Zero for radio and for anything the box reports no
	// position for.
	pausePosMu sync.Mutex
	pausePos   time.Duration

	// lastStandbyStop debounces the #197 standby-bounce mitigation: some ST20
	// (scm) firmware oscillates UPNP->STANDBY->UPNP on a power-off, re-selecting
	// STR's UPnP source so the box turns itself back on. HandleEnterStandby clears
	// the transport once per standbyStopDebounce so the rapid flip does not issue a
	// burst of Stops.
	standbyStopMu   sync.Mutex
	lastStandbyStop time.Time
	// lastStandbyClear rate-limits the transport clear during a power-off bounce
	// (separate from lastStandbyStop, which gates the resume-suppression window):
	// the clear re-fires on each flip of the UPNP<->STANDBY oscillation so a clear
	// that lost the ~170 ms race is retried, bounded by standbyClearMinGap.
	lastStandbyClear time.Time
	// lastUserPlayStart is when a user last explicitly asked for playback (a
	// hardware preset press or an app play). HandleEnterStandby reads it to tell
	// a UPNP->STANDBY flip that interrupts the user's own fresh recall (firmware
	// settling, #419) from a real power-off, and NoteUserPlay stamps it while
	// clearing the stop latches (the press is newer intent than any prior stop).
	// Guarded by standbyStopMu.
	lastUserPlayStart time.Time
	// lastSpontResume single-flights the spontaneous-power-off recovery so a
	// flapping source cannot stack resume goroutines. Guarded by standbyStopMu.
	lastSpontResume time.Time
	// userActivityFn reports when the box last emitted a userActivityUpdate
	// frame (a physical key press, box or IR remote); zero = none seen. Wired to
	// boxws.LastUserActivity. HandleEnterStandby uses it to recognise the
	// firmware spontaneously powering off STR's UPnP source with no user input
	// (#419). nil or all-zero (a firmware that never sends the frame) falls back
	// to the conservative power-off handling.
	userActivityFn func() time.Time

	// storm1036Fn reports whether the box is currently rejecting essentially
	// every recall (1036), how many rejections are in the window and since
	// when. Wired to boxws.Storm1036. Surfaced on the version envelope so the
	// desktop app and the phone remote can offer a SOFT reboot: the remedy
	// users reach for on their own, pulling the plug, resets the box clock and
	// poisons the next boot, while a soft reboot clears the state (#419
	// Finding 4). nil = not wired (no storm reported).
	storm1036Fn func() (bool, int, time.Time)

	// ownTransportCmdFn reports when STR itself last issued a transport-
	// mutating SOAP command; zero = never. Wired to
	// boxws.LastOwnTransportCommand. HandleEnterStandby uses it to excuse a
	// source flip that answers STR's own push. nil-safe.
	ownTransportCmdFn func() time.Time

	// playStateFn overrides boxPlayState for tests. nil = the real :8090 probe.
	playStateFn func() (standby, busy bool)

	// resumeOnPowerOnPath persists the per-box opt-out for "resume the last
	// station when the speaker is switched on" (default on; file absent or "1").
	// Empty falls back to defaultResumeOnPowerOnPath.
	resumeOnPowerOnPath string

	// displayTrackPath persists the per-box opt-IN for "show the live radio track
	// on the speaker's display" (default OFF; file absent or "0"). Pushing the ICY
	// title to the box re-issues SetAVTransportURI, which makes the box re-buffer
	// (a brief audio gap on each track change, verified on a Portable), so it is
	// off unless the user turns it on. Empty falls back to defaultDisplayTrackPath.
	displayTrackPath string

	// lastDisplayPush debounces the ICY display push so a station that flips its
	// StreamTitle between the song and promo/talk lines does not cause a re-buffer
	// gap every few seconds. Guarded by lastPlayMu.
	lastDisplayPush time.Time
	// lastICYTitle is the most recent radio StreamTitle seen, kept so enabling the
	// display push or changing its mode can show the CURRENT track immediately
	// instead of waiting for the next title change. Guarded by lastPlayMu.
	lastICYTitle string

	// announceAudio holds the most recently fetched announcement audio (#125), the
	// cloud-free replacement for the firmware's /speaker TTS endpoint. It is served
	// once to the box at /announce/audio WITH a Content-Length so the player stops
	// at the end, instead of the radio stream proxy's reconnect-on-EOF behaviour
	// (which looped a finite TTS clip ~60x, verified on a Portable). Guarded by
	// announceMu.
	announceMu    sync.Mutex
	announceAudio []byte
	announceMime  string

	// recent is the capped, debounced recently-played ring (#135). nil when not
	// wired (dev builds / Spotify-less boxes still work): the /api/recent
	// endpoint then serves an empty list and the play handlers skip recording.
	// recentRadioCard / recentSpotifyCard remember the active source card so the
	// live track callbacks (HandleStreamTitle for radio ICY, NoteRecentSpotifyTrack
	// for Spotify) can attribute tracks to them without re-deriving the card.
	recent            *recent.Store
	recentMu          sync.Mutex
	recentRadioCard   recentCardCtx
	recentSpotifyCard recentCardCtx
	// recentQueueCard remembers the DLNA folder currently playing as an
	// auto-advancing queue, so each track the queue pushes is recorded under one
	// "library" card (#220: folder plays were never added to Recently played).
	// Cleared when the queue stops (a single play, a stop, or running out).
	recentQueueCard recentCardCtx

	// boxPresets is the box's OWN preset list as last reported over the gabbo
	// presetsUpdated frame, including foreign sources (DEEZER etc.) STR did not
	// set. Lets the app show/preserve/recall them (Option C). Guarded by boxPresetsMu.
	boxPresetsMu sync.Mutex
	boxPresets   []BoxPreset
	// deletedBoxSlots tombstones slots the user just deleted, so a presetsUpdated
	// burst the box emits right after the removal does not resurrect the slot in
	// the app's merged view before the box-side RemovePreset has settled (a user
	// reported a deleted preset reappearing as a UPNP entry after an app restart).
	// Keyed slot -> deletion time; entries older than boxPresetTombstoneTTL are
	// ignored/pruned. Guarded by boxPresetsMu.
	deletedBoxSlots map[int]time.Time

	// queue is the agent-side DLNA library play queue (#202 follow-up). It
	// auto-advances on track end so a NAS/FRITZ!Box folder plays through like the
	// original SoundTouch box-side queue, even with the desktop app closed. A
	// watcher goroutine polls now_playing while a queue is active; queueGen
	// invalidates the per-track timing when a new track is pushed. queueMu guards
	// the watcher lifecycle and timing fields; the playQueue has its own lock.
	queue           *playQueue
	queueMu         sync.Mutex
	queueCancel     context.CancelFunc
	queueGen        int
	queueTrackStart time.Time
	queueTrackDur   time.Duration
	// baseCtx is the server-lifetime context (set in Run), the parent for the
	// long-lived queue watcher so it outlives the request that started the queue.
	baseCtx context.Context
}

// boxPresetTombstoneTTL is how long a just-deleted slot is filtered out of the
// box's reported preset list. It covers the box's post-change presetsUpdated
// burst and the RemovePreset round-trip; after it expires a slot the box still
// reports is shown again (it genuinely still exists on the box).
const boxPresetTombstoneTTL = 90 * time.Second

// BoxPreset is one of the box's own presets (incl. foreign sources like DEEZER),
// served by GET /api/box/presets so the app can show and preserve them. Mirrors
// boxws.BoxPreset; the agent maps the gabbo frame into this via NoteBoxPresets.
type BoxPreset struct {
	Slot          int    `json:"slot"`
	Source        string `json:"source"`
	Type          string `json:"type"`
	Location      string `json:"location"`
	SourceAccount string `json:"sourceAccount"`
	Name          string `json:"name"`
}

// recentCardCtx is the current source card for a source, retained so the live
// track callbacks (radio ICY title, Spotify track change) can hang their tracks
// under it (#135). homepage is the station website, carried so each ICY-title
// track entry keeps the "website" link target.
type recentCardCtx struct{ key, name, art, url, account, homepage string }

// lastPlayInfo is the box-facing URL + metadata of the current stream plus the
// re-push state. rePushes counts consecutive resume attempts on THIS stream and
// drives an exponential backoff; once it hits maxRePushes the stream is marked
// failed and never re-pushed again until a fresh play (setLastPlay) replaces it.
// The drop-coalescing latch lives on the Server (rePushInFlight), NOT here: a
// per-play latch died with every setLastPlay struct swap and let concurrent
// re-pushers stack (see the Server field).
type lastPlayInfo struct {
	boxURL, title, art, mime string
	ts                       time.Time
	rePushes                 int
	failed                   bool
}

// resumeMaxAge bounds how stale the last station may be and still come back on a
// power-on press. Generous (a week) because a user expects "abends aus, morgens
// an" to resume, like Bose did; the persisted lastPlay on NAND makes a long age
// reachable across the agent restart a long standby often causes (#119 Klaus).
const resumeMaxAge = 7 * 24 * time.Hour

// maxRePushes is the hard cap on consecutive resume attempts for one stream.
// After this many the stream is declared dead and left alone (no re-arm) until
// the user plays something new. The exponential backoff (capped at 30s) spaces
// the attempts out, so this many spans several minutes, not seconds: a
// SoundTouch 10 was seen to have its long radio stream dropped by the renderer
// after ~11 min and then recover slowly, so a cap of 5 (~30s of attempts) gave
// up far too early and the radio stayed silent. 10 keeps retrying for a few
// minutes while the backoff + the rePushInFlight latch still prevent the
// dozens-per-second runaway the cap originally fixed (v0.7.5).
const maxRePushes = 10

// statusCacheTTL bounds the staleness of a cached now_playing response and
// thus the maximum /now_playing hit rate against the Bose app to about
// 1/TTL per second regardless of client poll frequency.
const statusCacheTTL = 2 * time.Second

// statusStaleAfter is the cached now_playing age past which /api/status marks
// its fallback response as stale (X-STR-Status-Stale) and logs one WARN. The
// body itself keeps being served: clients regex-parse it as the box's XML, so
// blanking or replacing it would break them, but without the marker a box
// whose BoseApp died kept "Playing <station>" on every client forever.
const statusStaleAfter = 30 * time.Second

// playDetachTimeout bounds a play/recall push that has been detached from the
// caller's request context (#252): long enough for the standby wake (~6-8s)
// plus the UPnP SetURI+Play on a just-woken box, short enough that an
// abandoned request cannot hold boxCmdMu indefinitely.
const playDetachTimeout = 12 * time.Second

// ensureBoxReady wakes the box from standby (with retry+poll until
// really awake) and ensures the marge account is active.
// Called before every play call.
// handleBoxWake wakes the speaker from standby (the :17000 TAP wake) WITHOUT
// starting any playback. The desktop app calls it on a zone member that a user
// switched off at the speaker before enrolling it: the firmware otherwise adds a
// still-asleep box to the group and it stays silent while STR reports success
// (#70). Waking an already-awake box is a fast no-op.
func (s *Server) handleBoxWake(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.boxHost == "" {
		http.Error(w, "box host not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	if err := boxcli.WakeAndWait(ctx, s.boxHost, 8*time.Second, s.logger); err != nil {
		http.Error(w, "wake failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"awake": true})
}

func (s *Server) ensureBoxReady(ctx context.Context) {
	if s.boxHost != "" {
		// Detach from the caller's request context: a slow wake must not be
		// cancellable by the app giving up on the play POST, because the very
		// same r.Context() then drives the SetURI that follows. When the wake
		// (or the pair below) blocked past the app's request timeout, the app
		// cancelled the request and the recall's own PlayURL then ran on an
		// already-cancelled context and failed instantly with "context
		// cancelled" - every preset recall dead on ST20/ST30 (bernd, #252).
		wakeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
		if err := boxcli.WakeAndWait(wakeCtx, s.boxHost, 6*time.Second, s.logger); err != nil {
			s.logger.Warn("Box could not be woken from STANDBY", "err", err)
		}
		cancel()
	}
	if s.autoPair != nil {
		// Fire-and-forget. The marge-account refresh is NOT needed for the
		// UPnP playback that follows, yet the box's :8090 setMargeAccount POST
		// can hang for seconds on some firmwares. Running it inline blocked the
		// recall past the app's request timeout (see above), so run it in the
		// background on its own context and never delay the play on it.
		go func() {
			pairCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			s.autoPair.TriggerNow(pairCtx)
		}()
	}
}

// New creates a new webui server.
func New(addr string, logger *slog.Logger, opts ...Option) *Server {
	s := &Server{addr: addr, logger: logger, queue: newPlayQueue(),
		mirrorKick: make(chan struct{}, 1)}
	for _, o := range opts {
		o(s)
	}
	s.loadLastPlay()
	return s
}

// Run starts the server and blocks until ctx is cancelled.
//
// Every step that can fail or block emits a phase-marker log at WARN
// level so that even on a `--log-level warn` deployment (or with the
// diagnostic capturing only the tail of /tmp/streborn-agent.log) the
// bundle shows which step the agent reached. Without these markers an
// agent that bound :8090 but silently failed :8888 looked identical
// in the bundle to one that crashed mid-init.
func (s *Server) Run(ctx context.Context) error {
	s.logger.Warn("webui phase: Run entered", "addr", s.addr)
	s.baseCtx = ctx
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/manifest.webmanifest", s.handleManifest)
	mux.HandleFunc("/icon.png", s.handleIcon)
	mux.HandleFunc("/icon-large.png", s.handleIconLarge)
	mux.HandleFunc("/api/peers", s.handlePeers)
	mux.HandleFunc("/api/peers/seed", s.handlePeerSeed)
	mux.HandleFunc("/api/peers/forget", s.handlePeerForget)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// REST API
	mux.HandleFunc("/api/presets", s.handlePresets)
	mux.HandleFunc("/api/presets/", s.handlePresetSlot)
	mux.HandleFunc("/api/play", s.handlePlay)
	mux.HandleFunc("/api/play/", s.handlePlaySlot)
	mux.HandleFunc("/api/pause", s.handlePause)
	mux.HandleFunc("/api/resume", s.handleResume)
	mux.HandleFunc("/api/stop", s.handleStop)
	// Source-aware skip for the phone remote's Previous/Next controls: skips
	// Spotify when it is the live source, otherwise advances the STR play queue.
	mux.HandleFunc("/api/next", s.handleTransportNext)
	mux.HandleFunc("/api/prev", s.handleTransportPrev)
	mux.HandleFunc("/api/queue", s.handleQueue)
	mux.HandleFunc("/api/queue/next", s.handleQueueNext)
	mux.HandleFunc("/api/queue/prev", s.handleQueuePrev)
	mux.HandleFunc("/api/queue/shuffle", s.handleQueueShuffle)
	mux.HandleFunc("/api/queue/repeat", s.handleQueueRepeat)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/position", s.handlePosition)
	mux.HandleFunc("/api/recent", s.handleRecent)
	// Radio search/browse moved app-side (the app queries radio-browser
	// directly; see the app-first direction). The box no longer serves
	// /api/radio/* and no longer compiles in the radiobrowser package.
	mux.HandleFunc("/api/agent/version", s.handleAgentVersion)
	mux.HandleFunc("/api/agent/update", s.handleAgentUpdate)
	mux.HandleFunc("/api/agent/sidecar", s.handleAgentSidecar)
	mux.HandleFunc("/api/agent/enable-ssh", s.handleAgentEnableSSH)
	mux.HandleFunc("/api/box/settings", s.handleBoxSettings)
	mux.HandleFunc("/api/box/name", s.handleBoxName)
	mux.HandleFunc("/api/box/volume", s.handleBoxVolume)
	mux.HandleFunc("/api/box/bass", s.handleBoxBass)
	mux.HandleFunc("/api/box/source", s.handleBoxSource)
	mux.HandleFunc("/api/box/power", s.handleBoxPower)
	mux.HandleFunc("/api/region", s.handleRegion)
	mux.HandleFunc("/api/box/wlan", s.handleBoxWLAN)
	mux.HandleFunc("/api/box/reboot", s.handleBoxReboot)
	mux.HandleFunc("/api/box/remove-conflicting-mod", s.handleRemoveConflictingMod)
	mux.HandleFunc("/api/box/wake", s.handleBoxWake)
	mux.HandleFunc("/api/box/airplay-opt", s.handleBoxAirplayOpt)
	mux.HandleFunc("/api/box/resume-on-power-on", s.handleResumeOnPowerOn)
	mux.HandleFunc("/api/box/display-track", s.handleDisplayTrack)
	mux.HandleFunc("/api/box/mediaservers", s.handleMediaServers)
	mux.HandleFunc("/api/box/presets", s.handleBoxPresets)
	mux.HandleFunc("/api/box/presets/recall", s.handleBoxPresetRecall)
	mux.HandleFunc("/api/box/snapshot", s.handleBoxSnapshot)
	mux.HandleFunc("/api/box/snapshot/restore", s.handleBoxSnapshotRestore)
	mux.HandleFunc("/api/announce", s.handleAnnounce)
	mux.HandleFunc("/announce/audio", s.handleAnnounceAudio)
	mux.HandleFunc("/api/box/sync-presets", s.handleBoxSyncPresets)
	mux.HandleFunc("/api/box/zone", s.handleBoxZone)
	mux.HandleFunc("/api/box/balance", s.handleBoxBalance)
	mux.HandleFunc("/api/box/zone/volume", s.handleZoneVolume)
	mux.HandleFunc("/api/box/sleep", s.handleSleep)
	mux.HandleFunc("/api/box/zone/purge", s.handleZonePurge)
	mux.HandleFunc("/api/box/group", s.handleBoxGroup)
	mux.HandleFunc("/api/marge/group", s.handleMargeGroupDoc)
	mux.HandleFunc("/api/webhooks", s.handleWebhooks)
	mux.HandleFunc("/api/webhooks/test", s.handleWebhooksTest)
	mux.HandleFunc("/api/stick/status", s.handleStickStatus)
	mux.HandleFunc("/api/debug/state", s.handleDebugState)

	// LOCAL_INTERNET_RADIO: the adapter endpoints the BMX service registry
	// points at, so the box can resolve and play a native radio ContentItem
	// (see lir.go).
	mux.HandleFunc("/lir/", s.handleLIRStation)
	mux.HandleFunc("/core02/svc-bmx-adapter-orion/prod/orion/station", s.handleOrionStation)
	mux.HandleFunc("/core02/svc-bmx-adapter-orion/prod/orion/token", s.handleOrionToken)
	mux.HandleFunc("/api/debug/marge-lab", s.handleMargeLab)
	mux.HandleFunc("/api/debug/native-preset-probe", s.handleNativeProbe)
	// Radio service icons the BMX registry points the speaker at. Must be a
	// real route: without it these fall through to the catchall and the speaker
	// receives an HTML page where it asked for an image.
	mux.HandleFunc(bmxIconPrefix, s.handleBMXIcon)
	// Station artwork over plain HTTP: the speaker cannot fetch https itself.
	mux.HandleFunc(artProxyPath, s.handleArt)
	mux.HandleFunc("/api/debug/probe", s.handleDebugProbe)

	// Stream proxy: stable URLs for radio streams with token expiry.
	// See internal/streamproxy for details.
	if s.streamProxy != nil {
		s.streamProxy.Register(mux)
	}
	if s.spotifyStream != nil {
		// .ogg suffix matters: the Bose UPnP renderer keys playability off
		// the URL extension and rejects an extensionless Ogg stream
		// (INVALID_SOURCE) even with audio/ogg Content-Type + protocolInfo.
		mux.HandleFunc("/spotify/stream.ogg", s.spotifyStream)
		// Per-slot aliases (same single stream, distinct URLs) so the box can
		// store a UNIQUE location per Spotify preset. Without this, two Spotify
		// presets share the identical location and the box drops one of them
		// (observed: slot 1 vanished when 1 and 6 both used /spotify/stream.ogg).
		for slot := 1; slot <= 6; slot++ {
			mux.HandleFunc(fmt.Sprintf("/spotify/stream-%d.ogg", slot), s.spotifyStream)
		}
	}
	if s.spotifyInfo != nil {
		mux.HandleFunc("/spotify/info", s.spotifyInfo)
	}
	if s.spotifyExportCred != nil || s.spotifyImportCred != nil {
		mux.HandleFunc("/spotify/credential", s.handleSpotifyCredential)
		// Alias: the desktop app's preset-copy flow called this spelling for
		// weeks while only /spotify/credential was served; the catch-all index
		// answered it with 200 + HTML, which read as success and silently
		// transferred nothing (the "Spotify preset needs the app once" field
		// reports). Serve both so no client generation can fall through to the
		// index again.
		mux.HandleFunc("/api/spotify/credential", s.handleSpotifyCredential)
	}

	srv := &http.Server{Addr: s.addr, Handler: corsMiddleware(mux)}
	s.logger.Warn("webui phase: mux ready, calling ListenTCP", "addr", s.addr)
	// SO_REUSEADDR so the agent can rebind after a watchdog respawn
	// while the previous listener is still in TIME_WAIT.
	ln, err := netutil.ListenTCP(ctx, s.addr)
	if err != nil {
		s.logger.Error("webui phase: ListenTCP failed", "addr", s.addr, "err", err)
		return fmt.Errorf("webui listen %s: %w", s.addr, err)
	}
	s.logger.Warn("webui phase: ListenTCP succeeded, starting Serve", "addr", s.addr, "local", ln.Addr().String())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return fmt.Errorf("webui server: %w", err)
	}
}

// corsMiddleware allows cross-origin calls from the desktop app.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireMethod responds 405 and returns false unless the request method is one
// of the allowed ones, so a handler can guard the method in a single line.
func requireMethod(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	for _, m := range allowed {
		if r.Method == m {
			return true
		}
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

// decodeJSONRequest decodes a JSON request body (bounded by maxBytes) into v,
// responding 400 with the parse error on failure. Returns false when the caller
// should stop handling the request.
func decodeJSONRequest[T any](w http.ResponseWriter, r *http.Request, maxBytes int64, v *T) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes)).Decode(v); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
