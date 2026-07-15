// Package webui provides the config web interface on port 8888.
// Contains the HTML UI plus a REST API that is later also used by the
// Wails desktop app.
package webui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/JRpersonal/streborn/internal/autopair"
	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/boxsnapshot"
	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/netutil"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
	"github.com/JRpersonal/streborn/internal/streamproxy"
	"github.com/JRpersonal/streborn/internal/upnp"
	"github.com/JRpersonal/streborn/internal/webhooks"
	"github.com/JRpersonal/streborn/internal/wlanlive"
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
	zones       *zones.Store
	renderer    *upnp.Renderer
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
	lastPlayMu sync.Mutex
	lastPlay   *lastPlayInfo
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

	// wedge tracks the "box accepts transport pushes but never plays" state
	// that only a power-cycle clears; streamActivityFn (the stream proxy's
	// LastActivity) tells it apart from a failing station. See wedge.go.
	wedge            wedgeState
	streamActivityFn func() (lastFetch, lastFailure time.Time)

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
// rePushInFlight coalesces drops: a dead stream fires a disconnect on every
// failed resume, and without this each one spawned a new goroutine (reported
// v0.7.5: a dead slot-3 URL produced dozens of resume attempts per second that
// starved the control port :8888).
type lastPlayInfo struct {
	boxURL, title, art, mime string
	ts                       time.Time
	rePushes                 int
	failed                   bool
	rePushInFlight           bool
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

// SetWifiSignalFn wires a provider for the latest Wi-Fi signal class
// (from the boxws gabbo stream). cmd/agent calls this after creating the
// WebSocket client.
func (s *Server) SetWifiSignalFn(fn func() string) { s.wifiSignalFn = fn }

// SetBoxNameFn wires a provider for the box display name and model the agent
// currently knows (typically the mDNS announcer snapshot). cmd/agent calls
// this after the announcer is up.
func (s *Server) SetBoxNameFn(fn func() (name, model string)) { s.boxNameFn = fn }

// Option is a functional option for New.
type Option func(*Server)

// WithPresets wires the store for preset CRUD.
func WithPresets(p *presets.Store) Option {
	return func(s *Server) { s.presets = p }
}

// WithZones wires the multiroom zone persistence store so formed zones survive
// a reboot/standby and auto-reform (#70).
func WithZones(z *zones.Store) Option {
	return func(s *Server) { s.zones = z }
}

// WithBoxHost sets the Bose box IP/hostname for UPnP calls.
func WithBoxHost(host string) Option {
	return func(s *Server) {
		s.boxHost = host
		s.renderer = upnp.NewBoseRenderer(host)
	}
}

// WithLastPlayPath wires the NAND path the last-played stream is persisted to,
// so the power-on resume survives an agent restart over a long standby (#119).
func WithLastPlayPath(path string) Option {
	return func(s *Server) { s.lastPlayPath = path }
}

// WithBoxSnapshotPath wires the NAND path of the pre-takeover box snapshot
// (internal/boxsnapshot) so GET /api/box/snapshot can serve it.
func WithBoxSnapshotPath(path string) Option {
	return func(s *Server) { s.snapshotPath = path }
}

// WithReflectSourcesPath wires the reflect-sources file so the experimental
// restore endpoint can re-advertise account-linked cloud sources (Deezer).
func WithReflectSourcesPath(path string) Option {
	return func(s *Server) { s.reflectPath = path }
}

// WithAutoPair gives the server access to the AutoPair manager so that
// play calls can re-pair the box again after waking it from standby.
func WithAutoPair(m *autopair.Manager) Option {
	return func(s *Server) { s.autoPair = m }
}

// WithRegion passes the country code chosen by the setup wizard.
// Exposed via /api/region so the desktop app can derive its defaults
// for radio search and language from it.
func WithRegion(cc string) Option {
	return func(s *Server) { s.region = strings.ToUpper(cc) }
}

// WithRegionFile sets the persistent path for changes from
// /api/region (PUT). Without this path changes are only in memory.
func WithRegionFile(path string) Option {
	return func(s *Server) { s.regionFile = path }
}

// WithResumeOnPowerOnFile sets the persistent path for the per-box "resume the
// last station on power-on" opt-out (default on). Without it the default NAND
// path (defaultResumeOnPowerOnPath) is used.
func WithResumeOnPowerOnFile(path string) Option {
	return func(s *Server) { s.resumeOnPowerOnPath = path }
}

// WithDisplayTrackFile sets the persistent path for the per-box "show the live
// radio track on the speaker display" opt-in. Empty uses the default path
// (defaultDisplayTrackPath).
func WithDisplayTrackFile(path string) Option {
	return func(s *Server) { s.displayTrackPath = path }
}

// WithStreamProxy wires in the stream proxy. When set, the /stream/
// endpoint is registered. Bose ContentItems are then linked with
// http://127.0.0.1:8888/stream/<slot> instead of the real CDN URL —
// streams survive token expiry.
func WithStreamProxy(p *streamproxy.Server) Option {
	return func(s *Server) { s.streamProxy = p }
}

// WithWebhooks wires the user-configured webhook store (thumbs trigger).
func WithWebhooks(w *webhooks.Store) Option {
	return func(s *Server) { s.webhooks = w }
}

// WithSpotifySwitchedAway wires the Spotify manager's source-switch hook, called
// when the box is pointed at a non-Spotify source so the #14 auto-attach stands
// down (otherwise a radio recall jumps back to Spotify a second later).
func WithSpotifySwitchedAway(f func(ctx context.Context)) Option {
	return func(s *Server) { s.spotifySwitchedAway = f }
}

// WithSpotifyStream registers the handler that serves go-librespot's
// live Ogg to the box at /spotify/stream (the Spotify-preset audio
// plane).
func WithSpotifyStream(h http.HandlerFunc) Option {
	return func(s *Server) { s.spotifyStream = h }
}

// WithSpotifyInfo registers the handler that reports live Spotify state
// (ready, measured bitrate, device name) at /spotify/info.
func WithSpotifyInfo(h http.HandlerFunc) Option {
	return func(s *Server) { s.spotifyInfo = h }
}

// WithSpotifyReload injects the Spotify manager's live engine reload, called
// after an OTA sidecar write (handleAgentSidecar) so a freshly delivered
// go-librespot is hot-swapped in place without a box reboot. The function
// returns whether a running engine was restarted. Wiring it also makes the
// version endpoint advertise engineHotSwap=true so the desktop app skips its
// post-delivery activation reboot (#240).
func WithSpotifyReload(f func() bool) Option {
	return func(s *Server) { s.spotifyReload = f }
}

// WithSpotifyStop injects the Spotify manager's engine-stop, used by the
// space-pressed OTA write to genuinely free the regenerable go-librespot engine
// (stop the process so its NAND blocks release, then drop the binary) when a tight
// box cannot otherwise hold the agent update (#119). nil leaves the previous
// best-effort os.Remove (a no-op while the engine runs).
func WithSpotifyStop(f func() bool) Option {
	return func(s *Server) { s.spotifyStop = f; engineStopHook = f }
}

// PeerLink is one other STR speaker on the LAN, as shown in the on-box page's
// "Other speakers" section so a phone can hop between speakers.
type PeerLink struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// Reachable is false for a peer that was seen recently over mDNS but did not
	// answer a web-port probe on the last sweep. Such peers are still listed (so a
	// speaker briefly missed by a lossy mDNS round does not vanish and reappear,
	// #404/#381/#385) but the on-box page renders them dimmed / non-clickable.
	Reachable bool `json:"reachable"`
}

// WithPeers registers the resolver that lists the other STR speakers on the
// network (name + reachable web URL). nil disables the "Other speakers" section.
func WithPeers(fn func(ctx context.Context) []PeerLink) Option {
	return func(s *Server) { s.peersFn = fn }
}

// WithSpotifyControl registers the function that starts playback of a
// Spotify URI on a given account in go-librespot (the Spotify-preset
// control plane). An empty account plays with the current login; shuffle
// selects a fresh random start over the default resume-where-left-off.
func WithSpotifyControl(play func(ctx context.Context, uri, account string, shuffle bool) error) Option {
	return func(s *Server) { s.spotifyPlay = play }
}

// WithSpotifyUser registers the resolver for go-librespot's current account,
// used to stamp the account onto a newly saved Spotify preset.
func WithSpotifyUser(user func(ctx context.Context) string) Option {
	return func(s *Server) { s.spotifyUser = user }
}

// WithSpotifyContext registers the resolver for the Spotify context URI
// go-librespot is currently playing, used by the preset-save path to stamp the
// live account when saving the content that is playing right now.
func WithSpotifyContext(ctxURI func() string) Option {
	return func(s *Server) { s.spotifyContext = ctxURI }
}

// WithSpotifyMeta registers the resolver for a Spotify context's stable cover
// image and human title, stamped onto a newly saved Spotify preset.
func WithSpotifyMeta(meta func(ctx context.Context, uri string) (cover, title string)) Option {
	return func(s *Server) { s.spotifyMeta = meta }
}

// WithSpotifyStreaming registers the predicate that reports whether the box is
// currently pulling the Ogg stream, used by verifyRecall to avoid a disruptive
// re-issue while Spotify is already playing.
func WithSpotifyStreaming(streaming func() bool) Option {
	return func(s *Server) { s.spotifyStreaming = streaming }
}

// WithSpotifyReady registers the predicate that reports whether go-librespot has
// finished authenticating, so a soft Spotify recall can wait out a cold start.
func WithSpotifyReady(ready func() bool) Option {
	return func(s *Server) { s.spotifyReady = ready }
}

// WithSpotifySkip registers the hook that skips go-librespot to the next
// (forward=true) or previous track, wired to the phone remote's Previous/Next
// controls. Mirrors the hardware remote's Spotify skip.
func WithSpotifySkip(skip func(ctx context.Context, forward bool) error) Option {
	return func(s *Server) { s.spotifySkip = skip }
}

// WithSpotifyPremiumRequired registers the predicate that reports whether the
// logged-in Spotify account is free/open and so cannot do the autonomous recall
// playback a preset needs (#45). The recall handler uses it to answer with a
// clear "needs Premium" message instead of failing silently.
func WithSpotifyPremiumRequired(f func() bool) Option {
	return func(s *Server) { s.spotifyPremiumRequired = f }
}

// WithSpotifyCanRecall registers the predicate that reports whether a Spotify
// recall can proceed (a live go-librespot session OR a persisted credential), so
// a recall on a genuinely-never-logged-in speaker fails with a clear, actionable
// message while one with a live session still plays (#45; Patrick, 2026-06-24).
func WithSpotifyCanRecall(f func(ctx context.Context) bool) Option {
	return func(s *Server) { s.spotifyCanRecall = f }
}

// WithSpotifyExportCred registers the function that returns this box's active
// go-librespot credential so it can be copied to other speakers (#45 sync).
func WithSpotifyExportCred(f func() ([]byte, error)) Option {
	return func(s *Server) { s.spotifyExportCred = f }
}

// WithSpotifyImportCred registers the function that installs a credential copied
// from another speaker and restarts go-librespot to log in with it (#45 sync).
func WithSpotifyImportCred(f func(ctx context.Context, data []byte) error) Option {
	return func(s *Server) { s.spotifyImportCred = f }
}

// WithSpotifySetRecalling registers the hook that marks an in-flight recall so
// ServeOgg drives the new track from its start.
func WithSpotifySetRecalling(setRecalling func()) Option {
	return func(s *Server) { s.spotifySetRecalling = setRecalling }
}

// WithSpotifySuppressActivate registers the hook that holds go-librespot's
// auto-repoint off for a window, so the hardware-skip recovery's clean slot
// recall is not raced by the #14 auto-attach.
func WithSpotifySuppressActivate(suppress func(time.Duration)) Option {
	return func(s *Server) { s.spotifySuppressActivate = suppress }
}

// WithRecent wires the recently-played ring (#135) so the play handlers record
// the user's listening history and GET /api/recent serves it.
func WithRecent(r *recent.Store) Option {
	return func(s *Server) { s.recent = r }
}

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
	s := &Server{addr: addr, logger: logger, queue: newPlayQueue()}
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
	mux.HandleFunc("/api/peers", s.handlePeers)
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
	mux.HandleFunc("/api/box/presets", s.handleBoxPresets)
	mux.HandleFunc("/api/box/presets/recall", s.handleBoxPresetRecall)
	mux.HandleFunc("/api/box/snapshot", s.handleBoxSnapshot)
	mux.HandleFunc("/api/box/snapshot/restore", s.handleBoxSnapshotRestore)
	mux.HandleFunc("/api/announce", s.handleAnnounce)
	mux.HandleFunc("/announce/audio", s.handleAnnounceAudio)
	mux.HandleFunc("/api/box/sync-presets", s.handleBoxSyncPresets)
	mux.HandleFunc("/api/box/zone", s.handleBoxZone)
	mux.HandleFunc("/api/box/zone/purge", s.handleZonePurge)
	mux.HandleFunc("/api/box/group", s.handleBoxGroup)
	mux.HandleFunc("/api/webhooks", s.handleWebhooks)
	mux.HandleFunc("/api/webhooks/test", s.handleWebhooksTest)
	mux.HandleFunc("/api/stick/status", s.handleStickStatus)
	mux.HandleFunc("/api/debug/state", s.handleDebugState)
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

// ---- Presets CRUD ----

func (s *Server) handlePresets(w http.ResponseWriter, r *http.Request) {
	if s.presets == nil {
		http.Error(w, "presets store not initialized", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		all := s.presets.All()
		// Phase marker for the "presets reported empty" symptom on #60.
		// A WARN log on every empty GET makes it directly visible in the
		// diagnostic bundle whether the desktop app actually polled the
		// agent and received an empty array (vs the agent never being
		// reached). Non-empty responses stay at Debug to avoid noise.
		if len(all) == 0 {
			s.logger.Warn("preset store phase: GET /api/presets returned empty",
				"remote", r.RemoteAddr)
		} else {
			s.logger.Debug("GET /api/presets", "count", len(all), "remote", r.RemoteAddr)
		}
		writeJSON(w, http.StatusOK, all)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePresetSlot(w http.ResponseWriter, r *http.Request) {
	if s.presets == nil {
		http.Error(w, "presets store not initialized", http.StatusServiceUnavailable)
		return
	}
	slotStr := strings.TrimPrefix(r.URL.Path, "/api/presets/")
	slot, err := strconv.Atoi(slotStr)
	if err != nil || slot < 1 || slot > 6 {
		http.Error(w, "invalid slot, must be 1-6", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		p, ok := s.presets.Get(slot)
		if !ok {
			http.Error(w, "preset not set", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, p)
	case http.MethodPut:
		var p presets.Preset
		if !decodeJSONRequest(w, r, 1<<16, &p) {
			return
		}
		p.Slot = slot
		if p.Type == "" {
			p.Type = "radio"
		}
		// A queue preset (a saved DLNA folder, #queue-preset) has no single
		// StreamURL/URI: it carries an ordered Items list and a Shuffle flag and
		// recalls into the agent play-queue. It skips the radio/spotify URL gates
		// below; require at least one item with a URL instead, mirroring their
		// 422 shape. The dedup loop below is keyed on URI/StreamURL, both empty
		// here, so it is harmless for queue presets and leaves other slots alone.
		if p.Type == "queue" {
			hasItem := false
			for _, it := range p.Items {
				if it.URL != "" {
					hasItem = true
					break
				}
			}
			if !hasItem {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error": "This folder can't be saved as a preset: it has no playable tracks. Open a folder with audio files and try again.",
					"code":  "queue-empty",
				})
				return
			}
			if err := s.presets.SetSlot(p); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// Register the slot on the box so the hardware button is mapped. The
			// physical press is intercepted by RecallSlot (which starts the queue),
			// but the box still needs an entry for the key to fire at all, so point
			// it at this slot's stream proxy URL like every other preset.
			if s.boxHost != "" {
				boxCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				if err := boxcli.AddPreset(boxCtx, s.boxHost, slot, p.Name, boxPresetURL(slot, false)); err != nil {
					s.logger.Warn("box preset sync failed", "slot", slot, "err", err)
				}
				cancel()
			}
			writeJSON(w, http.StatusOK, p)
			return
		}
		// Reject (or heal) the saves that produced the dead presets that fail to
		// recall with "Service not available" (#45/#105). A type=spotify preset
		// MUST carry a replayable context URI; a non-spotify preset MUST carry a
		// real http(s) stream URL. A non-spotify preset whose stream URL actually
		// encodes a Spotify container (an older mis-save) is healed into a proper
		// Spotify preset instead of being stored as a dead radio link.
		if p.Type == "spotify" {
			if !playableSpotifyURI(p.URI) {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error": "This Spotify selection can't be saved to a preset: it has no replayable playlist, album or track. Open a playlist or album and try again.",
					"code":  "spotify-uri-unplayable",
				})
				return
			}
		} else if p.StreamURL == "" {
			// An EMPTY stream URL used to slip through the invalid-URL gate below
			// and save as a dead radio preset: the box then fetches /stream/<slot>
			// and the proxy 404s, i.e. a button that assigns fine but never plays
			// (#252). A non-spotify, non-queue preset has nothing else playable,
			// except a Spotify URI mis-typed as radio, which is healed instead.
			if playableSpotifyURI(p.URI) {
				p.Type = "spotify"
			} else {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error": "This selection can't be saved as a preset (no playable stream). Pick a radio station or a Spotify playlist and try again.",
					"code":  "stream-url-missing",
				})
				return
			}
		} else if !isHTTPURL(p.StreamURL) {
			if uri := legacySpotifyURI(p.StreamURL); uri != "" {
				p.Type, p.URI, p.StreamURL = "spotify", uri, ""
			} else {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error": "This selection can't be saved as a preset (no playable stream). Pick a radio station or a Spotify playlist and try again.",
					"code":  "stream-url-invalid",
				})
				return
			}
		}
		// A stream URL that points at THIS agent's own /stream/<n> proxy is a
		// poisoned save (#252): an older client stored the box-visible proxy
		// location instead of the station's origin URL, permanently
		// clobbering the preset (recall then trips the SSRF dial guard with
		// AUDIO_ERROR_BAD_URL). Heal it from the referenced slot's stored
		// origin; refuse when there is nothing left to heal from.
		if p.Type != "spotify" && p.StreamURL != "" {
			if ref, self := selfProxySlot(p.StreamURL); self {
				healed := false
				for _, src := range s.presets.All() {
					if src.Slot != ref || src.StreamURL == "" {
						continue
					}
					if _, srcSelf := selfProxySlot(src.StreamURL); srcSelf {
						break // the stored entry is poisoned too: origin lost
					}
					s.logger.Warn("preset save: healed a self-proxy stream URL from the referenced slot's stored origin (#252)",
						"slot", slot, "ref", ref)
					p.StreamURL = src.StreamURL
					if p.Codec == "" {
						p.Codec = src.Codec
					}
					if p.Bitrate == 0 {
						p.Bitrate = src.Bitrate
					}
					if p.Name == "" {
						p.Name = src.Name
					}
					if p.Art == "" {
						p.Art = src.Art
					}
					healed = true
					break
				}
				if !healed {
					writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
						"error": "This save would store the speaker's own proxy address instead of the station. Play the station again (or re-search it) and save then.",
						"code":  "stream-url-self-proxy",
					})
					return
				}
			}
		}
		// Stamp the account a Spotify preset belongs to (go-librespot's current
		// login) so a later recall can switch back to it on a multi-account box
		// (#27). Two cases: (a) no account yet, fill it from the current login;
		// (b) the preset being saved IS the content go-librespot is playing right
		// now, so the live account owns it and must win even over a stale account
		// carried in from an earlier save. Case (b) fixes the report that a preset
		// saved from a second household member's Spotify session kept the first
		// member's account (jensukk) because the old value was never refreshed
		// (ST30, 2026-07-14). A save for a NON-playing preset keeps its stored
		// account, so a bulk rename never clobbers another account's preset.
		// Account + cover are best-effort enrichment: use a fresh background
		// context, not r.Context(), so a client that disconnects right after the
		// PUT (e.g. a raw one-shot request) does not cancel them mid-fetch.
		if p.Type == "spotify" && s.spotifyUser != nil {
			savingLiveContext := p.URI != "" && s.spotifyContext != nil && s.spotifyContext() == p.URI
			if p.Account == "" || savingLiveContext {
				uctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				if u := s.spotifyUser(uctx); u != "" && u != p.Account {
					if p.Account != "" {
						s.logger.Info("preset save: refreshed Spotify account to the live playing account", "slot", slot, "from", p.Account, "to", u)
					}
					p.Account = u
				}
				cancel()
			}
		}
		// Give a Spotify preset a stable tile logo (the playlist image, #24) and a
		// real name (the playlist title), so the box display and the tile show
		// e.g. "Jens Chill" instead of a bare "Spotify". Only fills empties / a
		// placeholder name.
		if p.Type == "spotify" && p.URI != "" && s.spotifyMeta != nil &&
			(p.Art == "" || p.Name == "" || p.Name == "Spotify") {
			cctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			cover, title := s.spotifyMeta(cctx, p.URI)
			if p.Art == "" && cover != "" {
				p.Art = cover
			}
			if (p.Name == "" || p.Name == "Spotify") && title != "" {
				p.Name = title
			}
			cancel()
		}
		// Dedup: a given playlist/station lives on at most ONE preset. Saving it
		// here removes it from any other slot first (matched by Spotify URI, or
		// by stream URL for radio), so the same content cannot occupy two
		// buttons (user request; also avoids two Spotify presets colliding).
		for _, other := range s.presets.All() {
			if other.Slot == slot {
				continue
			}
			dup := (p.Type == "spotify" && p.URI != "" && other.URI == p.URI) ||
				(p.Type != "spotify" && p.StreamURL != "" && other.StreamURL == p.StreamURL)
			if !dup {
				continue
			}
			_ = s.presets.RemoveSlot(other.Slot)
			s.logger.Info("preset dedup: removed duplicate from other slot", "kept", slot, "removed", other.Slot)
			if s.boxHost != "" {
				rmCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = boxcli.RemovePreset(rmCtx, s.boxHost, other.Slot)
				cancel()
			}
		}
		if err := s.presets.SetSlot(p); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Sync to the box so hardware buttons know the correct slot.
		// Bose gets the stream proxy URL, not the real CDN.
		// This way the stream survives token expiry.
		if s.boxHost != "" {
			boxCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			// Spotify presets store the live Ogg stream as the box-side location
			// so the box's own activation on a hardware press attaches cleanly
			// instead of failing on /stream/<slot> (no Spotify source) and
			// flashing "service unavailable" (#22).
			proxyURL := boxPresetURL(slot, p.Type == "spotify")
			if err := boxcli.AddPreset(boxCtx, s.boxHost, slot, p.Name, proxyURL); err != nil {
				s.logger.Warn("box preset sync failed", "slot", slot, "err", err)
			}
			cancel()
		}
		writeJSON(w, http.StatusOK, p)
	case http.MethodDelete:
		if err := s.presets.RemoveSlot(slot); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Drop it from the box-preset snapshot right away and tombstone the slot so
		// a trailing gabbo presetsUpdated does not resurface it as a foreign (UPNP)
		// entry the user then "cannot delete" (reported after an app restart).
		s.forgetBoxPreset(slot)
		if s.boxHost != "" {
			boxCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			// Log the box-side removal outcome instead of dropping it: a silent
			// failure here is exactly how the preset comes back on the next boot.
			if err := boxcli.RemovePreset(boxCtx, s.boxHost, slot); err != nil {
				s.logger.Warn("preset delete: box-side RemovePreset failed", "slot", slot, "err", err)
			}
			cancel()
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// playableSpotifyURI reports whether uri is a Spotify context that go-librespot
// can replay on a preset recall. This is a SAVE gate, deliberately permissive:
// /player/play accepts any well-formed spotify: context (playlist, album, track,
// artist, show/podcast, episode, collection/Liked Songs, and user-scoped
// playlists like spotify:user:<id>:playlist:<id>), so we accept any non-empty
// spotify: URI with a real id and reject only what genuinely cannot recall: an
// empty URI or a /spotify/stream / container URL stored as a "URI" (the
// dead-preset cause, #45/#105). go-librespot is the authority on real
// playability; over-narrowing here wrongly blocked podcast/Liked-Songs saves.
func playableSpotifyURI(uri string) bool {
	uri = strings.TrimSpace(uri)
	if !strings.HasPrefix(uri, "spotify:") || looksLikeSpotifyStreamURL(uri) {
		return false
	}
	parts := strings.Split(uri, ":")
	// Require a kind and a non-empty trailing id: rejects "spotify:" and
	// "spotify:playlist:", accepts spotify:playlist:ID, spotify:show:ID,
	// spotify:episode:ID, spotify:collection, spotify:user:<id>:playlist:<id>.
	return len(parts) >= 2 && parts[1] != "" && parts[len(parts)-1] != ""
}

// isHTTPURL reports whether s is a real http(s) URL the stream proxy can fetch.
func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// isPlainHTTPURL reports whether s is a plaintext http:// URL (not https). A LAN
// media server reachable this way can be played by the box directly, skipping
// the stream proxy: the proxy exists to give Bose UPnP HTTPS and radio token
// resilience, neither of which a plain-HTTP LAN file needs (#139).
func isPlainHTTPURL(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), "http://")
}

// mimeFromURL guesses an audio MIME from a stream URL's file extension. Used to
// recall a library preset that did not record its codec MIME. Returns "" for an
// unknown or missing extension, in which case the caller leaves the box on its
// audio/mpeg default.
func mimeFromURL(raw string) string {
	u := strings.ToLower(raw)
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	switch {
	case strings.HasSuffix(u, ".flac"):
		return "audio/flac"
	case strings.HasSuffix(u, ".wav"):
		return "audio/wav"
	case strings.HasSuffix(u, ".m4a"), strings.HasSuffix(u, ".mp4"), strings.HasSuffix(u, ".aac"):
		return "audio/mp4"
	case strings.HasSuffix(u, ".ogg"), strings.HasSuffix(u, ".oga"):
		return "audio/ogg"
	case strings.HasSuffix(u, ".aif"), strings.HasSuffix(u, ".aiff"):
		return "audio/aiff"
	case strings.HasSuffix(u, ".mp3"):
		return "audio/mpeg"
	}
	return ""
}

// looksLikeSpotifyStreamURL reports whether a stored stream URL points at a
// Spotify source, so a non-spotify preset carrying it is really a mis-saved
// Spotify preset (#45/#105).
func looksLikeSpotifyStreamURL(s string) bool {
	return strings.Contains(s, "/spotify/stream") || strings.Contains(s, "/playback/container/")
}

// legacySpotifyURI recovers the spotify: context URI from a preset that an older
// version mis-saved as a non-spotify preset whose stream URL encoded a Spotify
// container, e.g. "/playback/container/<base64 spotify:playlist:...>". Returns
// "" when the URL is a normal radio/HTTP stream or carries no recoverable URI.
func legacySpotifyURI(streamURL string) string {
	const marker = "/playback/container/"
	i := strings.Index(streamURL, marker)
	if i < 0 {
		return ""
	}
	enc := streamURL[i+len(marker):]
	if j := strings.IndexAny(enc, "/?#"); j >= 0 {
		enc = enc[:j]
	}
	// The container is encoded with RawURLEncoding (boxurl), a URL-safe alphabet
	// with no '/'; URLEncoding (padded) is accepted too. The Std alphabets are
	// intentionally omitted: they can emit '/', which the cut above would have
	// truncated, so they could never round-trip here anyway.
	for _, d := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding} {
		if b, err := d.DecodeString(enc); err == nil && strings.HasPrefix(string(b), "spotify:") {
			return string(b)
		}
	}
	return ""
}

// ---- Play / Pause / Stop ----

type playRequest struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Icon  string `json:"icon"` // albumArtURI for box display
	UUID  string `json:"uuid"` // optional, for click tracking
	// Mime is the source codec MIME (audio/flac, audio/mp4, ...) for a network
	// library track, so the box decodes it correctly. Empty for radio -> the
	// renderer defaults to audio/mpeg.
	Mime string `json:"mime"`
	// Homepage is the station website (radio only), recorded into Recently-played
	// so a card can offer a "website" link like the radio search rows do (#135).
	Homepage string `json:"homepage"`
	// Codec is the station codec as reported by radio-browser ("MP3", "AAC",
	// "AAC+", ...). It selects the DIDL protocolInfo MIME for RADIO plays: the
	// box keys its decoder off that MIME, and an HE-AAC station labelled with
	// the fixed audio/mpeg default played SILENCE while the proxy forwarded
	// its bytes fine (#252, "Absolut Relax"). Deliberately a separate field
	// from Mime, which doubles as the "library file, play direct" marker
	// (#139) and must not be set for radio.
	Codec string `json:"codec"`
}

// handleWebhooks gets (GET) or replaces (PUT) the webhook config. The config
// holds the user's HTTP request(s) fired on a box trigger (today: the remote
// thumbs keys, see boxws OnThumbActivity).
func (s *Server) handleWebhooks(w http.ResponseWriter, r *http.Request) {
	if s.webhooks == nil {
		http.Error(w, "webhooks not configured", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.webhooks.Get())
	case http.MethodPut:
		var c webhooks.Config
		if !decodeJSONRequest(w, r, 1<<16, &c) {
			return
		}
		if err := s.webhooks.Set(c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, s.webhooks.Get())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleWebhooksTest fires an action immediately so the user can verify their
// URL from the app without pressing a key on the box. Body is an optional
// webhooks.Action; when absent or empty, the configured thumb action is fired.
func (s *Server) handleWebhooksTest(w http.ResponseWriter, r *http.Request) {
	if s.webhooks == nil {
		http.Error(w, "webhooks not configured", http.StatusServiceUnavailable)
		return
	}
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var a webhooks.Action
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&a)
	}
	// Fall back to the saved thumb action only when nothing testable was posted.
	// Configured() (not a bare URL check) so a udp/wol action, which has no URL,
	// is testable too (#187).
	if !a.Configured() {
		a = s.webhooks.Get().Thumb
	}
	if !a.Configured() {
		http.Error(w, "nothing to test (configure the action first)", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	code, err := s.webhooks.Fire(ctx, a)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": code >= 200 && code < 400, "status": code})
}

func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	if s.renderer == nil {
		http.Error(w, "renderer not configured (set --box-host)", http.StatusServiceUnavailable)
		return
	}
	var req playRequest
	if !decodeJSONRequest(w, r, 1<<16, &req) {
		return
	}
	if req.URL == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}
	s.ensureBoxReady(r.Context())
	// Detach the play from the request context (#252, same pattern as the
	// preset recall): the standby wake above can outlast the app's HTTP
	// timeout, and a caller that gave up must not cancel the playback it asked
	// for mid-start ("context canceled" right after a slow wake).
	playCtx, playCancel := context.WithTimeout(context.WithoutCancel(r.Context()), playDetachTimeout)
	defer playCancel()
	// A single play replaces any active library queue, so stop auto-advancing.
	s.stopQueue()
	// Ad-hoc radio: the box leaves any Spotify source; suppress the #14
	// auto-attach so it does not jump back to Spotify.
	if s.spotifySwitchedAway != nil {
		s.spotifySwitchedAway(playCtx)
	}
	// Decide how the box reaches the audio.
	//
	// Radio and any HTTPS source go through our loopback stream proxy: it hands
	// the box a stable URL, lets Bose UPnP play HTTPS (which it cannot do
	// itself), and reconnects transparently on CDN token expiry.
	//
	// A network library track is different. It carries its codec MIME (set by
	// the caller) and is a finite file on a LAN media server that supports HTTP
	// range requests, exactly the case the Bose app played directly. Routing it
	// through the radio proxy breaks it two ways: the proxy ignores the box's
	// Range requests, so the box cannot read a FLAC's stream header and sits at
	// "stream starting", and, built for endless radio, it treats the upstream
	// EOF that ends a file as a dropout and reconnects, replaying or garbling
	// the track (the mid-track noise). So a plain-HTTP library file is handed to
	// the box directly, like the Bose app did; only radio or an HTTPS library
	// source still needs the proxy (#139).
	playDirect := req.Mime != "" && isPlainHTTPURL(req.URL)
	playURL := boxurl.RawStream(req.URL)
	if playDirect {
		playURL = req.URL
	}
	s.logger.Info("play request", "direct", playDirect, "mime", req.Mime, "codec", req.Codec, "url", req.URL)
	// Advertise the real codec to the box when the caller knows it (a network
	// library track carries its DLNA-reported MIME, e.g. audio/flac, audio/mp4).
	// The box keys its decoder off this protocolInfo MIME, so a FLAC/ALAC/M4A
	// file mislabelled as audio/mpeg is rejected (AUDIO_ERROR_BAD_URL) while an
	// MP3 plays (#139). Radio derives the MIME from the station codec instead:
	// an AAC/HE-AAC station must be labelled audio/aac or the box decodes it as
	// MPEG and plays silence (#252); MP3/unknown keeps the audio/mpeg default.
	mime := req.Mime
	if mime == "" {
		mime = upnp.MimeForCodec(req.Codec)
	}
	var playErr error
	if mime != "" {
		playErr = s.renderer.PlayURLMime(playCtx, playURL, req.Title, req.Icon, mime)
	} else {
		playErr = s.renderer.PlayURL(playCtx, playURL, req.Title, req.Icon)
	}
	if playErr != nil {
		if isGroupedRejection(playErr) {
			s.writeGroupedPlayError(w, playErr)
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "Station could not be played",
			"detail": guessErrorReason(playErr),
			"url":    req.URL,
		})
		return
	}
	s.setLastPlay(playURL, req.Title, req.Icon, mime)
	// Recently-played (#135): a network-library file carries a MIME; radio does
	// not. Record the original URL as the replayable card target, not the proxy.
	if req.Mime != "" {
		s.recentNoteCard("upnp", req.URL, req.Title, req.Icon, req.URL, "", "")
	} else {
		s.recentNoteCard("radio", req.URL, req.Title, req.Icon, req.URL, "", req.Homepage)
	}
	// radio-browser click-tracking moved app-side (the app fires RadioClick
	// when it starts playback) so the box no longer needs the radiobrowser pkg.
	writeJSON(w, http.StatusOK, map[string]string{"status": "playing", "url": req.URL})
}

func (s *Server) handlePlaySlot(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
	if s.presets == nil {
		http.Error(w, "presets store not initialized", http.StatusServiceUnavailable)
		return
	}
	slotStr := strings.TrimPrefix(r.URL.Path, "/api/play/")
	slot, err := strconv.Atoi(slotStr)
	if err != nil {
		http.Error(w, "invalid slot", http.StatusBadRequest)
		return
	}
	p, ok := s.presets.Get(slot)
	if !ok {
		http.Error(w, "preset not configured", http.StatusNotFound)
		return
	}
	s.ensureBoxReady(r.Context())
	// recallStart anchors the verify's stand-down decision: a deliberate user
	// stop/pause/power-off that arrives AFTER this moment must end the verify
	// retries (the rolling userStopWindow alone expires between the 5s ticks).
	recallStart := time.Now()
	// Detach every box-facing step of this recall from the request context
	// (#252): the standby wake above can outlast the app's HTTP timeout, and a
	// caller that gave up must not cancel the playback it asked for mid-start.
	// Previously only the radio branch was detached; the Spotify, library and
	// queue recalls still died with "context canceled" after a slow wake.
	playCtx, playCancel := context.WithTimeout(context.WithoutCancel(r.Context()), playDetachTimeout)
	defer playCancel()
	// A queue preset (a saved DLNA folder) recalls into the agent play-queue
	// instead of the single-URL play below: reload its ordered tracks with the
	// saved shuffle flag and start from the first. We already hold boxCmdMu, so
	// use the *Locked variant (startQueue would re-lock and deadlock).
	if p.Type == "queue" {
		items := presetItemsToQueue(p.Items)
		if len(items) == 0 {
			http.Error(w, "preset has no playable tracks", http.StatusUnprocessableEntity)
			return
		}
		s.logger.Info("preset slot recall (app): queue", "slot", slot, "tracks", len(items), "shuffle", p.Shuffle)
		card := recentCardCtx{key: fmt.Sprintf("queue:slot:%d", slot), name: p.Name, art: p.Art}
		if err := s.startQueueLocked(playCtx, items, 0, p.Shuffle, repeatOff, card); err != nil {
			if isGroupedRejection(err) {
				s.writeGroupedPlayError(w, err)
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": "Folder could not be played", "detail": guessErrorReason(err),
				"slot": slot, "name": p.Name,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "playing", "slot": slot, "name": p.Name, "type": "queue"})
		return
	}
	// Every non-queue recall replaces an active library queue. Without this the
	// queue watcher kept evaluating the OLD track's timing and, when its
	// wall-clock net tripped minutes later, yanked playback from the station
	// the user explicitly chose back to the next queue track.
	s.stopQueue()
	// Heal a legacy mis-saved Spotify preset before recall: older versions could
	// store a Spotify selection as a non-spotify preset whose stream URL encoded
	// the Spotify container (e.g. /playback/container/<base64 spotify:...>). The
	// radio path would then stream-proxy a scheme-less URL and the box would get
	// nothing, which is the "Service not available" recall failure (#45/#105).
	// Recover the URI and route to the Spotify path; if it is a Spotify stream
	// with no recoverable URI, tell the user to re-save instead of pushing a
	// doomed /stream/<slot>.
	if p.Type != "spotify" && p.StreamURL != "" && !isHTTPURL(p.StreamURL) {
		if uri := legacySpotifyURI(p.StreamURL); uri != "" {
			p.Type, p.URI = "spotify", uri
			s.logger.Info("preset recall: healed legacy spotify preset", "slot", slot, "uri", uri)
		} else if looksLikeSpotifyStreamURL(p.StreamURL) {
			s.logger.Warn("preset recall: spotify preset has no replayable URI", "slot", slot, "url", p.StreamURL)
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": "This Spotify preset was saved in an older version and can't be replayed. Please open the playlist and save it to the preset again.",
				"code":  "spotify-preset-unreplayable",
				"slot":  slot, "name": p.Name,
			})
			return
		}
	}
	// Spotify presets have no playable HTTP StreamURL. Mirror the hardware-press
	// recall (cmd/agent playSpotifyPreset) so a soft recall behaves identically:
	//  1. wait out a cold go-librespot (auth not finished) instead of pointing
	//     the box at a not-yet-flowing stream, which starves and detaches after
	//     ~30s and forced the user to press the preset a second time,
	//  2. mark the recall so ServeOgg drives the new track from its start,
	//  3. point the box at THIS slot's stream first (now_playing shows the name
	//     and buffers) and load the playlist audio after, so the box buffers
	//     until audio flows.
	// Log every app-side slot recall so a remote "recall does nothing" report
	// (ST20 #45) shows the preset shape that was attempted.
	s.logger.Info("preset slot recall (app)", "slot", slot, "type", p.Type, "hasURI", p.URI != "", "account", p.Account)
	if p.Type == "spotify" && p.URI == "" {
		s.logger.Warn("spotify preset recall (app): type=spotify but empty URI, falling through to radio path", "slot", slot, "name", p.Name)
	}
	if p.Type == "spotify" && p.URI != "" {
		if s.spotifyPlay == nil {
			s.logger.Warn("spotify preset recall (app): Spotify not configured on this box", "slot", slot)
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": "Spotify not configured", "slot": slot, "name": p.Name,
			})
			return
		}
		// A speaker that has never been logged into Spotify AND holds no live
		// session has no way for go-librespot to start playback on its own, so the
		// recall would silently do nothing (#45 Pierre: saved preset account="" and
		// go-librespot not running). Tell the user how to fix it instead of
		// optimistically reporting "playing" and failing in the background. Gate on
		// CanRecall (live session OR persisted credential), NOT a persisted
		// credential alone: a box with a live-but-never-persisted zeroconf session
		// plays Spotify fine yet reports not-logged-in, and gating on the credential
		// alone wrongly refused its recall (Patrick, ST10, 2026-06-24).
		// Checked on the detached context: on a slow wake the request context
		// is already cancelled here and the probe would misreport "not picked
		// in Spotify yet" (422) for a speaker that is logged in fine (#252).
		if s.spotifyCanRecall != nil && !s.spotifyCanRecall(playCtx) {
			s.logger.Info("spotify preset recall (app): speaker not logged into Spotify", "slot", slot)
			// STR plays Spotify through this speaker as a Spotify Connect receiver
			// (the go-librespot sidecar), not via any Bose account link. The
			// speaker has to be picked in Spotify once so it stores a credential.
			// The desktop app branches on the code, so this wording is free to be
			// the accurate, non-Bose-linking instruction.
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": "This speaker has not been picked in Spotify yet. In the Spotify app on a device on the same Wi-Fi, tap the Connect/devices icon, choose this speaker and play any track once. After that this preset will recall on its own.",
				"code":  "spotify-not-logged-in",
				"slot":  slot, "name": p.Name,
			})
			return
		}
		// A free/open Spotify account cannot do the autonomous on-demand playback a
		// recall needs (it can only play when the phone app drives it), so the
		// recall would silently fail. Tell the user it needs Premium instead (#45).
		if s.spotifyPremiumRequired != nil && s.spotifyPremiumRequired() {
			s.logger.Info("spotify preset recall (app): account is free/open, recall needs Premium", "slot", slot)
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": "This speaker's Spotify account is free. Spotify preset recall needs Spotify Premium.",
				"code":  "spotify-premium-required",
				"slot":  slot, "name": p.Name,
			})
			return
		}
		// Mark the recall and point the box at THIS slot's stream FIRST (the box
		// shows the name and buffers), then answer the request right away. The
		// slow part (waiting out a cold go-librespot + loading the playlist audio
		// + verify) runs in the background, so the box buffers until audio flows
		// instead of starving and detaching, AND the desktop POST does not block
		// on a 12s cold-start wait (which playPost would mis-report as the box not
		// being ready, i.e. "speaker is still starting").
		if s.spotifySetRecalling != nil {
			s.spotifySetRecalling()
		}
		slotURL := boxurl.SpotifySlot(slot)
		if err := s.renderer.PlayURLMime(playCtx, slotURL, p.Name, p.Art, "audio/ogg"); err != nil {
			if isGroupedRejection(err) {
				s.writeGroupedPlayError(w, err)
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": "Spotify stream could not be played", "detail": guessErrorReason(err),
				"slot": slot, "name": p.Name,
			})
			return
		}
		gen := s.setLastPlay(slotURL, p.Name, p.Art, "audio/ogg")
		s.recentNoteCard("spotify", p.URI, p.Name, p.Art, p.URI, p.Account, "") // #135
		uri, name, art, account, shuffle := p.URI, p.Name, p.Art, p.Account, p.Shuffle
		go func() {
			bg := context.Background()
			t0 := time.Now()
			warm := s.spotifyReady == nil || s.spotifyReady()
			if s.spotifyReady != nil && !s.spotifyReady() {
				s.logger.Info("spotify soft recall: waiting for go-librespot ready (cold start)", "slot", slot)
				for i := 0; i < 24 && !s.spotifyReady(); i++ {
					time.Sleep(500 * time.Millisecond)
				}
			}
			if err := s.spotifyPlay(bg, uri, account, shuffle); err != nil {
				s.logger.Warn("spotify play (initial) failed, will verify+retry", "slot", slot, "err", err)
			}
			s.logger.Info("spotify soft recall: context load issued", "slot", slot, "warm", warm, "loadAfterMs", time.Since(t0).Milliseconds())
			s.verifyRecall(gen, recallStart, slotURL, func(ctx context.Context, lastAttempt bool) {
				// Re-point the box at the stream WITHOUT re-Play on the early
				// tries: ServeOgg resumes go-librespot on attach, so this
				// re-attaches without reshuffling/restarting the track (a re-Play
				// every retry was the "same song restarts a few seconds in" bug,
				// fixed for hardware in v0.7.4 but previously still present here).
				// Only the last attempt does a full re-Play, to recover a genuine
				// cold-boot auth race where the playlist never loaded at all.
				if lastAttempt {
					_ = s.spotifyPlay(ctx, uri, account, shuffle)
				}
				_ = s.renderer.PlayURLMime(ctx, slotURL, name, art, "audio/ogg")
			}, s.spotifyStreaming)
		}()
		writeJSON(w, http.StatusOK, map[string]any{"status": "playing", "slot": slot, "name": p.Name, "type": "spotify"})
		return
	}
	// Radio recall: tell Spotify the box switched away so its #14 auto-attach
	// does not yank the box back to a still-advancing go-librespot.
	if s.spotifySwitchedAway != nil {
		s.spotifySwitchedAway(playCtx)
	}
	// A library preset (saved from the Library tab, so Source is set) points at a
	// finite file on a LAN media server. Recall it like the Library play path:
	// hand the box the file directly with its codec MIME, bypassing the radio
	// stream proxy that stalls a FLAC on Range and garbles it on EOF (#139). The
	// MIME is re-derived from the URL because the preset store does not keep it;
	// an unknown extension falls through to the proxy path unchanged.
	if p.Source != "" && isPlainHTTPURL(p.StreamURL) {
		if mime := mimeFromURL(p.StreamURL); mime != "" {
			directURL := p.StreamURL
			s.logger.Info("preset slot recall (app): direct library file", "slot", slot, "mime", mime)
			if err := s.renderer.PlayURLMime(playCtx, directURL, p.Name, p.Art, mime); err != nil {
				if isGroupedRejection(err) {
					s.writeGroupedPlayError(w, err)
					return
				}
				writeJSON(w, http.StatusBadGateway, map[string]any{
					"error": "Track could not be played", "detail": guessErrorReason(err),
					"slot": slot, "name": p.Name,
				})
				return
			}
			gen := s.setLastPlay(directURL, p.Name, p.Art, mime)
			s.recentNoteCard("upnp", p.StreamURL, p.Name, p.Art, p.StreamURL, "", "") // #135
			name, art := p.Name, p.Art
			go s.verifyRecall(gen, recallStart, directURL, func(ctx context.Context, _ bool) {
				_ = s.renderer.PlayURLMime(ctx, directURL, name, art, mime)
			}, nil)
			writeJSON(w, http.StatusOK, map[string]any{"status": "playing", "slot": slot, "name": p.Name})
			return
		}
	}
	// Use the stream proxy URL so playback continues even after token
	// expiry (Bose sees the stable loopback URL).
	playURL := boxurl.StreamSlot(slot)
	// A preset saved from an AAC/HE-AAC station carries its codec: label the
	// stream audio/aac in the DIDL so the box picks the right decoder. The
	// fixed audio/mpeg label made those stations play silence (#252).
	mime := upnp.MimeForCodec(p.Codec)
	var playErr error
	if mime != "" {
		playErr = s.renderer.PlayURLMime(playCtx, playURL, p.Name, p.Art, mime)
	} else {
		playErr = s.renderer.PlayURL(playCtx, playURL, p.Name, p.Art)
	}
	if playErr != nil {
		// Log the failed radio recall: this 502 used to be returned with no
		// agent-side trace at all, so a remote diagnostic bundle showed a recall
		// that apparently never happened (#252).
		s.logger.Warn("preset slot recall (app): radio play failed",
			"slot", slot, "name", p.Name, "playURL", playURL, "err", playErr)
		if isGroupedRejection(playErr) {
			s.writeGroupedPlayError(w, playErr)
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":  "Station could not be played",
			"detail": guessErrorReason(playErr),
			"slot":   slot,
			"name":   p.Name,
		})
		return
	}
	gen := s.setLastPlay(playURL, p.Name, p.Art, mime)
	s.recentNoteCard("radio", p.StreamURL, p.Name, p.Art, p.StreamURL, "", p.Homepage) // #135
	name, art := p.Name, p.Art
	go s.verifyRecall(gen, recallStart, playURL, func(ctx context.Context, _ bool) {
		if mime != "" {
			_ = s.renderer.PlayURLMime(ctx, playURL, name, art, mime)
		} else {
			_ = s.renderer.PlayURL(ctx, playURL, name, art)
		}
	}, nil)
	writeJSON(w, http.StatusOK, map[string]any{"status": "playing", "slot": slot, "name": p.Name})
}

// verifyRecall confirms the box reached a playing state ON THE EXPECTED STREAM
// shortly after a recall and re-issues the play a few times if not. Fixes the
// "first press after a reboot does nothing, second press works" race
// (box/go-librespot not ready yet) without any latency on the happy path (the
// initial play already ran).
//
// expectedLocation is the box-side URL this recall pushed (e.g.
// boxurl.StreamSlot(slot)). "Busy" alone used to count as success, which hid
// exactly the #252 failure where a racing wake-resume replaced a just-issued
// recall with the PREVIOUS station: the box was playing, just the wrong
// stream. Now a busy box on a different location is re-issued like a silent
// one. Empty expectedLocation keeps the busy-only check.
//
// retry receives lastAttempt=true only on the final try. Spotify uses it to
// re-point the box without a full re-Play on the early tries (a re-Play
// reshuffles and restarts the track), reserving the disruptive re-Play for the
// last-resort recovery. This is the same policy the hardware recall settled on
// in v0.7.4 (cmd/agent verifySpotifyPlaying); routing both through one contract
// keeps the soft and hardware paths from drifting again.
//
// gen is the recall generation returned by this recall's setLastPlay and
// started is when the recall was issued. They feed the stand-down decision
// (verifyStandDownReason): a newer play supersedes this verify (two rapid
// preset presses used to spawn dueling verifies that ping-ponged the stations
// for ~15s), and a deliberate user stop/pause/power-off after `started` ends
// it (the retry used to audibly restart a station the user had just stopped,
// and re-armed the transport of a box the user had just powered off - the
// #197 vector the hardware verifies were already guarded against).
func (s *Server) verifyRecall(gen uint64, started time.Time, expectedLocation string, retry func(ctx context.Context, lastAttempt bool), working func() bool) {
	const attempts = 3
	for attempt := 1; attempt <= attempts; attempt++ {
		time.Sleep(5 * time.Second)
		if reason := s.recallStandDownReason(gen, started); reason != "" {
			s.logger.Info("recall verify: standing down", "reason", reason, "attempt", attempt)
			return
		}
		// working() is a source-specific "it is already fine" signal checked
		// before the box now_playing state. For Spotify it reports whether the
		// box is pulling the Ogg stream: now_playing flaps while the box
		// attaches, and a re-issue would reshuffle + restart the track (the
		// audible abort + UI play/stop/play flicker). Don't retry when working.
		if working != nil && working() {
			s.NoteBoxHealthy()
			return
		}
		location, busy := s.boxPlayLocation()
		if busy && recallLocationMatches(expectedLocation, location) {
			s.NoteBoxHealthy()
			return
		}
		// The box just rejected the source because it is not signed in (1036).
		// Re-pushing the same UPnP source only flaps it and can wedge the box
		// (Michal's ST300). A forced re-login was already kicked off by the
		// not-logged-in signal; stand this retry loop down and let the next user
		// recall land once the box is signed back in - no thrashing, no manual
		// "re-pair" step for the user.
		if s.recentLoginError() {
			s.logger.Warn("recall verify: box reported not-logged-in; standing down the retry (a re-login was triggered) instead of re-pushing and risking a wedge",
				"attempt", attempt)
			return
		}
		if busy {
			s.logger.Warn("recall verify: box is playing a different stream than this recall pushed, re-issuing",
				"attempt", attempt, "expected", expectedLocation, "playing", location)
		} else {
			s.logger.Warn("recall did not reach playing, retrying", "attempt", attempt)
		}
		// Serialize the re-push with every other box command: the Bose
		// firmware mishandles concurrent writes (see boxCmdMu), and a retry
		// SOAP call landing during a volume PUT re-created exactly the
		// collision the mutex was added to prevent.
		s.boxCmdMu.Lock()
		// Re-decide under the lock: a newer play or a user stop may have
		// landed while this retry waited for it, and pushing now would
		// clobber that newer intent.
		if reason := s.recallStandDownReason(gen, started); reason != "" {
			s.boxCmdMu.Unlock()
			s.logger.Info("recall verify: standing down", "reason", reason, "attempt", attempt)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		retry(ctx, attempt == attempts)
		cancel()
		s.boxCmdMu.Unlock()
	}
	s.logger.Warn("recall still not playing after retries")
	// Wedge detection (#power-cycle hint): decide whether this exhaustion
	// looks like the box rather than the station, and count it.
	s.NoteRecallExhausted()
}

// recallStandDownReason gathers the live inputs for verifyStandDownReason: the
// current recall generation and the last user-stop / power-off stamps.
func (s *Server) recallStandDownReason(gen uint64, started time.Time) string {
	s.lastPlayMu.Lock()
	curGen := s.recallGen
	s.lastPlayMu.Unlock()
	s.lastUserStopMu.Lock()
	userStop := s.lastUserStop
	s.lastUserStopMu.Unlock()
	s.standbyStopMu.Lock()
	standbyStop := s.lastStandbyStop
	s.standbyStopMu.Unlock()
	return verifyStandDownReason(gen, curGen, started, userStop, standbyStop)
}

// verifyStandDownReason decides whether an in-flight recall verify must abort
// instead of re-pushing its stream, and why (empty = keep verifying). Pure so
// the policy is unit-testable:
//
//   - A newer play bumped the recall generation: this verify is superseded.
//     Re-pushing would yank the box off the stream the user chose afterwards.
//   - The box was powered off after the recall started (#197): a re-push
//     re-arms the transport and scm ST20 firmware switches back on with it.
//   - The user deliberately stopped/paused after the recall started: the
//     retry would audibly restart what they just stopped. Compared against
//     the recall start, NOT the rolling userStopWindow: that 6s window
//     expires between the verify's 5/10/15s ticks, so a stop at t=1s would
//     have been forgotten by the t=10s retry.
//
// Stops that PRECEDE the recall never stand it down: recalling a preset right
// after stopping another station is a normal action whose verify must run.
func verifyStandDownReason(recallGen, curGen uint64, recallStart, lastUserStop, lastStandbyStop time.Time) string {
	switch {
	case curGen != recallGen:
		return "superseded by a newer play"
	case !lastStandbyStop.IsZero() && lastStandbyStop.After(recallStart):
		return "box powered off after the recall (#197)"
	case !lastUserStop.IsZero() && lastUserStop.After(recallStart):
		return "user stopped playback after the recall"
	}
	return ""
}

// recallLocationMatches reports whether the box's now-playing location is the
// stream a recall pushed. Lenient on missing data: with no expectation or no
// readable location it returns true, so a now_playing read hiccup can never
// turn the verify into a retry storm. Only a POSITIVE "the box is on a
// different URL" counts as a mismatch (#252).
func recallLocationMatches(expected, nowLocation string) bool {
	if expected == "" || nowLocation == "" {
		return true
	}
	return nowLocation == expected
}

// xmlAttrUnescape decodes the predefined XML entities the box emits in
// now_playing attribute values (location="...&amp;..."), so the recall verify
// compares real URLs, not encodings.
func xmlAttrUnescape(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'")
	return r.Replace(s)
}

// boxPlayLocation reads now_playing once and reports the ContentItem location
// the box is tuned to plus whether it is busy (playing, buffering or paused).
// Best-effort: ("", false) when the box cannot be read; verifyRecall treats an
// unreadable location as a match, so this can only ever ADD a justified retry,
// never a spurious one.
func (s *Server) boxPlayLocation() (location string, busy bool) {
	if s.boxHost == "" {
		return "", false
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get("http://" + s.boxHost + ":8090/now_playing")
	if err != nil {
		return "", false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	resp.Body.Close()
	body := string(b)
	if m := reNowPlayLocation.FindStringSubmatch(body); m != nil {
		location = xmlAttrUnescape(m[1])
	}
	busy = strings.Contains(body, "PLAY_STATE") || strings.Contains(body, "BUFFERING_STATE") || strings.Contains(body, "PAUSE_STATE")
	return location, busy
}

// setLastPlay records the box-facing stream + metadata for the auto-re-push. A
// fresh play resets the re-push state (rePushes=0, failed=false), so a stream
// that was previously declared dead gets a clean slate when the user plays it
// again. It returns the new recall generation; a recall passes it to
// verifyRecall so the verify stands down as soon as a later play bumps it.
func (s *Server) setLastPlay(boxURL, title, art, mime string) uint64 {
	now := time.Now()
	s.lastPlayMu.Lock()
	s.lastPlay = &lastPlayInfo{boxURL: boxURL, title: title, art: art, mime: mime, ts: now}
	s.recallGen++
	gen := s.recallGen
	// A fresh play is an explicit user "play this" and may well be a different
	// stream: re-arm the auto-resume crash-loop guard (#381).
	s.resumeAttempts = 0
	s.lastResumeAt = time.Time{}
	s.lastPlayMu.Unlock()
	// Persist so the power-on resume survives an agent restart over a long
	// standby (#119). Plays are user-paced, so this is a rare, cheap NAND write.
	s.persistLastPlay(boxURL, title, art, mime, now, 0, time.Time{})
	return gen
}

// persistedLastPlay is the on-NAND shape of the last-played stream (the resume
// target). The runtime re-push counters are deliberately omitted: a reload is a
// fresh start. The auto-resume guard counters (#381) ARE persisted: they exist
// precisely to survive the reboot a crashing resume causes.
type persistedLastPlay struct {
	BoxURL string    `json:"boxURL"`
	Title  string    `json:"title"`
	Art    string    `json:"art"`
	Mime   string    `json:"mime"`
	TS     time.Time `json:"ts"`
	// ResumeAttempts / LastResumeAt: the auto-resume crash-loop guard state
	// (#381, see resume_guard.go). Absent in files from older agents, which
	// unmarshals to the zero values = guard disarmed.
	ResumeAttempts int       `json:"resumeAttempts,omitempty"`
	LastResumeAt   time.Time `json:"lastResumeAt,omitzero"`
}

// persistLastPlay writes the resume target to NAND atomically (temp + rename),
// so a power loss mid-write cannot leave a torn file. Best-effort, no-op without
// a configured path.
func (s *Server) persistLastPlay(boxURL, title, art, mime string, ts time.Time, resumeAttempts int, lastResumeAt time.Time) {
	if s.lastPlayPath == "" {
		return
	}
	b, err := json.Marshal(persistedLastPlay{BoxURL: boxURL, Title: title, Art: art, Mime: mime, TS: ts,
		ResumeAttempts: resumeAttempts, LastResumeAt: lastResumeAt})
	if err != nil {
		return
	}
	tmp := s.lastPlayPath + ".tmp"
	// Warn, not Debug: on a full NAND this is the ONLY trace of why the
	// power-on resume has no station to bring back after the next standby
	// (#119, ST30 with a full /mnt/nv).
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		s.logger.Warn("last-play persist failed", "err", err)
		return
	}
	if err := os.Rename(tmp, s.lastPlayPath); err != nil {
		s.logger.Warn("last-play rename failed", "err", err)
		_ = os.Remove(tmp)
	}
}

// loadLastPlay restores the persisted resume target at agent start, so a power
// press after an overnight standby (which often restarts the agent) still brings
// the last station back instead of leaving the box on its native "Preset not
// assigned" (#119 Klaus). Best-effort; absent/corrupt is just no resume target.
func (s *Server) loadLastPlay() {
	if s.lastPlayPath == "" {
		return
	}
	b, err := os.ReadFile(s.lastPlayPath)
	if err != nil {
		return
	}
	var p persistedLastPlay
	if json.Unmarshal(b, &p) != nil || p.BoxURL == "" || p.TS.IsZero() {
		return
	}
	s.lastPlayMu.Lock()
	s.lastPlay = &lastPlayInfo{boxURL: p.BoxURL, title: p.Title, art: p.Art, mime: p.Mime, ts: p.TS}
	// Restore the auto-resume guard count (#381): after a crash-caused reboot
	// this is what tells the automatic resume it is looping.
	s.resumeAttempts = p.ResumeAttempts
	s.lastResumeAt = p.LastResumeAt
	s.lastPlayMu.Unlock()
	s.logger.Info("last-play restored from NAND for power-on resume", "title", p.Title, "ageMin", int(time.Since(p.TS).Minutes()), "resumeAttempts", p.ResumeAttempts)
}

// NoteLastPlay records a stream the agent pushed to the box OUTSIDE the webui
// (the hardware preset recall in cmd/agent goes straight to the renderer). It
// lets the auto-re-push (#4) and the power-button wake-resume work for hardware
// presses too, which otherwise left lastPlay unset and the box un-resumable.
func (s *Server) NoteLastPlay(boxURL, title, art, mime string) {
	// A hardware recall is by definition a non-queue play (cmd/agent routes a
	// queue preset through RecallSlot, which never reaches here): drop any
	// active library queue so its watcher does not advance over the user's new
	// choice minutes later.
	s.stopQueue()
	s.setLastPlay(boxURL, title, art, mime)
}

// --- Recently played (#135) ---
//
// These record the user's listening history into the capped, debounced ring.
// They are deliberately the only box-side work the feature adds: each call is a
// cheap in-RAM append (Add does no I/O), reusing events the agent already
// processes (play handlers, the ICY title callback, the hardware-preset gabbo
// event). All are no-ops when the recent store is not wired.

// recentNoteCard records a user-chosen source (radio station, Spotify playlist,
// NAS file) as the start of a Recently-played card. For radio and Spotify it also
// remembers the card so the live track callbacks can hang tracks under it.
func (s *Server) recentNoteCard(source, key, name, art, url, account, homepage string) {
	if s.recent == nil || key == "" {
		return
	}
	s.recent.Add(recent.Entry{Source: source, CardKey: key, CardName: name, CardArt: art, CardURL: url, Account: account, Homepage: homepage})
	s.recentMu.Lock()
	switch source {
	case "radio":
		s.recentRadioCard = recentCardCtx{key: key, name: name, art: art, url: url, homepage: homepage}
	case "spotify":
		// A fresh card resets the de-dup so the playlist's first song is recorded
		// even if its name happens to match the previous card's last track.
		s.recentSpotifyCard = recentCardCtx{key: key, name: name, art: art, url: url, account: account}
	}
	s.recentMu.Unlock()
}

// recentNoteQueueCard records a DLNA folder played as an auto-advancing queue as
// the start of a "library" (upnp) Recently-played card, and remembers it so each
// track the queue pushes is hung under it (like radio ICY tracks under a station).
// The replay target is the first track's URL; clicking the card re-plays it.
func (s *Server) recentNoteQueueCard(key, name, art, url string) {
	s.recentMu.Lock()
	s.recentQueueCard = recentCardCtx{key: key, name: name, art: art, url: url}
	s.recentMu.Unlock()
	s.recentNoteCard("upnp", key, name, art, url, "", "")
}

// recentNoteQueueTrack hangs the track the play-queue just started under the
// current folder card. No-op until a queue card has been recorded.
func (s *Server) recentNoteQueueTrack(track string) {
	if s.recent == nil || track == "" {
		return
	}
	s.recentMu.Lock()
	c := s.recentQueueCard
	s.recentMu.Unlock()
	if c.key == "" {
		return
	}
	s.recent.Add(recent.Entry{Source: "upnp", CardKey: c.key, CardName: c.name, CardArt: c.art, CardURL: c.url, Track: track})
}

// recentClearQueueCard forgets the active folder card so later tracks (a single
// play, a new radio station) are not mis-attributed to a folder that has stopped.
func (s *Server) recentClearQueueCard() {
	s.recentMu.Lock()
	s.recentQueueCard = recentCardCtx{}
	s.recentMu.Unlock()
}

// recentNoteRadioTrack hangs a live radio track (the ICY StreamTitle) under the
// current radio card. No-op until a radio card has been recorded.
func (s *Server) recentNoteRadioTrack(track string) {
	if s.recent == nil || track == "" {
		return
	}
	s.recentMu.Lock()
	c := s.recentRadioCard
	s.recentMu.Unlock()
	if c.key == "" {
		return
	}
	s.recent.Add(recent.Entry{Source: "radio", CardKey: c.key, CardName: c.name, CardArt: c.art, CardURL: c.url, Track: track, Homepage: c.homepage})
}

// NoteRecentSpotifyTrack hangs a live Spotify song under the current Spotify card.
// Wired to the Spotify manager's onTrack hook. No-op until a Spotify card has been
// recorded (a track that starts outside an STR recall has no card to attach to).
func (s *Server) NoteRecentSpotifyTrack(track, artist string) {
	if s.recent == nil || track == "" {
		return
	}
	s.recentMu.Lock()
	c := s.recentSpotifyCard
	s.recentMu.Unlock()
	if c.key == "" {
		return
	}
	// Store artist-first ("Artist - Title"). The view's formatTrack reads " - " as
	// the Shoutcast "Artist - Title" order, so this renders the artist on the lead
	// line exactly like radio, instead of mislabelling the song title as artist.
	full := track
	if artist != "" {
		full = artist + " - " + track
	}
	s.recent.Add(recent.Entry{Source: "spotify", CardKey: c.key, CardName: c.name, CardArt: c.art, CardURL: c.url, Track: full, Account: c.account})
}

// NoteRecentPreset records a hardware-preset press into Recently-played. The
// agent's gabbo handler calls it because the hardware recall goes straight to
// the renderer, bypassing the webui play handlers.
func (s *Server) NoteRecentPreset(p presets.Preset) {
	if p.Type == "spotify" {
		s.recentNoteCard("spotify", p.URI, p.Name, p.Art, p.URI, p.Account, "")
		return
	}
	s.recentNoteCard("radio", p.StreamURL, p.Name, p.Art, p.StreamURL, "", p.Homepage)
}

// handleRecent serves this box's recently-played ring (#135), oldest-first. The
// desktop app reads every box's ring and does the merge + source-card grouping;
// the box just returns its capped list.
func (s *Server) handleRecent(w http.ResponseWriter, r *http.Request) {
	if s.recent == nil {
		if r.Method == http.MethodDelete {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": 0})
			return
		}
		writeJSON(w, http.StatusOK, []recent.Entry{})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.recent.All())
	case http.MethodDelete:
		// DELETE /api/recent?all=1            -> clear the whole ring (explicit)
		// DELETE /api/recent?cardKey=X&ts=Y   -> remove the ONE card at that ts
		// "all=1" is REQUIRED to clear: a delete-card request with a missing/stale
		// cardKey must never fall through to wiping everything (that was the bug
		// where deleting one entry removed all older ones). Flush() now so the
		// change survives an immediate reboot instead of waiting out the debounce.
		removed := 0
		switch {
		case r.URL.Query().Get("all") == "1":
			s.recent.Clear()
		case r.URL.Query().Get("cardKey") != "":
			removed = s.recent.DeleteCardAt(r.URL.Query().Get("cardKey"), r.URL.Query().Get("ts"))
		default:
			http.Error(w, "specify all=1 to clear, or cardKey+ts to remove one card", http.StatusBadRequest)
			return
		}
		if err := s.recent.Flush(); err != nil {
			s.logger.Warn("recent: flush after delete failed", "err", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ResumeLastPlay re-pushes the last stream STR played: the power-on resume. On
// the SoundTouch firmware a power press emits NO powerStateUpdated frame; the box
// only reports a source change out of STANDBY and, because it can no longer play
// its UPNP selection itself, restores it as INVALID_SOURCE + DO_NOT_RESUME. boxws
// surfaces that as OnPowerWake (verified live on a Portable/taigan 2026-06-13:
// powerStateUpdated never appears, a real power press = STANDBY -> INVALID_SOURCE
// + DO_NOT_RESUME). Power-on is an explicit "play it again", so this overrides
// the user-stop the power-off STOP_STATE set and clears the failed/attempt state
// so even a previously dead stream gets one fresh try.
//
// The same DO_NOT_RESUME wake is also what a SELF-wake produces (a box pulled out
// of standby by its stereo pair / zone, which made Klaus' box start playing on
// its own, 2026-06-12). The two are indistinguishable on the wire, so the guard
// is zone membership: a standalone box can only leave standby by a user press, so
// it resumes; a box that is part of a zone does NOT (see boxInZone). Plus the
// per-box opt-out below.
func (s *Server) ResumeLastPlay() {
	if s.renderer == nil {
		return
	}
	// Per-box opt-out (default on). The few users who want silence on power-on
	// turn this off; everyone else gets the last station back, like Bose did.
	if !s.resumeOnPowerOnEnabled() {
		s.logger.Info("wake resume: power-on resume disabled for this box, not resuming")
		return
	}
	// Crash-loop guard (#381): when the last attempts all ended in a reboot
	// before playback stabilised, the persisted stream itself is the prime
	// suspect for crashing the box. Stand down BEFORE the wake call below so a
	// guarded box is not even woken. A manual play re-arms via setLastPlay.
	if s.autoResumeBlocked() {
		s.logger.Warn("wake resume: standing down, the last automatic resumes each ended in a reboot before playback stabilised (crash-loop guard, #381); press a preset key or start playback from the app to re-enable")
		return
	}
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	// Split the two no-resume reasons so a diagnostic shows which one hit, and
	// allow a much longer age than the old 12h: a power press after an overnight
	// (or weekend) standby still expects the last station back, like Bose did
	// (#119 Klaus). The persisted lastPlay (NAND) survives the agent restart that
	// a long standby often triggers, so this age is what actually gates now.
	if lp == nil {
		s.lastPlayMu.Unlock()
		s.logger.Info("wake resume: no last station remembered (no resume target on NAND yet)")
		return
	}
	if age := time.Since(lp.ts); age >= resumeMaxAge {
		s.lastPlayMu.Unlock()
		s.logger.Info("wake resume: last station too old to resume", "ageHours", int(age.Hours()), "maxHours", int(resumeMaxAge.Hours()))
		return
	}
	lp.failed = false
	lp.rePushes = 0
	boxURL, title, art, mime, capturedTS := lp.boxURL, lp.title, lp.art, lp.mime, lp.ts
	s.lastPlayMu.Unlock()

	go func() {
		// Let the power transition settle so the box's reported state is
		// unambiguous before we decide. The DO_NOT_RESUME wake that triggers this
		// fires on a power-on, but the box can also reach standby again right
		// after, so settle then read the real state.
		time.Sleep(2 * time.Second)

		// scm power-off bounce guard (#197). Some ST20 (scm) firmware oscillates
		// UPNP->STANDBY->UPNP on a power-off, and the STANDBY->UPNP restore arrives
		// as the SAME DO_NOT_RESUME frame a genuine power-ON does, so this
		// OnPowerWake can fire from a power-OFF. If HandleEnterStandby just cleared
		// the transport for this box (a UPNP->STANDBY we saw moments ago), this
		// "wake" is that bounce, not a user switching the box on: stand down and,
		// crucially, leave the user-stop HandleEnterStandby set intact (do NOT fall
		// through to the clear below) so neither this resume nor the parallel auto
		// re-push pulls the box back up. Without this, a power-off taken while the
		// stream was still buffering left the box briefly reporting BUFFERING/PLAY
		// at this settle point, so the standby && !busy guard below missed and STR
		// woke the box back on (deqw, ST20 #197: "turns back on and continues
		// playing music").
		if s.standbyStoppedRecently() {
			s.logger.Info("wake resume: standby bounce detected (box just powered off STR's source), not resuming (#197)")
			return
		}

		// Klaus guard: a box pulled out of standby by its stereo pair / zone emits
		// the SAME DO_NOT_RESUME wake as a user power press, so the frame cannot
		// tell them apart. But a STANDALONE box can only leave standby by a user
		// pressing power, so it is safe to resume; a box that is part of a zone is
		// not (the pair may have woken it), so stand down. This is what lets the
		// resume default to ON without bringing back Klaus' spontaneous playback.
		// Checked live (authoritative) rather than from cached zone events.
		if s.boxInZone() {
			s.logger.Info("wake resume: box is in a zone / stereo pair, not auto-resuming (self-wake guard)")
			return
		}

		// Power-off bounce guard for boxes where the UPnP-source standby did not
		// arm standbyStoppedRecently above. A rhino ST10 reports a power-off as a
		// gabbo STOP_STATE (-> NoteUserStop) rather than the UPNP->STANDBY that
		// HandleEnterStandby keys on, so the #197 guard missed it and this wake
		// resumed a box the user had just switched off (Svagerka, ST10: "off does
		// not stick, playback resumes within seconds"). Discriminator: a power-OFF
		// is preceded by a deliberate stop within the last few seconds (the box was
		// playing when off was pressed), whereas a genuine power-ON follows a box
		// that was already stopped/off, so no fresh stop precedes it. If a user stop
		// landed within userStopWindow, treat this wake as the power-off bounce and
		// stand down, keeping the user-stop intact so the parallel auto re-push does
		// not pull the box back up either. A real power-on hours later has no recent
		// stop, so it still resumes.
		if s.userStoppedRecently() {
			s.logger.Info("wake resume: a deliberate stop immediately preceded this wake (power-off bounce), not resuming")
			return
		}

		// Discriminate a power-OFF from a power-ON wake (#105): after the user
		// presses power OFF the box settles in standby; after a wake it has
		// already left standby (the DO_NOT_RESUME we are reacting to is the box
		// restoring its selection while awake). Never wake a box the user just
		// turned off, and leave the user-stop suppression intact so the parallel
		// auto-re-push does not pull it back up either.
		if standby, busy := s.boxPlayState(); standby && !busy {
			s.logger.Info("wake resume: box is in standby (deliberate power-off), not resuming")
			return
		}

		// Genuine power-on: an explicit "play it again", so drop the recent
		// user-stop (the power-off emitted STOP_STATE) that would otherwise
		// suppress this resume and the auto-re-push.
		s.lastUserStopMu.Lock()
		s.lastUserStop = time.Time{}
		s.lastUserStopMu.Unlock()

		if s.boxHost != "" {
			wctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			_ = boxcli.WakeAndWait(wctx, s.boxHost, 6*time.Second, s.logger)
			cancel()
		}
		// Box already playing? Then this DO_NOT_RESUME came from STR waking the
		// box for a preset press (not a bare power-on), and that press already
		// started playback. Skip so we do not re-push the same stream and cause a
		// double-start hiccup.
		if _, busy := s.boxPlayState(); busy {
			s.logger.Info("wake resume: box already playing, no resume needed")
			return
		}
		s.boxCmdMu.Lock()
		defer s.boxCmdMu.Unlock()
		// A play/recall that raced this resume may have been holding boxCmdMu
		// while the checks above ran: it is the very request whose ensureBoxReady
		// wake fired this DO_NOT_RESUME, and its stream had not started when the
		// busy check above looked (so the check missed it). By the time we get
		// the lock that recall has updated lastPlay, and pushing the target we
		// captured at entry would replace the user's NEW station with the
		// PREVIOUS one (#252: recall slot 5 right after a wake ended up back on
		// slot 4). Re-read lastPlay under the lock and stand down when it moved.
		s.lastPlayMu.Lock()
		cur := s.lastPlay
		s.lastPlayMu.Unlock()
		if resumeIsStale(boxURL, capturedTS, cur) {
			s.logger.Info("wake resume: a newer play started while this resume waited, standing down",
				"captured", boxURL, "current", lastPlayURL(cur))
			return
		}
		// Count the attempt and persist BEFORE the push: if this stream crashes
		// the box, the incremented count is what the next boot loads (#381).
		s.noteAutoResumeAttempt()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var err error
		if mime != "" {
			err = s.renderer.PlayURLMime(ctx, boxURL, title, art, mime)
		} else {
			err = s.renderer.PlayURL(ctx, boxURL, title, art)
		}
		if err != nil {
			s.logger.Warn("wake resume: play failed", "err", err, "url", boxURL)
			return
		}
		s.logger.Info("wake resume: resumed last stream after power-on", "url", boxURL, "title", title)
	}()
}

// resumeIsStale reports whether a resume target captured at the START of a
// wake-resume / reconnect-recovery has been overtaken by a newer play by the
// time the box command lock was finally acquired. Those paths capture lastPlay,
// then wait (settle sleep, state probes, boxCmdMu); a user play/recall that
// held the lock meanwhile has called setLastPlay, so the capture is the
// PREVIOUS station and pushing it would clobber the one the user just started
// (#252). A same-URL entry with a newer timestamp is stale too: the newer play
// already (re)started that stream, and a second push only causes a
// double-start hiccup.
func resumeIsStale(capturedURL string, capturedTS time.Time, current *lastPlayInfo) bool {
	if current == nil {
		return true // nothing to resume anymore
	}
	return current.boxURL != capturedURL || !current.ts.Equal(capturedTS)
}

// lastPlayURL is a nil-safe accessor for the stand-down log line.
func lastPlayURL(lp *lastPlayInfo) string {
	if lp == nil {
		return ""
	}
	return lp.boxURL
}

// RecoverAfterReconnect re-pushes the last stream when the gabbo WebSocket
// reconnects and finds the box awake but stuck on an STR selection it is not
// playing. This recovers the lost first press after a deep/overnight standby
// (#183): the box wakes and emits the preset/now-selection frame before STR has
// reconnected (the reconnect backoff had grown while the box was unreachable),
// so OnPresetSelected never runs and the display shows "service unavailable"
// until a second press.
//
// It reuses the power-on resume's safeguards so it can only ever resume, never
// surprise: it honours the per-box opt-out, stands down inside a zone (a
// stereo-pair self-wake looks identical on the wire), suppresses a deliberate
// user stop, and acts only when the box is awake-and-idle on an STR source
// (boxSelectionStuck). A routine idle reconnect (box in standby), a box already
// playing, or a box on a native source (AUX/Bluetooth) is a no-op. Unlike
// ResumeLastPlay it does NOT clear the user-stop: a reconnect is not the explicit
// "play it again" a real power press is.
func (s *Server) RecoverAfterReconnect() {
	if s.renderer == nil {
		return
	}
	if !s.resumeOnPowerOnEnabled() {
		return
	}
	// Crash-loop guard (#381). This path is the one a crash-caused REBOOT
	// takes: the agent starts, the gabbo WS connects, the box sits awake with
	// the stuck STR selection the crash left behind, and without this guard
	// the recovery re-pushes the very stream that crashed the box - forever.
	if s.autoResumeBlocked() {
		s.logger.Warn("reconnect recovery: standing down, the last automatic resumes each ended in a reboot before playback stabilised (crash-loop guard, #381); press a preset key or start playback from the app to re-enable")
		return
	}
	// A deliberate user stop must survive a WS reconnect: without this guard a
	// reconnect resumed the last stream the user had stopped.
	if s.userStoppedRecently() {
		s.logger.Info("reconnect recovery: user stopped recently, not resuming")
		return
	}
	// A gabbo reconnect can land mid power-off bounce (a flapping scm box keeps
	// reconnecting). If STR saw this box drop UPNP->STANDBY moments ago, stand down
	// so the reconnect recovery does not re-push a URI the firmware bounces on (#197).
	if s.standbyStoppedRecently() {
		s.logger.Info("reconnect recovery: box just dropped to standby, not resuming (#197)")
		return
	}
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	// The strict age gate depends on WHAT the box is stuck on and is applied
	// below once the selection is known (reconnectResumeWindow); here only the
	// generous outer bound shared with the power-on resume applies.
	if lp == nil || time.Since(lp.ts) >= resumeMaxAge {
		s.lastPlayMu.Unlock()
		return
	}
	boxURL, title, art, mime, capturedTS := lp.boxURL, lp.title, lp.art, lp.mime, lp.ts
	s.lastPlayMu.Unlock()

	go func() {
		// Let the wake settle so the box's reported state is unambiguous before
		// we decide (the box can flip through transient states right after a
		// reconnect/wake).
		time.Sleep(2 * time.Second)
		if s.boxInZone() {
			s.logger.Info("reconnect recovery: box in a zone / stereo pair, standing down (self-wake guard)")
			return
		}
		stuck, selLoc := s.boxStuckSelection()
		if !stuck {
			return // asleep, already playing, or on a native source: nothing to recover
		}
		// Only resume when the box is stuck on OUR last stream. A non-empty
		// selection location that does NOT reference lastPlay means the box
		// moved on (e.g. a failed Spotify preset recall left it on a different
		// selection); resurrecting the old stream then surprised the user by
		// starting an unrelated preset (#ST30 preset 1 self-started, 2026-07-10).
		// An empty INVALID_SOURCE (the box restored STR's UPNP source but could
		// not self-activate it, #183) carries no location and is the genuine
		// recovery target, gated by the freshness + user-stop guards above.
		if selLoc != "" && !sameStream(selLoc, boxURL) {
			s.logger.Info("reconnect recovery: box is stuck on a different selection than our last stream, not resuming",
				"selection", selLoc, "lastPlay", boxURL)
			return
		}
		if age := time.Since(capturedTS); age >= reconnectResumeWindow(selLoc) {
			s.logger.Info("reconnect recovery: last stream too old for this recovery, not resuming",
				"age", age, "window", reconnectResumeWindow(selLoc))
			return
		}
		s.boxCmdMu.Lock()
		defer s.boxCmdMu.Unlock()
		// Same anti-clobber guard as the power-on resume (#252): a play/recall
		// that held boxCmdMu while this recovery waited has updated lastPlay, and
		// pushing the entry capture now would replace the user's new stream.
		s.lastPlayMu.Lock()
		cur := s.lastPlay
		s.lastPlayMu.Unlock()
		if resumeIsStale(boxURL, capturedTS, cur) {
			s.logger.Info("reconnect recovery: a newer play started while this recovery waited, standing down",
				"captured", boxURL, "current", lastPlayURL(cur))
			return
		}
		// Count the attempt and persist BEFORE the push: if this stream crashes
		// the box, the incremented count is what the next boot loads (#381).
		s.noteAutoResumeAttempt()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var err error
		if mime != "" {
			err = s.renderer.PlayURLMime(ctx, boxURL, title, art, mime)
		} else {
			err = s.renderer.PlayURL(ctx, boxURL, title, art)
		}
		if err != nil {
			s.logger.Warn("reconnect recovery: play failed", "err", err, "url", boxURL)
			return
		}
		s.logger.Info("reconnect recovery: resumed last stream after WS reconnect", "url", boxURL, "title", title)
	}()
}

// boxSelectionStuck reports whether the box is awake with an STR selection it is
// not playing: the state a lost preset-press / power-on wake leaves behind (the
// box restored STR's UPNP source as INVALID_SOURCE and shows "service
// unavailable"). It is the trigger for RecoverAfterReconnect (#183) and is
// deliberately narrow: a box in standby, a box already playing/paused, or a box
// on a native source (AUX, Bluetooth) all return false so the recovery never
// fights them.
func (s *Server) boxSelectionStuck() bool {
	stuck, _ := s.boxStuckSelection()
	return stuck
}

// nowPlayingLocationRe pulls the ContentItem location out of a now_playing body.
var nowPlayingLocationRe = regexp.MustCompile(`location="([^"]*)"`)

// boxStuckSelection reports whether the box is awake with a stuck STR selection
// (see boxSelectionStuck's contract) AND the location of that selection, so the
// caller can tell whether the box is stuck on OUR last stream (resume it) or on
// something else (leave it). An empty location means the box carries no
// ContentItem (a bare INVALID_SOURCE), the #183 recovery target.
func (s *Server) boxStuckSelection() (stuck bool, location string) {
	if s.boxHost == "" {
		return false, ""
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get("http://" + s.boxHost + ":8090/now_playing")
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	body := string(b)
	if strings.Contains(body, "STANDBY") {
		return false, "" // box asleep: a routine idle reconnect, nothing to recover
	}
	if strings.Contains(body, "PLAY_STATE") || strings.Contains(body, "BUFFERING_STATE") || strings.Contains(body, "PAUSE_STATE") {
		return false, "" // already playing/paused
	}
	// Only recover an STR-owned selection (UPNP) or the box's failed
	// self-activation of it (INVALID_SOURCE). A native source the user picked
	// (AUX, BLUETOOTH, ...) is left alone.
	if !strings.Contains(body, `source="UPNP"`) && !strings.Contains(body, "INVALID_SOURCE") {
		return false, ""
	}
	if m := nowPlayingLocationRe.FindStringSubmatch(body); m != nil {
		location = m[1]
	}
	return true, location
}

// sameStream reports whether two box-facing stream URLs point at the same STR
// stream, comparing by path (the host differs between the master's own loopback
// form http://127.0.0.1:8888/stream/2 and the box-visible :17008 form). Used to
// tell whether the box's stuck selection is our last stream.
func sameStream(a, b string) bool {
	pa, pb := streamPath(a), streamPath(b)
	return pa != "" && pa == pb
}

func streamPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Path
}

// reconnectResumeMaxAge bounds how old the last stream may be when the box is
// stuck on OUR OWN stream location after a WS blip: that shape means the box
// dropped the stream mid-playback, so it only counts as "just playing" for a
// few minutes. Deliberately short - resurrecting an hours-old stopped stream
// surprised users (#ST30 self-start, 2026-07-10).
const reconnectResumeMaxAge = 10 * time.Minute

// reconnectResumeWindow returns how old the last stream may be for the
// reconnect recovery to resume it, depending on the box's stuck selection. A
// bare INVALID_SOURCE with no location is the wake signature (#183): after a
// deep standby the box often reboots, the wake frame beats the rebooted
// agent's first gabbo connect, and this recovery is then the ONLY path that
// brings the last station back - so it keeps the power-on resume's generous
// window ("abends aus, morgens an", #119). A selection stuck on our own stream
// location is a mid-playback drop and only resumes when it happened minutes
// ago (see reconnectResumeMaxAge).
func reconnectResumeWindow(stuckSelectionLocation string) time.Duration {
	if stuckSelectionLocation == "" {
		return resumeMaxAge
	}
	return reconnectResumeMaxAge
}

// defaultResumeOnPowerOnPath is the NAND flag file for the per-box power-on
// resume opt-out. Absent or "1" means on (the default), "0" means off.
const defaultResumeOnPowerOnPath = "/mnt/nv/streborn/resume-on-power-on"

// defaultDisplayTrackPath is the NAND flag file for the per-box "show the live
// radio track on the speaker display" opt-in. Absent or "0" means off (the
// default); "1" means on.
const defaultDisplayTrackPath = "/mnt/nv/streborn/display-track-on-box"

// minDisplayPushInterval is the shortest gap between two ICY display pushes. Each
// push re-buffers the box (a brief audio gap), and some stations flip the
// StreamTitle between the song and promo/talk lines every few seconds, so the
// push is rate-limited to keep those gaps occasional rather than constant.
const minDisplayPushInterval = 12 * time.Second

// displayTrackEnabled reports whether "show the live radio track on the speaker
// display" is enabled for this box. Default OFF: the flag file is absent on a
// fresh install and only an explicit "1"/"true"/"on"/"yes" turns it on. The env
// override STR_ICY_DISPLAY=1 still forces it on for dev/testing.
func (s *Server) displayTrackEnabled() bool {
	if icyDisplayPushEnabled() {
		return true
	}
	path := s.displayTrackPath
	if path == "" {
		path = defaultDisplayTrackPath
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(string(b))) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// defaultDisplayTrackModePath is the NAND file for WHAT the display push shows:
// "both" (Artist - Title, the default), "title", or "artist".
const defaultDisplayTrackModePath = "/mnt/nv/streborn/display-track-mode"

// displayTrackMode returns the configured display content: "both" | "title" |
// "artist". Default "both" (absent/unrecognized file).
func (s *Server) displayTrackMode() string {
	path := s.displayTrackPath
	if path == "" {
		path = defaultDisplayTrackPath
	}
	b, err := os.ReadFile(modePathFor(path))
	if err != nil {
		return "both"
	}
	switch m := strings.ToLower(strings.TrimSpace(string(b))); m {
	case "title", "artist", "both":
		return m
	default:
		return "both"
	}
}

// modePathFor derives the mode file path next to the enabled-flag file, so a test
// override of displayTrackPath keeps both files together.
func modePathFor(enabledPath string) string {
	if enabledPath == "" || enabledPath == defaultDisplayTrackPath {
		return defaultDisplayTrackModePath
	}
	return enabledPath + ".mode"
}

// splitStreamTitle splits an ICY StreamTitle into (artist, title). The separator
// is the de-facto tell across stations, matching the app's Recently-played view:
// " - " is "Artist - Title", " / " is "Title / Artist" (flipped). No separator:
// the whole string is the title, artist empty.
func splitStreamTitle(s string) (artist, title string) {
	s = strings.TrimSpace(s)
	for _, sep := range []string{" / ", " - ", " – ", " — "} {
		if i := strings.Index(s, sep); i > 0 && i+len(sep) < len(s) {
			left, right := strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(sep):])
			if left == "" || right == "" {
				continue
			}
			if sep == " / " {
				return right, left // "Title / Artist"
			}
			return left, right // "Artist - Title"
		}
	}
	return "", s
}

// displayTrackText applies the configured display mode to a raw ICY StreamTitle,
// returning what should appear on the speaker display. "both" keeps the full
// string; "title"/"artist" use the split, falling back to the full string when
// the split has no artist (no separator), so the display is never blank.
func (s *Server) displayTrackText(streamTitle string) string {
	switch s.displayTrackMode() {
	case "title":
		if _, title := splitStreamTitle(streamTitle); title != "" {
			return title
		}
	case "artist":
		if artist, _ := splitStreamTitle(streamTitle); artist != "" {
			return artist
		}
	}
	return strings.TrimSpace(streamTitle)
}

// boxInZone reports whether the speaker is currently part of a multiroom zone or
// stereo pair, read live from the box (/getZone). It is the power-on resume's
// self-wake guard: a standalone box can only leave standby by a user power press
// (safe to resume), but a zone member may have been woken by its pair (Klaus'
// spontaneous playback), so it must not auto-resume. On a read error it returns
// false (treat as standalone): a missing zone read should not silently disable
// the feature for the standalone majority, and the per-box opt-out is the
// backstop for the rare paired box that also fails the read.
func (s *Server) boxInZone() bool {
	if s.boxHost == "" {
		return false
	}
	// Persisted membership first: a box we recorded as part of a zone or stereo
	// pair must stand down from power-on resume even if the live /getZone races a
	// zone that is still forming (it legitimately reads empty mid-handshake) or
	// the read errors. This closes the self-wake gap where a member woken by its
	// pair resumed because the live read came back empty. Fail-safe direction:
	// silence beats spontaneous playback.
	if s.zones != nil {
		if _, ok := s.zones.Get(); ok {
			return true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	z, err := boxapi.New(s.boxHost).GetZone(ctx)
	if err != nil {
		s.logger.Info("wake resume: zone read failed, treating box as standalone", "err", err)
		return false
	}
	return z.Master != ""
}

// resumeOnPowerOnEnabled reports whether "resume the last station on power-on"
// is enabled for this box. Default ON: the flag file is absent on a fresh
// install, and only an explicit opt-out ("0" / "false" / "off" / "no") disables
// it. An unreadable file also defaults to on (fallback-first).
func (s *Server) resumeOnPowerOnEnabled() bool {
	path := s.resumeOnPowerOnPath
	if path == "" {
		path = defaultResumeOnPowerOnPath
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(string(b))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// userStopWindow is how long after a deliberate user stop the auto-re-push
// stays suppressed. A stop causes a proxy disconnect, and maybeRePush waits 2s
// before deciding; this window comfortably covers that gap plus the lag of a
// gabbo STOP_STATE frame, while being short enough that a genuine box-side drop
// later in the session is still resumed.
const userStopWindow = 6 * time.Second

// NoteUserStop records that the user deliberately stopped (or paused) playback.
// The auto-re-push checks this so a wanted stop is not immediately undone.
// Called from the STR Stop/Pause endpoints and from the gabbo STOP_STATE hook.
func (s *Server) NoteUserStop() {
	s.lastUserStopMu.Lock()
	s.lastUserStop = time.Now()
	s.lastUserStopMu.Unlock()
}

// ClearUserStop cancels a recorded user-stop so a manual resume re-enables the
// guarded auto-re-push immediately instead of staying suppressed for the
// userStopWindow.
func (s *Server) ClearUserStop() {
	s.lastUserStopMu.Lock()
	s.lastUserStop = time.Time{}
	s.lastUserStopMu.Unlock()
}

// userStoppedRecently reports whether a deliberate stop happened within
// userStopWindow.
func (s *Server) userStoppedRecently() bool {
	s.lastUserStopMu.Lock()
	defer s.lastUserStopMu.Unlock()
	return !s.lastUserStop.IsZero() && time.Since(s.lastUserStop) < userStopWindow
}

// standbyBounceWakeWindow is how long after HandleEnterStandby cleared the
// transport (a power-off STR saw as UPNP->STANDBY) a following DO_NOT_RESUME
// "wake" is treated as the scm power-off BOUNCE rather than a genuine power-on.
// On the ST20 (scm) the STANDBY->UPNP restore arrives within ~200 ms of the
// power-off and ResumeLastPlay then settles for 2 s before it decides, so the
// window must comfortably exceed that settle. See standbyStoppedRecently.
const standbyBounceWakeWindow = 6 * time.Second

// standbyStoppedRecently reports whether STR saw this box's UPnP source drop to
// STANDBY (a power-off) within standbyBounceWakeWindow. ResumeLastPlay, maybeRePush
// and RecoverAfterReconnect use it to stand down on the scm power-off bounce (a
// DO_NOT_RESUME / reconnect / stream-drop that follows a power-OFF) instead of
// re-waking a box the user just switched off.
func (s *Server) standbyStoppedRecently() bool {
	s.standbyStopMu.Lock()
	defer s.standbyStopMu.Unlock()
	return !s.lastStandbyStop.IsZero() && time.Since(s.lastStandbyStop) < standbyBounceWakeWindow
}

// RecentlyPoweredOff reports whether STR saw this box's UPnP source drop to
// STANDBY within the bounce window. Exported for the agent's hardware-preset
// recall verify (cmd/agent), which must abort its re-push retries when the user
// powered the box off mid-recall instead of treating standby as "not playing yet"
// and re-pushing the stream (#197).
func (s *Server) RecentlyPoweredOff() bool { return s.standbyStoppedRecently() }

// noteStandbyStop arms the power-off suppression seen on a UPNP->STANDBY drop: it
// refreshes lastStandbyStop (the #197 standbyStoppedRecently window) and records a
// user-stop, independent of whether the caller then clears the transport. This
// decoupling is deliberate: the zone / disabled guards in HandleEnterStandby
// govern only the transport-clear, but the suppression must stay armed for all
// three wake paths (ResumeLastPlay, maybeRePush, RecoverAfterReconnect) regardless.
func (s *Server) noteStandbyStop() {
	s.standbyStopMu.Lock()
	s.lastStandbyStop = time.Now()
	s.standbyStopMu.Unlock()
	s.NoteUserStop()
}

// standbyStopDebounce bounds the resume-suppression burst detection for the rapid
// UPNP<->STANDBY oscillation a power-off produces on some ST20 (scm) firmware.
const standbyStopDebounce = 4 * time.Second

// standbyClearMinGap rate-limits the transport clear during a power-off bounce so
// the ~170 ms UPNP<->STANDBY oscillation re-clears the transport a few times
// (covering a clear that lost the race) without flooding the box with SOAP calls.
const standbyClearMinGap = 500 * time.Millisecond

// standbyBounceFixEnabled gates the #197 mitigation. Default on; set
// STR_STANDBY_STOP=0 on the box to disable it if it ever regresses, without an
// OTA (run.sh exports the agent's environment).
func standbyBounceFixEnabled() bool {
	return os.Getenv("STR_STANDBY_STOP") != "0"
}

// HandleEnterStandby reacts to the box's own UPnP source dropping to STANDBY
// (a power-off), seen over gabbo. On some ST20 (scm) firmware the box then
// oscillates STANDBY->UPNP and switches itself back on because STR's UPnP
// transport still has a URI loaded and is treated as the active source, so the
// speaker "cannot be switched off" until several presses (#197, confirmed in a
// diagnostic: UPNP->STANDBY->UPNP within ~170 ms, repeated). STR clears the
// transport so the firmware has nothing to bounce back to.
//
// Conservative by design: it only runs when STR's own source (UPNP) was active,
// never when the box is in a zone (a mirror/slave legitimately re-selects UPNP),
// and is debounced so the flip does not issue a burst of Stops. Stopping a box
// the user just powered off matches their intent, so the blast radius is small.
func (s *Server) HandleEnterStandby() {
	if !standbyBounceFixEnabled() || s.renderer == nil {
		return
	}

	// Arm the suppression signal on EVERY observed power-off, BEFORE the zone guard
	// that only governs the transport-clear. The stamp (read by ResumeLastPlay's
	// standbyStoppedRecently #197 guard) and the user-stop (read by maybeRePush /
	// RecoverAfterReconnect) must stay armed even when we go on to skip the clear
	// (a zoned box), or all three wake paths would be free to re-wake a box the
	// user just powered off. Refreshed on every flip so a rapid second press keeps
	// the window alive (the reporter's "needs multiple presses").
	s.noteStandbyStop()

	if s.boxInZone() {
		return // a zone slave/master mirror re-selects UPNP on purpose; leave its transport
	}

	// Re-issue the clear on each flip of the oscillation, not just the first: a
	// single Stop loses the ~170 ms STANDBY->UPNP race and the firmware re-selects
	// STR's still-loaded URI. A short min-gap keeps a fast flap from flooding the
	// box with SOAP calls.
	s.standbyStopMu.Lock()
	doClear := s.lastStandbyClear.IsZero() || time.Since(s.lastStandbyClear) >= standbyClearMinGap
	if doClear {
		s.lastStandbyClear = time.Now()
	}
	s.standbyStopMu.Unlock()
	if !doClear {
		return
	}

	// A power-off is a deliberate stop: drop any queue so it does not fight the
	// standby, then Stop and EMPTY the transport URI so the firmware has nothing to
	// bounce back to (Stop alone leaves the URI loaded and the box re-selects it).
	s.stopQueue()
	s.logger.Info("standby bounce: box powered off STR's UPnP source, stopping + clearing the transport URI so it stays off (#197)")
	ctx, cancel := context.WithTimeout(s.queueCtx(), 5*time.Second)
	defer cancel()
	if err := s.renderer.Stop(ctx); err != nil {
		s.logger.Debug("standby bounce: transport stop returned (expected if already off)", "err", err)
	}
	if err := s.renderer.ClearURI(ctx); err != nil {
		s.logger.Debug("standby bounce: clear transport URI returned", "err", err)
	}
}

// HandleStreamDisconnect is called by the stream proxy when the Bose renderer
// closes a stream. When the upstream was healthy (so the box dropped it, not
// the source) it conservatively tries to resume, which fixes the renderer
// dropping a long stream on its own (radio stops after ~11 min, no STR error).
func (s *Server) HandleStreamDisconnect(upstreamErr error) {
	if upstreamErr != nil {
		return // upstream failed; not a box-side drop, leave it alone
	}
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	// Coalesce. Skip when there is no recent stream, the stream was already
	// declared dead, or a resume is already in flight. A dead/moved URL fires a
	// disconnect on EVERY failed resume; without this latch each one spawned a
	// fresh maybeRePush goroutine, producing the dozens-per-second runaway that
	// starved :8888 (v0.7.5).
	if lp == nil || time.Since(lp.ts) >= 6*time.Hour || lp.failed || lp.rePushInFlight {
		s.lastPlayMu.Unlock()
		return
	}
	lp.rePushInFlight = true
	s.lastPlayMu.Unlock()
	go s.maybeRePush()
}

// maybeRePush resumes the last stream, but only when it is safe: it waits a
// moment (a user power-off reaches STANDBY within ~1-2 s, so this tells "user
// turned it off" from "renderer dropped the stream while the box stays on"),
// then re-pushes only if the box is on and idle (not standby, not playing, not
// paused). A windowed counter caps retries so a genuinely failing stream is not
// looped forever.
func (s *Server) maybeRePush() {
	// Release the in-flight latch on exit so a later genuine drop can re-arm
	// (HandleStreamDisconnect refuses to spawn a second goroutine until then).
	defer func() {
		s.lastPlayMu.Lock()
		if s.lastPlay != nil {
			s.lastPlay.rePushInFlight = false
		}
		s.lastPlayMu.Unlock()
	}()

	// Exponential backoff keyed on how many attempts this stream already took:
	// 2s, 2s, 4s, 8s, 16s (capped at 30s). A dead/moved URL drops the moment it
	// is re-pushed, so without this the resume loop spun dozens of times per
	// second; the backoff spaces the (few) attempts out instead. The first wait
	// also serves the original purpose of telling a user power-off (box reaches
	// STANDBY in ~1-2s) from a renderer drop while the box stays on.
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	if lp == nil {
		s.lastPlayMu.Unlock()
		return
	}
	attempt := lp.rePushes
	s.lastPlayMu.Unlock()
	backoff := 2 * time.Second
	if attempt > 1 {
		backoff = time.Duration(1<<uint(attempt)) * time.Second
	}
	if backoff > 30*time.Second {
		backoff = 30 * time.Second
	}
	time.Sleep(backoff)

	// A deliberate user stop (STR Stop/Pause, or the box/remote stop button seen
	// over gabbo) must hold. Genuine box-side drops carry no such stop and resume.
	if s.userStoppedRecently() {
		s.logger.Info("re-push: user stopped deliberately, not resuming")
		return
	}
	// A power-off STR saw as UPNP->STANDBY (HandleEnterStandby) must also hold the
	// re-push: on the scm bounce the box is flipping STANDBY<->UPNP and a re-push
	// here would hand the firmware a fresh URI to switch back on with (#197).
	if s.standbyStoppedRecently() {
		s.logger.Info("re-push: box just powered off (standby bounce), not resuming (#197)")
		return
	}
	standby, busy := s.boxPlayState()
	if standby {
		s.logger.Info("re-push: box went to standby, not resuming (treated as user power-off)")
		return
	}
	if busy {
		// Recovered (playing/paused again, or the user switched). Reset the
		// attempt counter so a later genuine drop starts a fresh backoff window.
		s.lastPlayMu.Lock()
		if s.lastPlay != nil {
			s.lastPlay.rePushes = 0
		}
		s.lastPlayMu.Unlock()
		return
	}

	s.lastPlayMu.Lock()
	lp = s.lastPlay
	if lp == nil {
		s.lastPlayMu.Unlock()
		return
	}
	if lp.rePushes >= maxRePushes {
		// Hard stop. The stream keeps dropping (a dead/moved radio-browser URL,
		// 503, etc.). Mark it dead so no further disconnect re-arms it; only a
		// fresh play (setLastPlay) clears this. This is the fix for the runaway
		// that re-armed forever and starved the control port.
		lp.failed = true
		url := lp.boxURL
		s.lastPlayMu.Unlock()
		s.logger.Warn("re-push: stream keeps dropping, giving up for good (likely a dead/moved URL); not retrying until a new play",
			"url", url, "attempts", maxRePushes)
		return
	}
	lp.rePushes++
	boxURL, title, art, mime, n := lp.boxURL, lp.title, lp.art, lp.mime, lp.rePushes
	s.lastPlayMu.Unlock()

	s.logger.Info("re-push: box dropped the stream while idle, resuming", "url", boxURL, "attempt", n, "max", maxRePushes)
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var err error
	if mime != "" {
		err = s.renderer.PlayURLMime(ctx, boxURL, title, art, mime)
	} else {
		err = s.renderer.PlayURL(ctx, boxURL, title, art)
	}
	if err != nil {
		s.logger.Warn("re-push failed", "err", err, "url", boxURL)
	}
}

// icyDisplayPushEnabled reports whether STR should push live ICY StreamTitle
// updates to the box's now-playing/display by re-issuing SetAVTransportURI
// mid-stream. Off by default: re-setting the URI can make some renderers
// re-buffer (an audible gap on every track change), so this stays behind an
// env flag until verified on the target hardware. Set STR_ICY_DISPLAY=1.
func icyDisplayPushEnabled() bool {
	return os.Getenv("STR_ICY_DISPLAY") == "1"
}

// HandleStreamTitle pushes a freshly parsed radio StreamTitle to the box so it
// appears in now-playing / on the display, by re-issuing the stream STR last
// told the box to play with the new title as DIDL metadata. The URL stays the
// stable proxy URL, only the title changes.
//
// Gated behind STR_ICY_DISPLAY: a URI re-set may cost an audio gap on some
// renderers (see icyDisplayPushEnabled), so we keep the safe default of
// surfacing the title only in the app (via /api/stream/title) until the
// mid-stream re-set is verified on real hardware. Wired from the stream proxy.
func (s *Server) HandleStreamTitle(title string) {
	// Recently-played (#135): record the live radio track under the current
	// source card, regardless of whether the on-box ICY display push is enabled
	// (this callback fires on every ICY title change either way).
	s.recentNoteRadioTrack(title)
	// Remember the live title so enabling the push / changing its mode can show
	// the CURRENT track immediately (see pushDisplayNow), not only the next one.
	if title != "" {
		s.lastPlayMu.Lock()
		s.lastICYTitle = title
		s.lastPlayMu.Unlock()
	}
	if !s.displayTrackEnabled() || s.renderer == nil || title == "" {
		return
	}
	// Rate-limit: re-buffering the box on every StreamTitle flip (song <-> promo)
	// would gap the audio constantly. Skip if we pushed within the last window.
	s.lastPlayMu.Lock()
	throttled := !s.lastDisplayPush.IsZero() && time.Since(s.lastDisplayPush) < minDisplayPushInterval
	s.lastPlayMu.Unlock()
	if throttled {
		return
	}
	s.pushDisplayTitle(title)
}

// setDisplayText re-issues the current stream's now-playing metadata with shown
// as the on-display title, keeping the stream URL / art / mime. It is the shared
// box write behind both the ICY title push and the revert-to-default path. This
// re-buffers the box (a brief audio gap), so callers gate it. Updates the
// debounce stamp. No-op if nothing is playing.
func (s *Server) setDisplayText(shown string) {
	if s.renderer == nil || shown == "" {
		return
	}
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	if lp == nil {
		s.lastPlayMu.Unlock()
		return
	}
	boxURL, art, mime := lp.boxURL, lp.art, lp.mime
	s.lastDisplayPush = time.Now()
	s.lastPlayMu.Unlock()
	if boxURL == "" {
		return
	}
	// Serialise against other box commands (re-push, play) so a title update
	// cannot interleave with a stream switch mid-SOAP.
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var err error
	if mime != "" {
		err = s.renderer.SetURIMime(ctx, boxURL, shown, art, mime)
	} else {
		err = s.renderer.SetURI(ctx, boxURL, shown, art)
	}
	if err != nil {
		s.logger.Warn("display push failed", "err", err, "shown", shown)
		return
	}
	s.logger.Info("display push", "shown", shown)
}

// pushDisplayTitle re-issues the now-playing metadata so the configured display
// text (artist / title / both, applied to rawTitle) appears on the speaker.
// Callers gate it: HandleStreamTitle debounces, the enable / mode-change path
// pushes once immediately.
func (s *Server) pushDisplayTitle(rawTitle string) {
	shown := s.displayTrackText(rawTitle)
	if shown == "" {
		return
	}
	s.setDisplayText(shown)
}

// pushDisplayNow immediately shows the current track on the speaker display,
// bypassing the debounce. Used right after the user enables the feature or
// switches the artist/title/both mode, so the display updates at once instead of
// waiting for the next song change. No-op when disabled or nothing is playing.
func (s *Server) pushDisplayNow() {
	if !s.displayTrackEnabled() {
		return
	}
	s.lastPlayMu.Lock()
	cur := s.lastICYTitle
	s.lastPlayMu.Unlock()
	if cur != "" {
		s.pushDisplayTitle(cur)
	}
}

// pushDisplayDefault reverts the speaker display to its normal text (the station
// name STR set when it started playing) right after the user turns the artist/
// title push OFF, instead of leaving the last custom text on screen until the
// next song change. Gated on the box actually playing so a SetURI never wakes an
// idle speaker; the display only carries our custom text during radio playback.
func (s *Server) pushDisplayDefault() {
	if standby, busy := s.boxPlayState(); standby || !busy {
		return
	}
	s.lastPlayMu.Lock()
	title := ""
	if s.lastPlay != nil {
		title = s.lastPlay.title
	}
	s.lastPlayMu.Unlock()
	if title != "" {
		s.setDisplayText(title)
	}
}

// boxPlayState reads now_playing and reports whether the box is in standby and
// whether it is busy (playing, buffering or paused). It FAILS CLOSED: when the
// box host is unknown or the query keeps erroring it reports standby=true, so a
// caller that would otherwise wake or re-push the box stands down when it cannot
// confirm the box is awake. A SoundTouch 10 sends no power event on a standby
// press, so the only guard against STR resuming over a deliberate standby is
// this state check; a transient /now_playing error must not be read as "awake
// and idle" and trigger a resume. One quick retry first so a single hiccup does
// not abort a legitimate stream recovery (where the box really is awake+idle).
func (s *Server) boxPlayState() (standby, busy bool) {
	if s.boxHost == "" {
		return true, false
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(400 * time.Millisecond)
		}
		resp, err := cl.Get("http://" + s.boxHost + ":8090/now_playing")
		if err != nil {
			lastErr = err
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		resp.Body.Close()
		body := string(b)
		standby = strings.Contains(body, "STANDBY")
		busy = strings.Contains(body, "PLAY_STATE") || strings.Contains(body, "BUFFERING_STATE") || strings.Contains(body, "PAUSE_STATE")
		return standby, busy
	}
	// Could not read the box state: assume standby so we never resume/wake on an
	// uncertain state (silence beats spontaneous playback).
	s.logger.Warn("box play-state query failed, assuming standby (will not resume/re-push)", "err", lastErr)
	return true, false
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
	// A pause is also a deliberate "stop pulling" intent: the box stops reading
	// from the proxy, which fires the same disconnect path. Suppress the resume.
	s.NoteUserStop()
	if err := s.renderer.Pause(r.Context()); err != nil {
		// Pausing while the speaker is idle makes the box answer with a
		// UPnP "Action request came in wrong state" fault. Pause is an
		// idempotent intent: if there is nothing playing the desired
		// state already holds, so treat it as a no-op instead of
		// surfacing a raw SOAP fault to the user.
		if isWrongTransportState(err) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "not_playing"})
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// handleResume resumes playback from a UPnP PAUSED state with a plain
// AVTransport Play (what the Bose remote's own play/pause does), so a paused
// network-library track continues from its position instead of restarting.
// /api/play always re-pushes SetAVTransportURI, which restarts a finite track,
// and Pause/Stop were the only transport controls STR exposed, so a paused NAS
// track could not be resumed from the app (#202). If the box is no longer
// paused (it left PAUSED after a standby/timeout, surfacing as a "wrong state"
// fault), fall back to re-pushing the last stream.
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
	// A user-initiated resume cancels the deliberate-stop intent so the guarded
	// auto-re-push is allowed again for the rest of the session.
	s.ClearUserStop()
	if err := s.renderer.Play(r.Context()); err != nil {
		if isWrongTransportState(err) {
			// The box is no longer in PAUSED. Re-push the last stream so the user
			// still gets audio (from the start for a finite track).
			s.lastPlayMu.Lock()
			lp := s.lastPlay
			s.lastPlayMu.Unlock()
			if lp != nil {
				if perr := s.renderer.PlayURLMime(r.Context(), lp.boxURL, lp.title, lp.art, lp.mime); perr == nil {
					writeJSON(w, http.StatusOK, map[string]string{"status": "playing"})
					return
				}
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "not_playing"})
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "playing"})
}

// isWrongTransportState reports whether a UPnP AVTransport error was the
// box rejecting the action because the renderer is not in a state that
// allows it. Bose answers Pause/Stop with this when nothing is playing,
// using errorCode 501 and the text "Action request came in wrong state"
// (the AVTransport spec also defines 701 for the same situation).
func isWrongTransportState(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "wrong state") ||
		strings.Contains(msg, "<errorCode>501</errorCode>") ||
		strings.Contains(msg, "<errorCode>701</errorCode>")
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
	// Mark this as a deliberate stop BEFORE issuing it, so the disconnect the
	// stop triggers does not race the auto-re-push into restarting the stream.
	s.NoteUserStop()
	// A stop ends any active library queue (no auto-advance after the user stops).
	s.stopQueue()
	if err := s.renderer.Stop(r.Context()); err != nil {
		// Same idempotent treatment as Pause: stopping an already-idle
		// box yields a "wrong state" UPnP fault that the user need not
		// see.
		if isWrongTransportState(err) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "not_playing"})
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// Verify the renderer actually stopped: a wedged renderer ACKs Stop yet
	// keeps playing (observed live on a Portable, 2026-07-10 - transport
	// stayed PLAYING while the source machine sat at INVALID_SOURCE). One
	// re-issued Stop, then an honest answer, so callers can escalate (reboot
	// hint) instead of trusting a blind 200.
	if state, ok := s.verifyRendererStopped(r.Context()); !ok {
		s.logger.Warn("stop: renderer ignored Stop and keeps playing (control wedge, a reboot usually clears it)", "transportState", state)
		writeJSON(w, http.StatusOK, map[string]string{"status": "stopped", "renderer": "still-playing"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// verifyRendererStopped polls the renderer's transport state briefly after a
// Stop, re-issuing the Stop once if it still reports PLAYING. Returns the
// last observed state and whether the renderer left PLAYING. Best-effort: an
// unreadable state counts as stopped (no false alarms on boxes whose
// GetTransportInfo is flaky).
func (s *Server) verifyRendererStopped(ctx context.Context) (string, bool) {
	retried := false
	state := ""
	for i := 0; i < 4; i++ {
		time.Sleep(600 * time.Millisecond)
		tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		st, err := s.renderer.TransportState(tctx)
		cancel()
		if err != nil {
			return state, true
		}
		state = st
		if st != "PLAYING" {
			return st, true
		}
		if !retried {
			retried = true
			sctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			_ = s.renderer.Stop(sctx)
			cancel()
		}
	}
	return state, false
}

// handleAgentVersion returns the running stick agent version. Used by
// the desktop app to detect whether an update is due.
func (s *Server) handleAgentVersion(w http.ResponseWriter, _ *http.Request) {
	out := map[string]string{
		"version": agentVersion(),
		"build":   agentBuild(),
	}
	// Carry the box display name/model the agent knows so the desktop app
	// can label a flashed speaker even when its own cross-LAN /info probe
	// is momentarily slow (e.g. the busy window right after an OTA restart).
	if s.boxNameFn != nil {
		if name, model := s.boxNameFn(); name != "" {
			out["friendlyName"] = name
			if model != "" {
				out["model"] = model
			}
		}
	}
	// Report whether the go-librespot Spotify sidecar is deployed and which
	// content it is, so the desktop app can decide whether to push it over OTA
	// (it ships ~10 MB; we only want to send it when the box is missing it or
	// has a different build). The binary historically reached the box ONLY via
	// the stick->NAND boot sync, so a box that was ever only OTA-updated stayed
	// without it and Spotify silently never played (#45/#105, e.g. an OTA-only
	// SoundTouch 30). present/missing + the content hash drive that gate.
	if present, sha := goLibrespotStamp(); present {
		out["goLibrespot"] = "present"
		if sha != "" {
			out["goLibrespotSha256"] = sha
		}
		// Size of the deployed engine, so the desktop app's sidecar space
		// pre-flight can count a present (old) engine as reclaimable: the
		// sidecar write drops it before the new one lands, so an engine
		// UPDATE fits on a tight box even when the raw free figure says no.
		if fi, err := os.Stat(goLibrespotBinPath); err == nil {
			out["goLibrespotSizeBytes"] = strconv.FormatInt(fi.Size(), 10)
		}
	} else {
		out["goLibrespot"] = "missing"
	}
	// Advertise that this agent hot-swaps the Spotify engine live: it restarts
	// go-librespot in place right after a sidecar OTA write, so a freshly
	// delivered/updated engine is active without a box reboot. The desktop app
	// reads this to skip the post-delivery activation reboot it still needs for
	// older agents that bind the binary only at process start (#240).
	if s.spotifyReload != nil {
		out["engineHotSwap"] = "true"
	}
	// NAND headroom on the tiny (~31 MB) writable volume, so the desktop app can
	// see before an OTA whether the ~10 MB agent + sidecar will fit and warn
	// instead of pushing into a "no space left on device" failure (the stickless
	// SoundTouch 30 case where SSH is closed and the disk state was otherwise
	// invisible). Strings to match this endpoint's map[string]string shape.
	if total, avail, ok := diskFree(nandRoot); ok {
		out["nandTotalBytes"] = strconv.FormatInt(total, 10)
		out["nandFreeBytes"] = strconv.FormatInt(avail, 10)
	}
	// Wedged-control state (see wedge.go): the desktop app and the phone
	// remote read this to tell the user a power-cycle is needed.
	if status, since := s.BoxHealth(); status != "ok" {
		out["boxHealth"] = status
		out["boxHealthSinceSec"] = strconv.FormatInt(int64(time.Since(since).Seconds()), 10)
	} else {
		out["boxHealth"] = "ok"
	}
	// Two heads-up flags for the desktop app (#270), emitted only when there is
	// something to warn about so the common response stays small: a rival
	// SoundTouch tool's leftover files (they fight STR), and no STR-saved Wi-Fi
	// (the box only stays online with the stick/cable and strands the user on the
	// next cold boot).
	if mod := detectConflictingMod(); mod != "" {
		out["conflictingMod"] = mod
	}
	if wlanCredsWarningWarranted() {
		out["wlanCreds"] = "missing"
	}
	// The ON-DISK agent binary's hash (the running build is in version/build
	// above). Together they let the desktop app tell "the binary landed but
	// the box still runs the old version" (a durability rollback, #381) apart
	// from "the push never arrived", and stop re-pushing into a reboot loop.
	if sha := agentBinaryStamp(); sha != "" {
		out["agentBinarySha256"] = sha
	}
	// A failed tier-3 RAM-staged swap leaves a marker instead of rebooting
	// into a silently-old binary; surface it so the failure is visible on a
	// stickless box where nothing else is.
	if msg, err := os.ReadFile(swapFailMarker); err == nil && len(msg) > 0 {
		out["otaSwapFailed"] = strings.TrimSpace(string(msg))
	}
	writeJSON(w, http.StatusOK, out)
}

// agentBinNANDPath is the NAND path of the agent binary — the only binary a
// stickless boot ever executes, and the OTA write target.
const agentBinNANDPath = "/mnt/nv/streborn/bin/streborn-armv7l"

// agentBinShaCache memoizes the on-disk agent binary hash keyed by
// mtime+size, so the frequent version polls (every 2 s during an OTA) do not
// re-hash 12.5 MB on the weak box CPU. Re-hashes only when the file changes.
var agentBinShaCache struct {
	sync.Mutex
	mtime time.Time
	size  int64
	sha   string
}

// agentBinaryStamp returns the hex SHA256 of the agent binary on NAND, or ""
// when it cannot be read.
func agentBinaryStamp() string {
	fi, err := os.Stat(agentBinNANDPath)
	if err != nil {
		return ""
	}
	agentBinShaCache.Lock()
	defer agentBinShaCache.Unlock()
	if agentBinShaCache.sha != "" &&
		fi.ModTime().Equal(agentBinShaCache.mtime) &&
		fi.Size() == agentBinShaCache.size {
		return agentBinShaCache.sha
	}
	b, err := os.ReadFile(agentBinNANDPath)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	agentBinShaCache.mtime = fi.ModTime()
	agentBinShaCache.size = fi.Size()
	agentBinShaCache.sha = hex.EncodeToString(sum[:])
	return agentBinShaCache.sha
}

// goLibrespotBinPath is where the agent runs the Spotify sidecar from; the
// desktop OTA writes it here too. Kept in lock-step with cmd/agent's
// goLibrespotPath and usb-stick/run.sh's NAND copy.
const goLibrespotBinPath = "/mnt/nv/streborn/bin/go-librespot"

// goLibrespotStamp reports whether the go-librespot sidecar is deployed
// (>1 KB, i.e. a real binary not an empty stub) and the hex SHA256 of its
// contents. The hash lets the desktop app skip re-pushing the ~10 MB binary
// when the box already has the embedded build. It is cached in a sibling
// .sha256 marker so a version poll does not re-hash 10 MB on the weak box CPU
// every few minutes: the marker is trusted while it is at least as new as the
// binary; otherwise (absent, or the binary was replaced by a stick install
// that did not write a marker) it is computed once and cached. Best-effort:
// a present binary with an unreadable hash reports present with an empty sha,
// which the app treats as "push once" (correct and idempotent).
func goLibrespotStamp() (present bool, sha string) {
	fi, err := os.Stat(goLibrespotBinPath)
	if err != nil || fi.Size() < 1024 {
		return false, ""
	}
	marker := goLibrespotBinPath + ".sha256"
	if mfi, err := os.Stat(marker); err == nil && !mfi.ModTime().Before(fi.ModTime()) {
		if b, rerr := os.ReadFile(marker); rerr == nil {
			if h := strings.TrimSpace(string(b)); h != "" {
				return true, h
			}
		}
	}
	data, err := os.ReadFile(goLibrespotBinPath)
	if err != nil {
		return true, "" // present but hash unknown; app re-pushes once
	}
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	_ = os.WriteFile(marker, []byte(h), 0o644)
	return true, h
}

// handleAgentEnableSSH opens root SSH on the box on demand, so the desktop app
// can SSH in (to uninstall STR, or run diagnostics) WITHOUT a USB stick and
// WITHOUT the fragile :17000 marge-injection dance. The agent already runs here
// as root, so it just does what run.sh's ensure_sshd_running does at boot: touch
// the remote_services marker Bose's sshd init gates on, then start sshd. This is
// the clean app-first path for a box that ALREADY runs STR (the uninstall case):
// no marge check, no reboot, no autopair fight. The marker in /tmp is tmpfs, so
// SSH closes again on the next reboot - deliberately transient, since the
// uninstall's own final reboot returns the box to a stock, SSH-closed state.
// LAN-only.
func (s *Server) handleAgentEnableSSH(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "enable-ssh only allowed from LAN", http.StatusForbidden)
		return
	}
	// Bose's /etc/init.d/sshd only starts sshd when this marker is present.
	if err := os.WriteFile("/tmp/remote_services", nil, 0o644); err != nil {
		s.logger.Warn("enable-ssh: could not write remote_services marker", "err", err)
	}
	started := ensureSSHDRunning(s.logger)
	s.logger.Info("enable-ssh: on-demand SSH open requested", "sshdRunning", started)
	writeJSON(w, http.StatusOK, map[string]any{"sshd": started})
}

// sshdRunning reports whether an sshd process is alive (pidof sshd).
func sshdRunning() bool {
	out, _ := exec.Command("pidof", "sshd").Output()
	return strings.TrimSpace(string(out)) != ""
}

// ensureSSHDRunning starts sshd if it is not already up, mirroring run.sh's
// ensure_sshd_running: try the Bose init script (which needs the remote_services
// marker), then fall back to /usr/sbin/sshd directly. The init script exit code
// is untrustworthy (it prints "Not starting sshd" and still exit-0s), so success
// is decided by a real process check, not the exit code.
func ensureSSHDRunning(logger *slog.Logger) bool {
	if sshdRunning() {
		return true
	}
	if _, err := os.Stat("/etc/init.d/sshd"); err == nil {
		_ = exec.Command("/etc/init.d/sshd", "start").Run()
		if sshdRunning() {
			return true
		}
	}
	if _, err := os.Stat("/usr/sbin/sshd"); err == nil {
		_ = exec.Command("/usr/sbin/sshd").Run()
		if sshdRunning() {
			return true
		}
		logger.Warn("enable-ssh: /usr/sbin/sshd ran but no sshd process appeared")
		return false
	}
	logger.Warn("enable-ssh: no sshd init script and no /usr/sbin/sshd found")
	return false
}

// handleAgentUpdate receives a new stick agent binary, writes it
// atomically to /mnt/nv/streborn/bin/streborn-armv7l and restarts the
// agent. Body must be the raw ARM binary.
//
// On success the stick still returns 200 OK and then exits. The
// rc.local bootstrap starts the new agent.
func (s *Server) handleAgentUpdate(w http.ResponseWriter, r *http.Request) {
	body, ok := readUploadedELF(w, r)
	if !ok {
		return
	}
	const dst = agentBinNANDPath
	err := writeBinaryAtomic(dst, body)
	// Tier 2 (#270): a NAND that UBIFS parked read-only fails the write (or,
	// sneakier, fails every reclaim delete so the write path reports "no
	// space"). Probe, remount rw, retry once; if the volume stays protected,
	// the truthful error beats another opaque 507.
	if err != nil && (isReadOnlyFSErr(err) || !nandWritable()) {
		if remountNANDRW(s.logger) {
			err = writeBinaryAtomic(dst, body)
		} else {
			http.Error(w, errNANDReadOnly.Error(), http.StatusInsufficientStorage)
			return
		}
	}
	// Tier 3 (#270): the volume writes fine but genuinely cannot hold OLD + NEW
	// agent side by side even after the reclaim (small ST20 volumes). Stage the
	// new binary in RAM and let a detached helper swap it in after this process
	// exits, then reboot — peak NAND need drops to a single copy. Since the
	// optimistic-write change errInsufficientNAND means a REAL failed write
	// attempt (ENOSPC / short write), never a pessimistic statfs prediction, so
	// this tier only engages when the filesystem itself said no.
	if errors.Is(err, errInsufficientNAND) {
		if serr := s.stageAndSwapViaRAM(dst, body); serr == nil {
			writeJSON(w, http.StatusOK, map[string]string{
				"status": "ok",
				"action": "reboot",
				"mode":   "ram-staged",
			})
			go func() {
				// Give the 200 OK time to flush, then exit: the helper waits
				// for this PID before copying (the running binary is ETXTBSY
				// and its blocks are pinned until we are gone).
				time.Sleep(1500 * time.Millisecond)
				// Refresh a still-inserted stick BEFORE exiting so the boot
				// sync cannot revert this update (#381). Delaying our exit is
				// safe: the swap helper waits for this PID.
				refreshStickAgentBinary(body, s.logger)
				s.logger.Info("exiting for the RAM-staged binary swap; the helper reboots the box")
				os.Exit(0)
			}()
			return
		} else {
			s.logger.Warn("RAM-staged swap unavailable, reporting the space failure", "err", serr)
		}
	}
	if err != nil {
		http.Error(w, err.Error(), nandWriteHTTPStatus(err))
		return
	}

	// Safe-over-fast (#381): prove the binary is ON FLASH before telling the
	// app anything and before any reboot. The fsync inside writeBinaryAtomic
	// should make this a formality, but UBIFS + these boxes have burned us:
	// verify by re-reading past the page cache, retry the write once on a
	// mismatch, and refuse with a truthful error rather than reboot into the
	// old binary and let the app loop-push forever.
	if verr := verifyBinaryOnFlash(dst, body); verr != nil {
		s.logger.Warn("agent update: flash verify failed, rewriting once", "err", verr)
		if err := writeBinaryAtomic(dst, body); err != nil {
			http.Error(w, err.Error(), nandWriteHTTPStatus(err))
			return
		}
		if verr = verifyBinaryOnFlash(dst, body); verr != nil {
			s.logger.Error("agent update: flash verify failed twice, refusing to reboot", "err", verr)
			http.Error(w, "update did not persist to flash: "+verr.Error(), http.StatusInternalServerError)
			return
		}
	}

	// A verified write supersedes any earlier tier-3 swap failure.
	_ = os.Remove(swapFailMarker)
	s.logger.Info("agent update written and flash-verified, rebooting box for a clean post-OTA state", "size", len(body))
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"action": "reboot",
	})

	// Always reboot after an OTA rather than just self-restarting the
	// process. Jens 2026-06-01: a self-restart left the freshly-updated
	// box with no presets visible in the app — the boot-time preset push
	// and the leave-OOB full re-sync (cmd/agent reconcileOnce forceFull)
	// only run on a real boot, not on a live process restart; and OTA
	// replaces only the binary, so the NAND run.sh + rc.local otherwise
	// stay at the pre-OTA vintage (project_ota_only_replaces_binary). A
	// reboot makes the new binary self-deploy its matching run.sh/rc.local
	// AND re-run the preset reconcile from clean. Delay so the 200 OK
	// above flushes to the desktop app before the box drops off the LAN.
	go func() {
		time.Sleep(1500 * time.Millisecond)
		// Refresh a still-inserted stick BEFORE the reboot: run.sh's boot sync
		// copies the stick binary over NAND unconditionally, so a stale stick
		// would silently revert the binary just written to dst (#381). The
		// desktop app's SSH stick refresh covers this only when SSH is open;
		// this on-box write needs nothing. The reboot waits for it.
		refreshStickAgentBinary(body, s.logger)
		// Unconditional flush before the reboot. refreshStickAgentBinary syncs
		// only when a stick is mounted; on a stickless box (the #381 field
		// case) nothing else flushed this path before v0.9.7. Every other
		// reboot in the project already syncs first — this one now does too.
		_ = exec.Command("sync").Run()
		time.Sleep(2 * time.Second)
		s.logger.Info("post-OTA reboot")
		_ = dst // the new binary is in place at dst; the boot path runs it
		if err := exec.Command("reboot").Run(); err != nil {
			// reboot binary missing or refused — fall back to the detached
			// process self-restart so we at least run the new binary. The
			// sleep 70 covers the :8081/:9080 TIME_WAIT window (60 s
			// tcp_fin_timeout on this kernel) so the new binary does not
			// crash-loop on "address already in use" (seen 2026-05-17).
			s.logger.Error("post-OTA reboot failed, falling back to process self-restart", "err", err)
			quoted := make([]string, 0, len(os.Args))
			for _, a := range os.Args {
				quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", "'\\''")+"'")
			}
			shCmd := "sleep 70 && exec " + strings.Join(quoted, " ") + " >> /tmp/streborn-agent.log 2>&1"
			cmd := exec.Command("sh", "-c", shCmd)
			cmd.SysProcAttr = sysProcAttrSetsid()
			if serr := cmd.Start(); serr != nil {
				s.logger.Error("self-restart fallback also failed", "err", serr)
				return
			}
			time.Sleep(100 * time.Millisecond)
			os.Exit(0)
		}
	}()
}

// readUploadedELF reads and validates a raw ARM ELF binary POSTed to an OTA
// endpoint: LAN-only, size-bounded, ELF-magic checked. On any problem it writes
// the HTTP error response and returns ok=false. Shared by handleAgentUpdate and
// handleAgentSidecar so the two upload endpoints cannot drift on their guards.
func readUploadedELF(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if !requireMethod(w, r, http.MethodPost) {
		return nil, false
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "update only allowed from LAN", http.StatusForbidden)
		return nil, false
	}
	const maxSize = 30 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSize+1))
	if err != nil {
		http.Error(w, "read: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	if len(body) > maxSize {
		http.Error(w, "binary too big", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	if len(body) < 1024 {
		http.Error(w, "binary too small", http.StatusBadRequest)
		return nil, false
	}
	// ELF magic check.
	if body[0] != 0x7f || body[1] != 'E' || body[2] != 'L' || body[3] != 'F' {
		http.Error(w, "not an ELF binary", http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

// errInsufficientNAND is returned by writeBinaryAtomic when an actually
// attempted .new write (or its rename) failed with a real out-of-space error
// even after reclaiming regenerable junk. The OTA handlers map it to 507
// Insufficient Storage so the desktop app can tell "the box is full" apart
// from a generic write failure and show the inventory instead of a raw 500
// after a full upload (#ST30 Daniel), and handleAgentUpdate's RAM-staged
// tier 3 keys off it.
var errInsufficientNAND = errors.New("insufficient NAND space")

// nandWriteMargin is the slack required above the binary size before an atomic
// write is attempted: filesystem overhead plus a cushion so a borderline write
// does not ENOSPC mid-stream.
const nandWriteMargin = 512 * 1024

// engineStopHook stops the running go-librespot so the space-pressed OTA write can
// truly free the engine's NAND blocks before dropping it (an unlink of a running
// binary frees nothing). Set by WithSpotifyStop; nil in tests and when Spotify is
// not configured, in which case the reclaim falls back to a plain os.Remove.
var engineStopHook func() bool

// writeBinaryAtomic writes body to dst via a .new temp + rename so a partial
// write never becomes the live binary, creating the parent dir on a fresh box
// (first OTA after install has no /mnt/nv/streborn/bin yet). 0755 so the file
// is executable.
//
// The box's writable NAND (/mnt/nv) is tiny (~31 MB, shared with the Bose
// firmware), and the atomic write needs room for a SECOND full copy of the
// ~10 MB binary beside the live one. On a SoundTouch 30 that tipped /mnt/nv
// over and the OTA failed with "no space left on device" (Daniel, 2026-06-24),
// with no way to see what was eating the space because the box was stickless so
// SSH was closed. Two defences: (1) before writing, drop a stale .new from an
// earlier interrupted OTA (a half-written temp from a failed attempt otherwise
// eats the very headroom the retry needs, so every retry keeps failing until
// the next boot's run.sh cleanup_nand runs) and, if statfs predicts a shortage,
// reclaim obvious junk; (2) on failure, embed the NAND inventory (df + biggest
// entries + foreign-firmware dirs) in the error so the desktop app surfaces it
// verbatim and the user's report tells us whether the ST30 is genuinely tighter
// or is carrying leftovers from a previous custom firmware.
//
// The write itself is OPTIMISTIC: the statfs prediction steers the reclaim
// cascade but never refuses the write. UBIFS free space is deliberately
// pessimistic (it assumes incompressible data while the volume compresses
// transparently, ~1.5x on Go binaries; measured 2026-07-10), so its "no" is
// frequently wrong for this write while its "yes" is always safe. Only the
// filesystem's own verdict on the actual write (a real ENOSPC / short write)
// maps to errInsufficientNAND now.
func writeBinaryAtomic(dst string, body []byte) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	tmp := dst + ".new"
	// Drop this target's stale temp, then proactively reclaim regenerable junk on
	// EVERY OTA write (not only when already tight), so a previous failed
	// attempt's leftovers, a stale OTHER-binary .new, or oversized logs never eat
	// the headroom this write needs (#ST30 Daniel). reclaimNAND never touches Bose
	// files or the live binaries.
	_ = os.Remove(tmp)
	reclaimNAND()
	need := int64(len(body))
	engineStopped := false
	engineReclaim := ""
	predictedFull := false
	if !nandHasRoom(dir, need) {
		// Second-tier reclaim: the go-librespot Spotify engine (~16 MB) is the one
		// big regenerable block left on a nearly-full NAND (ST30, ~31 MB), and the
		// cheap reclaim above never touches it. Drop it so the agent .new fits
		// rather than failing the whole update (#119); the desktop app re-delivers
		// it after the reboot (EnsureSpotifyEngine, triggered by goLibrespot !=
		// "present"). Only runs when statfs predicts a shortage, so a roomy box
		// keeps its engine.
		//
		// Stop the engine FIRST: it is normally running during an OTA, and a plain
		// os.Remove of a running binary only unlinks the path while the kernel keeps
		// its blocks pinned until the process exits, so dropping it freed nothing and
		// the update still failed with "no space left" (#119). StopEngine kills it and
		// waits for exit so the ~16 MB actually frees; reclaimSpotifyEngine then drops
		// the (now-unused) binary.
		if engineStopHook != nil {
			engineStopped = engineStopHook()
		}
		engineReclaim = reclaimSpotifyEngine()
		// Re-check with patience: UBIFS updates its free-space accounting lazily
		// after a delete, and the engine's blocks release on process reap, so an
		// immediate statfs can still show the old figure. A field report (#270)
		// showed a 507 whose inventory still carried the engine with no way to
		// tell which step failed; the outcome of each step is now logged and
		// embedded in the error.
		fits := false
		for i := 0; i < 10; i++ {
			if nandHasRoom(dir, need) {
				fits = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		predictedFull = !fits
		slog.Info("OTA write: space-pressed reclaim ran",
			"engineStopped", engineStopped, "engineReclaim", engineReclaim, "fits", fits)
		if !fits {
			// Do NOT refuse: agent+engine measurably fit the tightest (ST20,
			// ~26.7 MB) NAND once compression is accounted for, and refusing on
			// the pessimistic figure is what kept those boxes un-updatable.
			_, avail, _ := diskFree(dir)
			slog.Info("OTA write: statfs still predicts no room after reclaim; attempting the write anyway (UBIFS under-reports free space for compressible data)",
				"needKB", need/1024, "availKB", avail/1024)
		}
	}
	if err := writeFileSynced(tmp, body, 0o755); err != nil {
		// A mid-stream ENOSPC leaves a truncated tmp; remove it so no partial
		// .new survives for the next attempt.
		_ = os.Remove(tmp)
		return classifyNANDWriteErr("write tmp", err, dir, need, engineStopped, engineReclaim, predictedFull)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return classifyNANDWriteErr("rename", err, dir, need, engineStopped, engineReclaim, predictedFull)
	}
	// fsync the parent directory so the rename itself reaches the UBIFS
	// journal. Without this the whole write + rename can sit in the page
	// cache, and the post-OTA reboot 1.5 s later rolls the file back to the
	// pre-OTA binary — the box then boots the OLD version byte-perfect while
	// the app keeps re-pushing forever (#381 meierchen006, cgb280).
	syncDir(dir)
	return nil
}

// writeFileSynced is os.WriteFile plus an fsync before close, so the data is
// on flash (not just in the page cache) when it returns. Every binary the OTA
// path writes is followed by a reboot soon after; an unsynced write on UBIFS
// simply does not survive that (#381).
func writeFileSynced(path string, body []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// syncDir fsyncs a directory so a just-completed rename is journaled.
// Best-effort: some filesystems/platforms refuse directory handles.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// verifyBinaryOnFlash forces the just-written binary out to flash and proves
// it survived: global sync, drop the page cache, re-read dst and compare its
// hash against what was uploaded. This is the same lesson run.sh's #302
// deploy learned — "the md5 reads back the bytes we just wrote from RAM
// cache … even when the flash writeback silently failed" — applied to the
// OTA path. Only called right before a reboot, so dropping the page cache is
// free. Returns nil when the on-flash bytes match.
func verifyBinaryOnFlash(dst string, body []byte) error {
	_ = exec.Command("sync").Run()
	// Best-effort: /proc/sys/vm/drop_caches needs root (the agent is root on
	// the box) but does not exist in tests.
	_ = os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0o200)
	got, err := os.ReadFile(dst)
	if err != nil {
		return fmt.Errorf("flash verify re-read: %w", err)
	}
	want := sha256.Sum256(body)
	have := sha256.Sum256(got)
	if want != have {
		return fmt.Errorf("flash verify: on-flash binary differs from the upload (%d vs %d bytes) — the NAND write did not persist", len(got), len(body))
	}
	return nil
}

// isNoSpaceErr reports whether err is a REAL filesystem out-of-space failure:
// ENOSPC (usually wrapped in fs.PathError -> fmt.Errorf chains) or a short
// write. This filesystem verdict is what the optimistic OTA write trusts, as
// opposed to the pessimistic statfs prediction that used to refuse up front.
func isNoSpaceErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.ENOSPC) ||
		errors.Is(err, io.ErrShortWrite) ||
		strings.Contains(err.Error(), "no space left")
}

// classifyNANDWriteErr turns a failed step of the atomic OTA write into the
// error the handlers act on: a real out-of-space failure maps to
// errInsufficientNAND (507 + inventory; handleAgentUpdate's RAM-staged tier 3
// keys off it), anything else stays a plain write error (500, or the EROFS
// tier 2 when it smells read-only). The message records that the write WAS
// actually attempted, so a field report can tell a genuine ENOSPC apart from
// the pre-flight refusals older agents issued.
func classifyNANDWriteErr(step string, err error, dir string, need int64, engineStopped bool, engineReclaim string, predictedFull bool) error {
	if !isNoSpaceErr(err) {
		return fmt.Errorf("%s: %w [NAND %s]", step, err, nandReportLine())
	}
	if engineReclaim == "" {
		engineReclaim = "reclaim not needed, statfs predicted room"
	}
	_, avail, _ := diskFree(dir)
	return fmt.Errorf("%w: the write was actually attempted and the filesystem refused it (%s: %v): need %dKB, statfs free %dKB after reclaim (%s; engine stop=%v; statfs predicted full=%v) [NAND %s]",
		errInsufficientNAND, step, err, need/1024, avail/1024, engineReclaim, engineStopped, predictedFull, nandReportLine())
}

// nandWriteHTTPStatus maps a writeBinaryAtomic error to an HTTP status: 507
// Insufficient Storage when the box is out of NAND (so the desktop app can tell a
// full box apart from a generic failure and surface the inventory), else 500.
func nandWriteHTTPStatus(err error) int {
	if errors.Is(err, errInsufficientNAND) {
		return http.StatusInsufficientStorage
	}
	return http.StatusInternalServerError
}

// --- NAND disk diagnostics -------------------------------------------------
//
// These mirror run.sh's nand_inventory + cleanup_nand on the agent so the same
// picture is available over plain HTTP, not just in the SSH-gated diagnostic
// bundle: in /api/agent/version (free/total, every poll), in /api/debug/state
// (full inventory), and embedded into the OTA failure error. A stickless box
// (SSH closed since v0.8.1) can then still reveal its disk state to the app.

const nandRoot = "/mnt/nv"
const strNANDDir = "/mnt/nv/streborn"

// diskFree lives in statfs_linux.go / statfs_other.go: the Linux build does a
// real statfs, other hosts report "unknown" so the package stays testable on
// dev machines.

// selfProxyPathRe matches the agent's own per-slot proxy path (/stream/1..6),
// which is the box-visible preset location and never a valid station origin.
var selfProxyPathRe = regexp.MustCompile(`^/stream/([1-6])$`)

// selfProxySlot reports whether raw points at this agent's own /stream/<slot>
// proxy and which slot it references. Heuristic: the per-slot proxy path plus
// either a loopback host or one of STR's own ports - a real station origin
// never matches all of that (#252).
func selfProxySlot(raw string) (int, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, false
	}
	m := selfProxyPathRe.FindStringSubmatch(u.Path)
	if m == nil {
		return 0, false
	}
	host, port := u.Hostname(), u.Port()
	if port != "8888" && port != "17008" && host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, true
}

// dirBytes is a du -s in bytes for path, best-effort: unreadable entries are
// skipped, never fatal. Cheap on the tiny NAND.
func dirBytes(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, fi os.FileInfo, err error) error {
		if err != nil || fi == nil {
			return nil
		}
		if !fi.IsDir() {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// isForeignNANDDir reports whether a top-level /mnt/nv dir is neither STR's nor
// one of Bose's, i.e. a candidate leftover from a third-party post-cloud tool
// or another custom firmware the owner ran before STR. Mirrors run.sh's
// nand_inventory freshness check.
func isForeignNANDDir(name string) bool {
	switch name {
	case "streborn", "nv", "lost+found":
		return false
	// Bose-native persistence STR must NOT flag as foreign: AWS IoT certs, the
	// Bluetooth Profile Manager store, the Wi-Fi supplicant state, Avahi/mDNS,
	// and the box's own logs. Before this a stock box wrongly listed these as
	// "foreign", which buried the real signal (a rival mod's marker files, see
	// detectConflictingMod) in noise.
	case "IoTCerts", "btpm", "wpa_supplicant", "avahi", "BoseLog":
		return false
	}
	l := strings.ToLower(name)
	if strings.Contains(l, "bose") || strings.Contains(l, "persistence") {
		return false
	}
	return true
}

// hasSavedWLANCreds reports whether STR persisted the box's Wi-Fi SSID+password
// to NAND (run.sh writes strNANDDir/wlan-creds so the box can rejoin Wi-Fi on a
// stick-free cold boot). False means STR has no saved Wi-Fi: the box only stays
// online while the stick or an ethernet cable is inserted, so it strands the user
// on the next cold boot. A user who set Wi-Fi up via the Bose app instead of
// STR's own Wi-Fi setup hits exactly this (#270). Best-effort.
func hasSavedWLANCreds() bool {
	fi, err := os.Stat(filepath.Join(strNANDDir, "wlan-creds"))
	return err == nil && fi.Size() > 0
}

// detectConflictingMod returns the rival cloud-free SoundTouch tool (chiefly
// AfterTouch, github.com/gesellix/Bose-SoundTouch) whose real artifacts sit on
// the box, or "" for an STR-only box. Two tools both redirecting the Bose
// cloud and both driving the OLED / Wi-Fi / presets fight each other and
// strand the box (flashing display, orange Wi-Fi, no playback, #270).
//
// v0.9.7: v0.9.6 keyed this on two marker files (test_oled_stop,
// bco_needs_factory_reset) believed to be AfterTouch's. Field diagnostics
// proved them Bose-native: healthy STR-only speakers carry them (and they
// survive every factory reset because /mnt/nv is never wiped, so the warned
// user could never clear the warning), while a box with REAL AfterTouch
// leftovers (an /mnt/nv/aftertouch directory) carried neither. Detection now
// keys on AfterTouch's actual footprint: its NAND directory, its resolv.conf
// override, and an rc.local hook mentioning it. Best-effort.
func detectConflictingMod() string {
	if fi, err := os.Stat(filepath.Join(nandRoot, "aftertouch")); err == nil && fi.IsDir() {
		return "AfterTouch"
	}
	if _, err := os.Stat(filepath.Join(nandRoot, "aftertouch.resolv.conf")); err == nil {
		return "AfterTouch"
	}
	// STR's own rc.local never references the rival tool, so a hook line in
	// the boot script is an unambiguous fingerprint.
	if b, err := os.ReadFile(filepath.Join(nandRoot, "rc.local")); err == nil &&
		strings.Contains(strings.ToLower(string(b)), "aftertouch") {
		return "AfterTouch"
	}
	return ""
}

// handleRemoveConflictingMod removes the leftovers of a rival cloud-free
// SoundTouch tool (AfterTouch) that clash with STR: its /mnt/nv/aftertouch
// directory, its resolv.conf override, and any aftertouch hook line in rc.local.
// It touches ONLY those artifacts, never the rest of /mnt/nv (which holds the
// box's own Wi-Fi/AirPlay/account persistence and STR's own streborn/ dir). The
// desktop app surfaces this as a one-click button so users never need SSH; a
// reboot afterwards fully clears the rival tool's already-running processes.
func (s *Server) handleRemoveConflictingMod(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	mod := detectConflictingMod()
	if mod == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "mod": "", "removed": []string{}})
		return
	}
	removed := []string{}
	// 1. The rival tool's NAND directory.
	dir := filepath.Join(nandRoot, "aftertouch")
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		if err := os.RemoveAll(dir); err != nil {
			http.Error(w, "could not remove aftertouch/: "+err.Error(), http.StatusInternalServerError)
			return
		}
		removed = append(removed, "aftertouch/")
	}
	// 2. Its resolv.conf override.
	resolv := filepath.Join(nandRoot, "aftertouch.resolv.conf")
	if _, err := os.Stat(resolv); err == nil {
		if err := os.Remove(resolv); err != nil {
			http.Error(w, "could not remove aftertouch.resolv.conf: "+err.Error(), http.StatusInternalServerError)
			return
		}
		removed = append(removed, "aftertouch.resolv.conf")
	}
	// 3. Any aftertouch hook line in rc.local. Rewrite it without those lines,
	// keeping every other line (STR's own boot hooks and Bose defaults) intact.
	rcl := filepath.Join(nandRoot, "rc.local")
	if b, err := os.ReadFile(rcl); err == nil && strings.Contains(strings.ToLower(string(b)), "aftertouch") {
		lines := strings.Split(string(b), "\n")
		kept := lines[:0]
		dropped := 0
		for _, ln := range lines {
			if strings.Contains(strings.ToLower(ln), "aftertouch") {
				dropped++
				continue
			}
			kept = append(kept, ln)
		}
		if dropped > 0 {
			if err := os.WriteFile(rcl, []byte(strings.Join(kept, "\n")), 0o755); err != nil {
				http.Error(w, "could not clean rc.local: "+err.Error(), http.StatusInternalServerError)
				return
			}
			removed = append(removed, fmt.Sprintf("rc.local (%d line)", dropped))
		}
	}
	_ = exec.Command("sync").Run()
	s.logger.Info("removed conflicting-mod leftovers", "mod", mod, "removed", removed)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "ok",
		"mod":           mod,
		"removed":       removed,
		"stillDetected": detectConflictingMod() != "",
	})
}

// wlanCredsWarningWarranted reports whether the "no Wi-Fi saved in STR"
// warning should fire: only when the creds file is missing on a chassis whose
// Wi-Fi lives in the volatile coprocessor store (wlan-mode "bco" /
// "taigan-bco") — there STR's boot-time replay really is what keeps the box
// on Wi-Fi, and a missing file does strand the user on the next cold boot.
//
// v0.9.7: v0.9.6 warned on ANY missing wlan-creds and hit healthy boxes whose
// Wi-Fi persists in the Bose firmware's own store (sm2 wpa_supplicant
// profiles) or that run on ethernet — boxes with months of clean cold boots
// suddenly told the user they would strand (#381 cgb280, #119). An unknown or
// absent wlan-mode stays quiet for the same reason.
func wlanCredsWarningWarranted() bool {
	if hasSavedWLANCreds() {
		return false
	}
	mode, err := os.ReadFile(filepath.Join(strNANDDir, "wlan-mode"))
	if err != nil {
		return false
	}
	switch strings.TrimSpace(string(mode)) {
	case "bco", "taigan-bco":
		return true
	}
	return false
}

// nandEntry is one top-level /mnt/nv entry with its recursive size.
type nandEntry struct {
	Name    string `json:"name"`
	Bytes   int64  `json:"bytes"`
	IsDir   bool   `json:"isDir"`
	Foreign bool   `json:"foreign"`
}

// nandInventory is the structured disk report used by /api/debug/state: df for
// the writable filesystems plus per-entry sizes under /mnt/nv (sorted biggest
// first) with foreign dirs flagged.
func nandInventory() map[string]any {
	rep := map[string]any{}
	if total, avail, ok := diskFree(nandRoot); ok {
		rep["nvTotalBytes"] = total
		rep["nvFreeBytes"] = avail
		rep["nvUsedBytes"] = total - avail
	}
	if total, avail, ok := diskFree("/"); ok {
		rep["rootTotalBytes"] = total
		rep["rootFreeBytes"] = avail
	}
	entries, err := os.ReadDir(nandRoot)
	if err != nil {
		rep["nvEntriesErr"] = err.Error()
		return rep
	}
	list := make([]nandEntry, 0, len(entries))
	foreign := []string{}
	for _, e := range entries {
		ne := nandEntry{
			Name:  e.Name(),
			Bytes: dirBytes(filepath.Join(nandRoot, e.Name())),
			IsDir: e.IsDir(),
		}
		if e.IsDir() && isForeignNANDDir(e.Name()) {
			ne.Foreign = true
			foreign = append(foreign, e.Name())
		}
		list = append(list, ne)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Bytes > list[j].Bytes })
	rep["nvEntries"] = list
	// STR's own dir broken down (bin/ files included), so a bundle shows
	// whether the agent binary, the Spotify engine, or logs eat the space.
	rep["strEntries"] = strDirEntries()
	rep["foreignDirs"] = foreign // empty => STR/Bose-only ("fresh")
	// Two "the box will misbehave / strand the user" signals (#270): a rival
	// SoundTouch tool's marker files, and no STR-saved Wi-Fi (stick-only online).
	rep["conflictingMod"] = detectConflictingMod()
	rep["hasWLANCreds"] = hasSavedWLANCreds()
	return rep
}

// nandReportLine is a compact one-line inventory for embedding in an OTA error
// (which the desktop app surfaces verbatim and the user pastes into a report):
// free/total, the biggest few /mnt/nv entries, and any foreign dirs.
func nandReportLine() string {
	var b strings.Builder
	if total, avail, ok := diskFree(nandRoot); ok {
		fmt.Fprintf(&b, "/mnt/nv free=%dKB total=%dKB", avail/1024, total/1024)
	} else {
		b.WriteString("/mnt/nv df unavailable")
	}
	entries, err := os.ReadDir(nandRoot)
	if err != nil {
		return b.String()
	}
	type es struct {
		name    string
		bytes   int64
		foreign bool
	}
	list := make([]es, 0, len(entries))
	foreign := []string{}
	for _, e := range entries {
		f := e.IsDir() && isForeignNANDDir(e.Name())
		list = append(list, es{e.Name(), dirBytes(filepath.Join(nandRoot, e.Name())), f})
		if f {
			foreign = append(foreign, e.Name())
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].bytes > list[j].bytes })
	b.WriteString("; top:")
	for i, e := range list {
		if i >= 6 {
			break
		}
		fmt.Fprintf(&b, " %s=%dKB", e.name, e.bytes/1024)
	}
	// Break STR's own dir down too: a bare "streborn=30071KB" cannot show
	// whether the space-pressed engine drop actually removed go-librespot
	// (#270: the engine survived a reclaim and the report could not say so).
	strList := strDirEntries()
	if len(strList) > 0 {
		b.WriteString("; str:")
		for i, e := range strList {
			if i >= 5 {
				break
			}
			fmt.Fprintf(&b, " %s=%dKB", e.Name, e.Bytes/1024)
		}
	}
	if len(foreign) > 0 {
		b.WriteString("; foreign(non-STR/Bose): " + strings.Join(foreign, ","))
	} else {
		b.WriteString("; foreign: none")
	}
	return b.String()
}

// strDirEntries lists the entries under /mnt/nv/streborn (with bin/ broken out
// into its files, since the two big binaries live there), biggest first.
func strDirEntries() []nandEntry {
	var list []nandEntry
	for _, sub := range []string{strNANDDir, filepath.Join(strNANDDir, "bin")} {
		ents, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if sub == strNANDDir && e.Name() == "bin" {
				continue // broken out via the second pass
			}
			p := filepath.Join(sub, e.Name())
			rel, rerr := filepath.Rel(nandRoot, p)
			if rerr != nil {
				rel = p
			}
			list = append(list, nandEntry{Name: rel, Bytes: dirBytes(p), IsDir: e.IsDir()})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Bytes > list[j].Bytes })
	return list
}

// reclaimNAND frees obvious, regenerable junk so a tight OTA write has room:
// stale .new temps from an interrupted OTA and oversized/rotated logs. The
// agent-side mirror of run.sh's cleanup_nand for the OTA path, which (unlike a
// stick boot) does not pass through a reboot. Best-effort throughout; never
// touches Bose files or the live binaries.
func reclaimNAND() {
	binDir := filepath.Join(strNANDDir, "bin")
	for _, pat := range []string{
		filepath.Join(binDir, "*.new"),
		// lib/*.new: interrupted shim atomic-replace temps (str-shim.so.new etc.);
		// cleaned here too so the bin/ and lib/ atomic writes are symmetric.
		filepath.Join(strNANDDir, "lib", "*.new"),
		filepath.Join(strNANDDir, "cap*.ogg"),
	} {
		if matches, _ := filepath.Glob(pat); matches != nil {
			for _, m := range matches {
				_ = os.Remove(m)
			}
		}
	}
	_ = os.Remove(filepath.Join(strNANDDir, "agent.log.1"))
	_ = os.Remove(filepath.Join(nandRoot, "sp-oauth.out"))
	// Stranded SSH-repair staging dir: the desktop app stages the ~28 MB install
	// file set into <base>/streborn-install and the install copies it into
	// /mnt/nv/streborn, but an older app left the staging copy behind, filling the
	// NAND so the next OTA's .new could not fit (#ST30 Daniel). The running agent
	// never uses it (it runs from streborn/bin), so it is always safe to drop here.
	_ = os.RemoveAll(filepath.Join(nandRoot, "streborn-install"))
	_ = os.RemoveAll(filepath.Join(strNANDDir, "streborn-install"))
	// run.out (whole-session run-override.sh stdout) is bounded per boot but
	// uncapped within one long uptime, and run.sh's cleanup_nand omits it; cap it
	// here on every OTA write alongside the rotated logs.
	for _, name := range []string{"setup.log", "setup.log.prev", "agent.log", "previous.log", "boot.log", "run.out"} {
		p := filepath.Join(strNANDDir, name)
		if fi, err := os.Stat(p); err == nil && fi.Size() > 131072 {
			if b, rerr := os.ReadFile(p); rerr == nil && int64(len(b)) > 65536 {
				_ = os.WriteFile(p, b[int64(len(b))-65536:], 0o644)
			}
		}
	}
}

// ReclaimNAND frees regenerable NAND junk (stale OTA temps, the ~28 MB
// SSH-repair staging dir, oversized logs) from outside the package. The agent
// calls it once at startup so a box left tight by an interrupted OTA or an older
// app's staging leftover self-heals on the next agent (re)start, without waiting
// for a full run.sh reboot or the next OTA write. Best-effort; never touches Bose
// files or the live binaries.
func ReclaimNAND() { reclaimNAND() }

// reclaimSpotifyEngine removes the go-librespot Spotify sidecar binary (and its
// sha marker) to free the single biggest regenerable block on a tight NAND. The
// running agent does not need it to apply an update, and the desktop app
// re-delivers it after the reboot (EnsureSpotifyEngine, triggered by goLibrespot
// != "present"), so dropping it is always recoverable. Called only by the
// space-pressed OTA write, never on a roomy box (#119). The returned outcome is
// logged and embedded into a 507 error, because a silent failure here left a
// field report (#270) with a full NAND and no clue whether the drop ever
// happened.
func reclaimSpotifyEngine() string {
	_ = os.Remove(goLibrespotBinPath + ".sha256")
	err := os.Remove(goLibrespotBinPath)
	switch {
	case err == nil:
		return "engine dropped"
	case os.IsNotExist(err):
		return "engine absent"
	default:
		return "engine drop failed: " + err.Error()
	}
}

// nandHasRoom reports whether the filesystem backing dir can hold a need-byte
// file plus the atomic-write margin. Returns true when df is unavailable, so an
// unknown free figure never blocks a write (matching the original gate). Since
// the optimistic-write change it only steers the reclaim cascade and logging;
// it no longer refuses the write. Var so the test for that optimistic attempt
// can force the pessimistic "no room" prediction on a roomy temp dir.
var nandHasRoom = func(dir string, need int64) bool {
	_, avail, ok := diskFree(dir)
	if !ok {
		return true
	}
	return avail >= need+nandWriteMargin
}

// handleAgentSidecar receives the go-librespot Spotify sidecar binary and
// writes it atomically to /mnt/nv/streborn/bin/go-librespot. It does NOT reboot
// the box, but it DOES hot-swap the engine in place: after the write it restarts
// the supervised go-librespot (spotifyReload) so the freshly delivered binary is
// live with no reboot (#240). A first-time delivery to a box that had no engine
// is picked up by the manager's waitForBinary instead; either way no reboot is
// needed. This closes the gap where the sidecar shipped only via the stick->NAND
// boot sync, leaving an OTA-only box (e.g. a SoundTouch 30 whose USB stick never
// copied it) silently unable to play Spotify despite a synced login (#45/#105).
//
// Hot-swapping also frees NAND sooner: the old engine inode is held by the
// running process until it exits, so killing+relaunching it on the write releases
// that ~10 MB immediately instead of holding it until the next reboot.
func (s *Server) handleAgentSidecar(w http.ResponseWriter, r *http.Request) {
	body, ok := readUploadedELF(w, r)
	if !ok {
		return
	}
	if err := writeBinaryAtomic(goLibrespotBinPath, body); err != nil {
		http.Error(w, err.Error(), nandWriteHTTPStatus(err))
		return
	}
	// Stamp the content hash next to the binary so the next /api/agent/version
	// reports it and the desktop app skips re-pushing this ~10 MB binary when the
	// box already has the embedded build.
	sum := sha256.Sum256(body)
	if err := os.WriteFile(goLibrespotBinPath+".sha256", []byte(hex.EncodeToString(sum[:])), 0o644); err != nil {
		s.logger.Warn("go-librespot sidecar: hash marker write failed (non-fatal)", "err", err)
	}
	// Flush now: an agent OTA often reboots the box right after this delivery,
	// and an unsynced 16 MB engine simply vanished across that reboot (deqw,
	// 2026-07-12: "delivered" 09:18:51, gone after the 09:19 reboot, re-pushed
	// 09:22:50 — which then wedged the box).
	_ = exec.Command("sync").Run()
	s.logger.Info("go-librespot sidecar written via OTA", "size", len(body))
	// Activate the freshly delivered engine live: restart the supervised
	// go-librespot so it re-execs the new binary, with no box reboot. A first-time
	// delivery to a box that had no engine is already picked up by the manager's
	// waitForBinary; this hot-swaps the already-running case, which previously
	// needed a manual restart after the update (#240 Pierre, #ST30 Daniel).
	// Best-effort and reported back so the desktop app's diagnostics can see it;
	// the engineHotSwap capability in /api/agent/version is what gates the app's
	// decision to skip its activation reboot.
	reloaded := false
	if s.spotifyReload != nil {
		reloaded = s.spotifyReload()
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"reloaded": strconv.FormatBool(reloaded),
	})
}

// isLocalLAN true if the request comes from a private LAN IP
// (RFC1918) or localhost. Updates from the internet are blocked.
func isLocalLAN(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsPrivate() || ip.IsLoopback()
}

// guessErrorReason converts the technical UPnP / network error into a
// human-readable hint. The box's SOAP responses are heavily wrapped in
// XML and not directly understandable.
func guessErrorReason(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "402") || strings.Contains(s, "No URI"):
		return "The stream could not be loaded. Some stations serve playlist files (.pls/.m3u) or HTTPS streams that the speaker cannot play directly. Try a different station."
	case strings.Contains(s, "no such host") || strings.Contains(s, "lookup"):
		return "Could not reach the stream URL server."
	case strings.Contains(s, "timeout"):
		return "Speaker did not respond. It may be in standby — try again."
	case strings.Contains(s, "connection refused"):
		return "Speaker refused the connection."
	default:
		return s
	}
}

// isGroupedRejection reports whether a UPnP play failure is the box refusing
// transport control because it is currently a FOLLOWER in a multiroom zone /
// stereo group: the firmware answers SetAVTransportURI with UPnP error 501
// "Can't control member of group" (#70). Matched on the fault description
// (the code 501 alone is the generic "Action Failed").
func isGroupedRejection(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "member of group")
}

// writeGroupedPlayError answers a play request the box rejected as a group
// follower (#70) with a structured 409 instead of the raw SOAP fault, so the
// app can tell the user to drive the group's lead speaker (and offer to jump
// there) rather than showing an inscrutable UPnP error. The master hint is
// best-effort and omitted when unknown.
func (s *Server) writeGroupedPlayError(w http.ResponseWriter, err error) {
	resp := map[string]string{"error": "box-grouped"}
	if master := s.groupMasterHint(); master != "" {
		resp["master"] = master
	}
	s.logger.Info("play rejected: box is a grouped follower, answering 409 box-grouped",
		"master", resp["master"], "err", err)
	writeJSON(w, http.StatusConflict, resp)
}

// groupMasterHint resolves the current zone master (deviceID, falling back to
// the master's LAN IP) from the box's own /getZone, with a short budget so a
// slow firmware cannot stall the error response. Empty when unknown.
func (s *Server) groupMasterHint() string {
	if s.boxHost == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	z, err := boxapi.New(s.boxHost).GetZone(ctx)
	if err != nil {
		return ""
	}
	if z.Master != "" {
		return z.Master
	}
	return z.SenderIP
}

// ---- Box Settings (Bose API Proxy) ----

// handleBoxSettings returns info + volume + bass + network + sources
// combined as JSON.
func (s *Server) handleBoxSettings(w http.ResponseWriter, r *http.Request) {
	if s.boxHost == "" {
		http.Error(w, "box host not configured", http.StatusServiceUnavailable)
		return
	}
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	settings, err := c.LoadSettings(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// wlan0/wpa boxes: STR changes Wi-Fi by rewriting wpa_supplicant directly,
	// bypassing Bose, so Bose /networkInfo keeps reporting the OLD profile (stale
	// SSID / frequency / signal even though the box is really associated
	// elsewhere). Read the LIVE association from wpa_supplicant and let it win;
	// /networkInfo is only the fallback when the live read is unavailable or the
	// field is empty. BCO/eth0 boxes have no wpa_supplicant and keep the
	// gabbo-signal + provisionedSSID path below untouched.
	if iface, mech := detectWlanMechanism(); mech == "wpa" {
		if live := wlanlive.Read(ctx, iface); live.Associated {
			for i := range settings.Network.Interfaces {
				ni := &settings.Network.Interfaces[i]
				if ni.Type != "WIFI_INTERFACE" {
					continue
				}
				if live.SSID != "" {
					ni.SSID = live.SSID
				}
				if live.FrequencyKHz != 0 {
					ni.Frequency = live.FrequencyKHz
				}
				if live.Signal != "" {
					ni.Signal = live.Signal
				}
			}
		}
	}
	// BCO speakers (Portable, scm ST20) report the connected interface as
	// ethernet with no signal in /networkInfo, but the box does emit the
	// Wi-Fi signal class over the gabbo WebSocket. Fill the connected
	// interface's empty signal from there so the settings UI shows it on
	// those models too.
	if s.wifiSignalFn != nil {
		if sig := s.wifiSignalFn(); sig != "" {
			for i := range settings.Network.Interfaces {
				ni := &settings.Network.Interfaces[i]
				if ni.Signal == "" && (ni.State == "NETWORK_ETHERNET_CONNECTED" || ni.State == "NETWORK_WIFI_CONNECTED") {
					ni.Signal = sig
				}
			}
		}
	}
	// Same for the SSID: BCO /networkInfo carries none, but STR knows the
	// network it provisioned (its wlan-creds, or the AirplayConfiguration
	// profile). Fill it on the connected interface when empty.
	if ssid := provisionedSSID(); ssid != "" {
		for i := range settings.Network.Interfaces {
			ni := &settings.Network.Interfaces[i]
			if ni.SSID == "" && (ni.State == "NETWORK_ETHERNET_CONNECTED" || ni.State == "NETWORK_WIFI_CONNECTED") {
				ni.SSID = ssid
			}
		}
	}
	writeJSON(w, http.StatusOK, settings)
}

// provisionedSSID returns the Wi-Fi SSID the speaker is on, from the most
// reliable source available. Used to fill the SSID on BCO boxes (scm ST20,
// Portable), whose /networkInfo reports only an ethernet coprocessor with
// no ssid field.
//
// Sources, in order:
//  1. STR's own wlan-creds: what STR last provisioned. Subject to the
//     stick-mount race at cold boot, so it can be empty even on a box that
//     is associated (issue #90).
//  2. Bose NetManager's NetworkProfiles.xml: the box's own ground truth for
//     the profile it associates to. Present before BoseApp's HTTP server is
//     up, so it survives the boot race that leaves wlan-creds empty.
//  3. The slot-0 PersistentWifiProfile in AirplayConfiguration.xml (WAC
//     onboarding path).
func provisionedSSID() string {
	if b, err := os.ReadFile("/mnt/nv/streborn/wlan-creds"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "SSID=") {
				if v := strings.TrimSpace(strings.TrimPrefix(line, "SSID=")); v != "" {
					return v
				}
			}
		}
	}
	if m, _ := filepath.Glob("/mnt/nv/BoseApp-Persistence/*/NetworkProfiles.xml"); len(m) > 0 {
		if b, err := os.ReadFile(m[0]); err == nil {
			if v := ssidAfter(string(b), "<profile"); v != "" {
				return v
			}
		}
	}
	if m, _ := filepath.Glob("/mnt/nv/BoseApp-Persistence/*/AirplayConfiguration.xml"); len(m) > 0 {
		if b, err := os.ReadFile(m[0]); err == nil {
			if v := ssidAfter(string(b), "PersistentWifiProfile"); v != "" {
				return v
			}
		}
	}
	return ""
}

// ssidAfter extracts the first ssid="..." attribute value that appears at
// or after the given anchor substring in s, or "" if none.
func ssidAfter(s, anchor string) string {
	i := strings.Index(s, anchor)
	if i < 0 {
		return ""
	}
	r := s[i:]
	j := strings.Index(r, `ssid="`)
	if j < 0 {
		return ""
	}
	r = r[j+len(`ssid="`):]
	if k := strings.IndexByte(r, '"'); k >= 0 {
		return r[:k]
	}
	return ""
}

// handleBoxName PUT sets the box name. Body {"name":"..."}.
func (s *Server) handleBoxName(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPut) {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSONRequest(w, r, 1024, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "name empty", http.StatusBadRequest)
		return
	}
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	if err := c.SetName(ctx, req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// On /name POST Bose also resets the margeURL back to default —
	// trigger AutoPair so the pair state is immediately re-established.
	if s.autoPair != nil {
		go s.autoPair.TriggerNow(context.Background())
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "name": req.Name})
}

// handleBoxVolume PUT sets the volume. Body {"value":N}.
func (s *Server) handleBoxVolume(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet, http.MethodPut) {
		return
	}
	c := boxapi.New(s.boxHost)
	// GET returns the current volume so home automation / a status display can
	// read the level (and do relative up/down) without parsing the heavier
	// /api/box/settings blob.
	if r.Method == http.MethodGet {
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		v, err := c.GetVolume(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"value": v.Actual, "target": v.Target, "muted": v.Muted})
		return
	}
	// PUT sets the absolute volume (0-100).
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	var req struct {
		Value int `json:"value"`
	}
	if !decodeJSONRequest(w, r, 256, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	if err := c.SetVolume(ctx, req.Value); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"value": req.Value})
}

// handleBoxSource PUT switches the box to another source: AUX,
// BLUETOOTH or STANDBY. Body {"source":"AUX"}.
//
// Bose /select expects a ContentItem XML. We build it depending on the
// source. STANDBY has its own ContentItem without sourceAccount.
func (s *Server) handleBoxSource(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPut) {
		return
	}
	var req struct {
		Source string `json:"source"`
	}
	if !decodeJSONRequest(w, r, 256, &req) {
		return
	}
	src := strings.ToUpper(strings.TrimSpace(req.Source))
	if src == "" {
		http.Error(w, "source missing", http.StatusBadRequest)
		return
	}
	client := &http.Client{Timeout: 6 * time.Second}

	// Special case STANDBY: no ContentItem source at Bose. /key POWER
	// only triggers the LED animation, /standby is the real endpoint —
	// and Bose expects **GET**, not POST (POST returns 400).
	if src == "STANDBY" {
		u := fmt.Sprintf("http://%s:8090/standby", s.boxHost)
		resp, err := client.Get(u)
		if err != nil {
			http.Error(w, "box unreachable: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			http.Error(w, "box error: "+string(respBody), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"source": "STANDBY"})
		return
	}

	var body string
	switch src {
	case "AUX":
		body = `<ContentItem source="AUX" sourceAccount="AUX"></ContentItem>`
	case "BLUETOOTH", "BT":
		body = `<ContentItem source="BLUETOOTH" sourceAccount=""></ContentItem>`
	default:
		http.Error(w, "unsupported source: "+src, http.StatusBadRequest)
		return
	}
	url := fmt.Sprintf("http://%s:8090/select", s.boxHost)
	httpReq, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, url, strings.NewReader(body))
	httpReq.Header.Set("Content-Type", "text/xml")
	httpReq.Header.Set("User-Agent", "STR/1.0")
	resp, err := client.Do(httpReq)
	if err != nil {
		http.Error(w, "box unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		// A speaker that lacks the requested hardware source (e.g. the
		// ST20 variants without Bluetooth) rejects /select with a 1005
		// UNKNOWN_SOURCE_ERROR. Surface that as a machine-readable reason
		// so the client can show a friendly localized message instead of
		// the raw Bose error XML.
		if strings.Contains(string(respBody), "UNKNOWN_SOURCE_ERROR") {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "source_unavailable",
				"source": src,
			})
			return
		}
		http.Error(w, "box error: "+string(respBody), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"source": src})
}

// handleBoxPower is the mobile remote's single on/off control. Body {"on":bool}.
//
// "off" puts the box into Bose standby (GET /standby) - the real power-off. Users
// reported that Stop only pauses the stream and the speaker stays on (the box has
// no concept of "off" for a stream, Stop just halts the transport); standby is
// what actually switches it off, so the remote needs its own power control.
//
// "on" wakes the box from standby and brings the last station back, the same
// power-on resume a hardware power press gives. Because this is an explicit user
// press, it skips the ResumeLastPlay self-wake/zone guards and pushes the last
// stream directly; if nothing is remembered the bare wake still powers the box up.
func (s *Server) handleBoxPower(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if !decodeJSONRequest(w, r, 256, &req) {
		return
	}
	if s.boxHost == "" {
		http.Error(w, "box host not configured", http.StatusServiceUnavailable)
		return
	}
	if !req.On {
		// Power off: Bose /standby expects GET (POST returns 400). Same call the
		// Standby input uses; the real power-off, unlike Stop/Pause.
		client := &http.Client{Timeout: 6 * time.Second}
		resp, err := client.Get(fmt.Sprintf("http://%s:8090/standby", s.boxHost))
		if err != nil {
			http.Error(w, "box unreachable: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			http.Error(w, "box error: "+string(body), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"on": false})
		return
	}
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
	// Power on: explicit user "play it again", so clear any deliberate-stop intent,
	// wake the box, then re-push the last station from under the lock.
	s.ClearUserStop()
	s.ensureBoxReady(r.Context())
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	var boxURL, title, art, mime string
	if lp != nil {
		lp.failed = false
		lp.rePushes = 0
		boxURL, title, art, mime = lp.boxURL, lp.title, lp.art, lp.mime
	}
	s.lastPlayMu.Unlock()
	if boxURL != "" {
		if err := s.renderer.PlayURLMime(r.Context(), boxURL, title, art, mime); err != nil {
			s.logger.Warn("power on: resume of last station failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"on": true})
}

// handleBoxBass PUT sets the bass value. Body {"value":N}.
func (s *Server) handleBoxBass(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPut) {
		return
	}
	var req struct {
		Value int `json:"value"`
	}
	if !decodeJSONRequest(w, r, 256, &req) {
		return
	}
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	if err := c.SetBass(ctx, req.Value); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"value": req.Value})
}

// handleBoxZone serves the SoundTouch multiroom zone (#70, BETA):
//
//	GET    -> the live zone the box reports {"master","senderIP","members"[]}
//	POST   -> form/replace a zone with THIS box as master (body: master + slaves)
//	DELETE -> dissolve the zone this box leads
//
// POST/DELETE also persist to the zones store so the zone auto-reforms after a
// reboot/standby/Wi-Fi outage without the user re-grouping. This is the blind
// beta path: it drives the native Bose /setZone family directly and logs every
// step (master, slaves, the firmware's read-back) into agent.log so multi-speaker
// testers' diagnostic bundles show exactly what the firmware did.
func (s *Server) handleBoxZone(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleZoneGet(w, r)
	case http.MethodPost:
		s.handleZoneForm(w, r)
	case http.MethodDelete:
		s.handleZoneDissolve(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleZoneGet(w http.ResponseWriter, r *http.Request) {
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	z, err := c.GetZone(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, z)
}

type zoneMemberReq struct {
	DeviceID string `json:"deviceID"`
	IP       string `json:"ip"`
}

type zoneFormReq struct {
	Master zoneMemberReq   `json:"master"`
	Slaves []zoneMemberReq `json:"slaves"`
	Name   string          `json:"name"`
	Stereo bool            `json:"stereo"`
	// Mode is "native" (firmware /setZone) or "mirror" (each slave's box pulls
	// the master's stream via UPnP). Empty defaults to native.
	Mode string `json:"mode"`
}

// handleZoneForm creates (or replaces) a group with this box as master (#70 beta).
// Two user-switchable modes: "native" drives the Bose /setZone family so the
// firmware syncs the slaves (tightest, when the firmware accepts STR's source);
// "mirror" points each slave's box at the master's current stream over UPnP
// (looser sync, works more widely). Either way the group is persisted so it
// auto-reforms after a reboot/standby. The caller supplies the master's and
// slaves' deviceID+IP from discovery, so the agent need not self-identify.
func (s *Server) handleZoneForm(w http.ResponseWriter, r *http.Request) {
	var req zoneFormReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Master.DeviceID == "" || len(req.Slaves) == 0 {
		http.Error(w, "master deviceID and at least one slave are required", http.StatusBadRequest)
		return
	}
	mode := req.Mode
	if mode != "mirror" {
		mode = "native"
	}
	master := boxapi.ZoneMember{DeviceID: req.Master.DeviceID, IP: req.Master.IP}
	slaves := make([]boxapi.ZoneMember, 0, len(req.Slaves))
	for _, m := range req.Slaves {
		slaves = append(slaves, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
	}
	s.logger.Info("zone: forming (beta)", "mode", mode, "master", master.DeviceID, "masterIP", master.IP,
		"slaves", len(slaves), "stereo", req.Stereo, "name", req.Name)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	c := boxapi.New(s.boxHost)

	// The master is always THIS box, so resolve its deviceID from the local
	// firmware /info rather than trusting the app-supplied value (#70). The
	// desktop derives a member's deviceID from discovery, where a two-chip
	// chassis (ST20 spotty/BCO, Portable) announces its wlan0 (SMSC) MAC over
	// mDNS, which is NOT the SoundTouch deviceID the firmware keys /setZone and
	// /addGroup on (that is the SCM MAC in /info). For the master that mismatch
	// is fatal: the firmware never recognizes itself as master, so the zone reads
	// back empty (the "0.8.x regression" deqw and Albrecht hit was really this).
	master.DeviceID = s.localDeviceID(ctx, c, master.DeviceID)

	// A stereo pair is a firmware-native L/R group (POST /addGroup), not a
	// multiroom zone. It needs exactly one partner; the master is the LEFT
	// channel and the partner the RIGHT by Bose convention. Only the ST10
	// actually pairs, but every model lists /addGroup, so we let the firmware
	// be the authority and surface its real response to the app.
	if req.Stereo {
		s.formStereoPair(w, ctx, c, master, slaves, req.Name)
		return
	}

	// Persist first so a transient drive error still leaves the group on record
	// for the reconcile loop to retry. Only the master persists.
	z := zones.Zone{Master: master.DeviceID, MasterIP: master.IP, Mode: mode, Name: req.Name}
	for _, m := range slaves {
		z.Slaves = append(z.Slaves, zones.Member{DeviceID: m.DeviceID, IP: m.IP})
	}
	if s.zones != nil {
		if err := s.zones.Set(z); err != nil {
			s.logger.Warn("zone: persist failed", "err", err)
		}
	}

	if mode == "mirror" {
		// Deliberate user action: push unconditionally (reconcile=false), the
		// user just asked for exactly this group.
		s.mirrorToSlaves(ctx, z, false)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "mirror"})
		return
	}

	// Native: drive the firmware zone and read back what it actually formed.
	//
	// /setZone tears down the master's in-flight UPnP session (#70): the
	// firmware cannot adopt an externally pushed session into a fresh zone, so
	// forming a group while music plays deselects the source (INVALID_SOURCE,
	// errorUpdate 1036 UpnpRcvdContentItemInWrongState) and the room goes
	// silent even though the zone reports formed, with "Select a preset..." on
	// the display. Capture whether STR's stream was playing BEFORE the form and
	// re-push it to the now-grouped master afterwards; the master distributes
	// it to the followers (verified live: a play pushed to the master after
	// forming reaches every member).
	var resume *lastPlayInfo
	if _, busy := s.boxPlayState(); busy {
		s.lastPlayMu.Lock()
		if s.lastPlay != nil {
			cp := *s.lastPlay
			resume = &cp
		}
		s.lastPlayMu.Unlock()
	}

	// Never form against a standby master: the firmware then wakes INTO its
	// stale UPnP item, throws the 1036 wrong-state error and self-dissolves
	// the fresh zone ~300ms after reporting ok (#70, observed live).
	s.ensureBoxReady(ctx)

	// Remove members the user dropped from the group. /setZone only ADDS the
	// listed slaves, it never removes one, so re-forming with a smaller list -
	// exactly how the app removes a member (uncheck + apply) - leaves the dropped
	// box in the firmware zone: it "briefly leaves then comes back" (Albrecht,
	// 7-box fleet, 2026-07-14). Read the live zone and RemoveZoneSlave anyone no
	// longer wanted. Match on IP, the chassis-stable key: a two-chip box (Portable,
	// ST20 BCO) announces its wlan0 MAC over discovery, which is NOT the SCM
	// deviceID the firmware lists for it, so a deviceID-only match would wrongly
	// keep the dropped box. Best-effort, before the add below.
	if live, gerr := c.GetZone(ctx); gerr == nil && live.Master != "" && len(live.Members) > 0 {
		wantIP := make(map[string]bool, len(slaves))
		wantDev := make(map[string]bool, len(slaves))
		for _, sl := range slaves {
			if sl.IP != "" {
				wantIP[sl.IP] = true
			}
			if sl.DeviceID != "" {
				wantDev[strings.ToLower(sl.DeviceID)] = true
			}
		}
		var toRemove []boxapi.ZoneMember
		for _, m := range live.Members {
			keep := (m.IP != "" && wantIP[m.IP]) || (m.DeviceID != "" && wantDev[strings.ToLower(m.DeviceID)])
			if !keep {
				toRemove = append(toRemove, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
			}
		}
		if len(toRemove) > 0 {
			s.logger.Info("zone: dropping members no longer in the group before re-forming", "count", len(toRemove), "master", master.DeviceID)
			if err := c.RemoveZoneSlave(ctx, master, toRemove); err != nil {
				s.logger.Warn("zone: reconcile removeZoneSlave failed", "err", err)
			}
		}
	}

	if err := c.SetZone(ctx, master, slaves); err != nil {
		s.logger.Warn("zone: setZone failed", "err", err, "master", master.DeviceID)
		http.Error(w, "setZone: "+err.Error(), http.StatusBadGateway)
		return
	}
	z2, err := c.GetZone(ctx)
	if err != nil {
		s.logger.Warn("zone: formed but getZone read-back failed", "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "native"})
		return
	}
	// The master's optimistic member list is not proof a slave joined (#70): the
	// firmware lists a member it announced to before the slave's own zone reflects
	// enrolment, so a 3-box group reported success while one box silently never
	// joined. The authoritative "missing" set therefore comes from each FOLLOWER's
	// own /getZone (verifyFollowersJoined), polled with a short retry because a
	// slave's self-report lags forming by ~100ms to several seconds. The master's
	// read-back is kept only as supplementary diagnostics (masterMissing).
	masterLive := make(map[string]bool, len(z2.Members))
	for _, m := range z2.Members {
		masterLive[strings.ToLower(m.DeviceID)] = true
	}
	masterMissing := make([]string, 0)
	for _, sl := range slaves {
		if !masterLive[strings.ToLower(sl.DeviceID)] {
			masterMissing = append(masterMissing, sl.DeviceID)
		}
	}
	missing, unverifiable := verifyFollowersJoined(ctx, s.logger, z2.Master, slaves, func(fctx context.Context, ip string) (boxapi.Zone, error) {
		return boxapi.New(ip).GetZone(fctx)
	})
	verified := len(slaves) - len(missing)
	// Regression guard (#70 / Albrecht 0.8.x): if the master's own read-back shows
	// no members and no master after SetZone, the firmware never actually formed a
	// zone (it worked in 0.7.29, broke in 0.8.0x). Report that honestly as ok=false
	// so the app stops claiming success when nothing joined, instead of leaning on
	// the optimistic "ok=true" the old code always returned.
	masterFormed := len(z2.Members) > 0 && z2.Master != ""
	ok := verified > 0
	if !masterFormed {
		s.logger.Warn("zone: master read-back empty after setZone (slaves did not join — possible 0.8.x regression)",
			"liveMaster", z2.Master, "liveMembers", len(z2.Members), "requestedSlaves", len(slaves))
	}
	s.logger.Info("zone: formed", "mode", "native", "ok", ok, "liveMaster", z2.Master,
		"requestedSlaves", len(slaves), "liveMembers", len(z2.Members),
		"masterMissing", strings.Join(masterMissing, ","),
		"verified", verified, "missing", strings.Join(missing, ","),
		"unverifiable", strings.Join(unverifiable, ","))
	if resume != nil && masterFormed {
		go s.resumeAfterZoneForm(*resume)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": ok, "mode": "native", "master": z2.Master, "senderIP": z2.SenderIP,
		"members": z2.Members, "requested": len(slaves),
		"verified": verified, "missing": missing, "unverifiable": unverifiable,
		"masterMissing": masterMissing,
	})
}

// resumeAfterZoneForm re-pushes the stream that was playing on this box before
// a native zone form tore it down (see handleZoneForm). The firmware needs a
// settle moment after /setZone before it accepts a new SetURI - pushing too
// early just re-triggers the 1036 wrong-state error - so wait, then push under
// the box command lock, standing down when the user stopped meanwhile or a
// newer play superseded the captured one.
func (s *Server) resumeAfterZoneForm(lp lastPlayInfo) {
	if s.renderer == nil {
		return
	}
	time.Sleep(1500 * time.Millisecond)
	if s.userStoppedRecently() {
		s.logger.Info("zone: not restarting playback after forming, user stopped meanwhile")
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	s.lastPlayMu.Lock()
	cur := s.lastPlay
	s.lastPlayMu.Unlock()
	if resumeIsStale(lp.boxURL, lp.ts, cur) {
		s.logger.Info("zone: not restarting playback after forming, a newer play superseded it",
			"captured", lp.boxURL, "current", lastPlayURL(cur))
		return
	}
	push := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if lp.mime != "" {
			return s.renderer.PlayURLMime(ctx, lp.boxURL, lp.title, lp.art, lp.mime)
		}
		return s.renderer.PlayURL(ctx, lp.boxURL, lp.title, lp.art)
	}
	err := push()
	if err != nil {
		// One retry after a longer settle: right after /setZone the firmware
		// sporadically rejects the first SetURI while the zone is still wiring
		// its followers.
		time.Sleep(3 * time.Second)
		err = push()
	}
	if err != nil {
		s.logger.Warn("zone: could not restart the master's stream after forming; the group is formed but silent - press play or a preset to start it",
			"err", err, "url", lp.boxURL)
		return
	}
	s.logger.Info("zone: master's stream restarted after group forming", "url", lp.boxURL, "title", lp.title)
}

// handleSpotifyCredential moves the go-librespot Spotify login between speakers
// (#45): GET returns this box's active credential blob, POST installs a blob
// exported from another box and restarts go-librespot to log in with it. LAN-only,
// same trust model as the rest of the agent API; the blob is a reusable Spotify
// Connect credential, so the desktop app should only move it between the user's
// own speakers.
func (s *Server) handleSpotifyCredential(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if s.spotifyExportCred == nil {
			http.Error(w, "spotify not configured", http.StatusServiceUnavailable)
			return
		}
		data, err := s.spotifyExportCred()
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "no spotify login stored on this speaker", "detail": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	case http.MethodPost:
		if s.spotifyImportCred == nil {
			http.Error(w, "spotify not configured", http.StatusServiceUnavailable)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256*1024))
		if err != nil || len(data) == 0 {
			http.Error(w, "empty or oversized credential", http.StatusBadRequest)
			return
		}
		if err := s.spotifyImportCred(r.Context(), data); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": "import failed", "detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// followerZoneFetch returns one follower's own zone self-report. Split out so a
// test can inject a fake without standing up a :8090 server (boxapi.New hardcodes
// the :8090 port via Client.url).
type followerZoneFetch func(ctx context.Context, ip string) (boxapi.Zone, error)

// verifyFollowersJoined polls each requested slave's OWN /getZone until it
// reports masterID as its zone master, or a per-follower deadline elapses (#70).
// Trusting only the master's optimistic member list reports a complete group
// while a follower is actually still standalone: the master lists a member the
// firmware announced before the slave actually enrolled, and the slave's own
// zone lags ~100ms to several seconds behind. A follower that never names
// masterID as its master within the budget is returned in "missing" so the app
// can flag it instead of claiming success. Followers with no known IP cannot be
// verified and are returned in "unverifiable" (left to the master's view).
func verifyFollowersJoined(ctx context.Context, logger *slog.Logger, masterID string, slaves []boxapi.ZoneMember, fetch followerZoneFetch) (missing, unverifiable []string) {
	return verifyFollowersJoinedTimed(ctx, logger, masterID, slaves, fetch, defaultFollowerVerifyTiming)
}

// followerVerifyTiming bounds verifyFollowersJoined's polling. Injected so the
// tests can shrink the budget from seconds to milliseconds; production always
// uses defaultFollowerVerifyTiming.
type followerVerifyTiming struct {
	perFollowerBudget time.Duration
	pollInterval      time.Duration
	perCallTimeout    time.Duration
}

var defaultFollowerVerifyTiming = followerVerifyTiming{
	perFollowerBudget: 4 * time.Second,
	pollInterval:      700 * time.Millisecond,
	perCallTimeout:    2 * time.Second,
}

// verifyFollowersJoinedTimed is verifyFollowersJoined with explicit timing;
// see there for the semantics.
func verifyFollowersJoinedTimed(ctx context.Context, logger *slog.Logger, masterID string, slaves []boxapi.ZoneMember, fetch followerZoneFetch, timing followerVerifyTiming) (missing, unverifiable []string) {
	perFollowerBudget := timing.perFollowerBudget
	pollInterval := timing.pollInterval
	perCallTimeout := timing.perCallTimeout
	for _, sl := range slaves {
		if sl.IP == "" {
			unverifiable = append(unverifiable, sl.DeviceID)
			continue
		}
		deadline := time.Now().Add(perFollowerBudget)
		joined := false
		var lastSelfMaster string
		var lastMembers int
		var lastErr error
		for {
			cctx, cancel := context.WithTimeout(ctx, perCallTimeout)
			fz, ferr := fetch(cctx, sl.IP)
			cancel()
			if ferr != nil {
				lastErr = ferr
			} else {
				lastErr = nil
				lastSelfMaster = fz.Master
				lastMembers = len(fz.Members)
				if fz.Master != "" && strings.EqualFold(fz.Master, masterID) {
					joined = true
					break
				}
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				break
			}
			select {
			case <-ctx.Done():
			case <-time.After(pollInterval):
			}
			if ctx.Err() != nil {
				break
			}
		}
		if joined {
			logger.Info("zone: follower confirmed", "follower", sl.DeviceID, "ip", sl.IP, "selfMaster", lastSelfMaster)
			continue
		}
		if lastErr != nil {
			logger.Info("zone: follower never confirmed (self-report unreachable)", "follower", sl.DeviceID, "ip", sl.IP, "err", lastErr.Error())
		} else {
			logger.Info("zone: follower never confirmed", "follower", sl.DeviceID, "ip", sl.IP, "selfMaster", lastSelfMaster, "selfMembers", lastMembers)
		}
		missing = append(missing, sl.DeviceID)
	}
	return missing, unverifiable
}

// localDeviceID returns this box's authoritative Bose SoundTouch deviceID, read
// from the local firmware /info, falling back to supplied when /info is
// unreachable or carries no deviceID. The zone protocol (/setZone, /addGroup)
// keys on this exact ID. The desktop derives a member's deviceID from discovery,
// which on a two-chip chassis can be the wlan0 (SMSC) MAC instead of the
// SoundTouch (SCM) deviceID; since the master is always the box this agent runs
// on, the agent is the authority for its own ID and corrects the mismatch (#70).
func (s *Server) localDeviceID(ctx context.Context, c *boxapi.Client, supplied string) string {
	info, err := c.GetInfo(ctx)
	if err != nil {
		return supplied
	}
	real := strings.TrimSpace(info.DeviceID)
	if real == "" {
		return supplied
	}
	if supplied != "" && !strings.EqualFold(real, supplied) {
		s.logger.Info("zone: corrected master deviceID from firmware /info (app sent the chassis wlan0/SMSC MAC, not the SoundTouch ID)",
			"supplied", supplied, "firmware", real)
	}
	return real
}

// formStereoPair drives POST /addGroup to make a real left/right stereo pair
// and persists it so it is honored on dissolve. master becomes LEFT, the single
// partner becomes RIGHT. The firmware decides whether the box can pair (ST10
// only); its error is returned verbatim to the app so testers see the truth.
func (s *Server) formStereoPair(w http.ResponseWriter, ctx context.Context, c *boxapi.Client, master boxapi.ZoneMember, slaves []boxapi.ZoneMember, name string) {
	if len(slaves) != 1 {
		http.Error(w, "a stereo pair needs exactly one partner speaker", http.StatusBadRequest)
		return
	}
	master.Role = "LEFT"
	partner := slaves[0]
	partner.Role = "RIGHT"
	if name == "" {
		name = "Stereo pair"
	}

	// Resolve the partner's REAL SoundTouch deviceID from its OWN firmware /info.
	// The app derives a member's deviceID from mDNS, where a two-chip chassis
	// announces its wlan0/SMSC MAC, not the deviceID the firmware keys /addGroup
	// on. localDeviceID already corrects this for the master; the partner (RIGHT)
	// needs the same, or AddGroup embeds the wrong chip's MAC and the firmware
	// silently drops the channel (live: an ST10+ST10 pair never formed, #70).
	if partner.IP != "" {
		if pinfo, perr := boxapi.New(partner.IP).GetInfo(ctx); perr == nil {
			if real := strings.TrimSpace(pinfo.DeviceID); real != "" {
				if !strings.EqualFold(real, partner.DeviceID) {
					s.logger.Info("stereo: corrected partner deviceID from its firmware /info (app sent the chassis MAC, not the SoundTouch ID)",
						"supplied", partner.DeviceID, "firmware", real, "partnerIP", partner.IP)
				}
				partner.DeviceID = real
			}
			// Bose stereo /addGroup needs both speakers set up on the SAME marge
			// account; an empty account is the usual silent reject (a tester's box-4).
			if strings.TrimSpace(pinfo.MargeAccountUUID) == "" {
				s.logger.Warn("stereo: partner has no marge account, /addGroup will likely be rejected (set the speaker up first)", "partnerIP", partner.IP)
			}
		} else {
			s.logger.Warn("stereo: could not read partner /info, using the app-supplied deviceID", "err", perr, "partnerIP", partner.IP)
		}
	}

	// Persist before driving the firmware so the dissolve path knows it is a
	// stereo pair even after an agent restart. Stereo pairs are firmware-native,
	// so the reconcile loop leaves them alone (the box re-forms across reboots).
	if s.zones != nil {
		z := zones.Zone{
			Master: master.DeviceID, MasterIP: master.IP, Stereo: true, Name: name,
			Slaves: []zones.Member{{DeviceID: partner.DeviceID, IP: partner.IP, Role: partner.Role}},
		}
		if err := s.zones.Set(z); err != nil {
			s.logger.Warn("stereo: persist failed", "err", err)
		}
	}

	s.logger.Info("stereo: pairing via /addGroup (beta)", "name", name,
		"left", master.DeviceID, "leftIP", master.IP, "right", partner.DeviceID, "rightIP", partner.IP)
	members := []boxapi.ZoneMember{master, partner}
	if err := c.AddGroup(ctx, name, master.DeviceID, members); err != nil {
		s.logger.Warn("stereo: addGroup failed (only the ST10 supports stereo pairs)", "err", err)
		http.Error(w, "addGroup: "+err.Error(), http.StatusBadGateway)
		return
	}
	g, err := c.GetGroup(ctx)
	if err != nil {
		s.logger.Warn("stereo: paired but getGroup read-back failed", "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stereo": true})
		return
	}
	// Assert the firmware actually bound BOTH channels. /addGroup can return 200
	// while silently dropping a member (wrong deviceID, account mismatch), which
	// the old code reported as ok=true — the user thought it worked but only one
	// speaker played. A real stereo pair must read back exactly two members.
	if len(g.Members) != 2 {
		s.logger.Warn("stereo: firmware formed an INCOMPLETE pair (a speaker was dropped)", "id", g.ID, "members", len(g.Members))
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "stereo": true, "members": len(g.Members),
			"error": "the speaker did not accept the pair. Both speakers must be set up and on the same account, and only the SoundTouch 10 supports stereo pairs.",
		})
		return
	}
	s.logger.Info("stereo: paired", "id", g.ID, "members", len(g.Members))
	writeJSON(w, http.StatusOK, g)
}

// mirrorToSlaves points each slave's box at the master's current stream URL over
// UPnP (the mirror path). The master's stream is whatever STR last told the
// master box to play (s.lastPlay), which the slaves can pull from the master
// agent's stream proxy. Looser than firmware sync, but works when the firmware
// refuses to distribute STR's source. Best-effort + heavily logged.
//
// reconcile marks the periodic 5-minute re-form (PeriodicZoneReconcile) as
// opposed to the user just having formed the group. The reconcile must only
// repair what is actually broken: lastPlay is PERSISTED across reboots, so
// without state guards an idle or standby master sprayed its stale last stream
// onto every slave each tick — a slave busy with a Spotify playlist was yanked
// to the master's old radio station every 5 minutes (#342), and a healthy
// mirroring slave was re-pushed into a re-buffer hiccup each tick. With
// reconcile set, the master must be actively playing the mirrored stream, and
// each slave is only (re)pointed per slaveMirrorAction (never woken from
// standby, never taken off another source it is playing).
func (s *Server) mirrorToSlaves(ctx context.Context, z zones.Zone, reconcile bool) {
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	s.lastPlayMu.Unlock()
	if lp == nil || lp.boxURL == "" {
		s.logger.Info("zone mirror: master is not playing yet; slaves will mirror once you start playback and the reconcile fires (beta)")
		return
	}
	// A deliberate form/remove must NOT resurrect playback the user stopped.
	// lastPlay outlives a stop, so pushing it here on the unconditional
	// (reconcile=false) path restarted a group the user had just stopped:
	// stopping a mirror group and then removing one slave restarted the stream
	// on ALL members including the removed one (live, Portable master + 2 ST10,
	// 2026-07-10). Only push when the master is actually playing its stream and
	// no user stop is in effect; otherwise just update the membership silently.
	// The reconcile=true path has its own per-box now-playing guards below.
	if !reconcile {
		if standby, busy := s.boxPlayState(); standby || !busy || s.userStoppedRecently() {
			s.logger.Info("zone mirror: master is stopped, updating group membership without restarting playback (beta)")
			return
		}
	}
	if reconcile {
		np := s.snapshotNowPlaying(ctx)
		if reason := masterMirrorSkipReason(np, lp.boxURL); reason != "" {
			s.logMirrorSkip("master", reason)
			return
		}
		s.clearMirrorSkip("master")
	}
	// lp.boxURL points the MASTER's own box at its loopback stream proxy
	// (http://127.0.0.1:8888/...). A slave cannot fetch that; it must reach the
	// master across the LAN. Rewrite the host to the master's LAN IP so each
	// slave pulls the master's stream (#70: the slave's display updated but its
	// audio kept its old stream because it was handed the master's loopback URL).
	slaveURL := s.mirrorURLForSlaves(ctx, lp.boxURL, z.MasterIP)
	for _, m := range z.Slaves {
		if m.IP == "" {
			continue
		}
		if reconcile {
			push, reason := slaveMirrorAction(fetchNowPlaying(ctx, m.IP), slaveURL)
			if !push {
				s.logMirrorSkip("slave "+m.IP, reason)
				continue
			}
			s.clearMirrorSkip("slave " + m.IP)
			s.logger.Info("zone mirror: re-forming slave (beta)", "slave", m.IP, "reason", reason)
		}
		rr := upnp.NewBoseRenderer(m.IP)
		var err error
		if lp.mime != "" {
			err = rr.PlayURLMime(ctx, slaveURL, lp.title, lp.art, lp.mime)
		} else {
			err = rr.PlayURL(ctx, slaveURL, lp.title, lp.art)
		}
		if err != nil {
			s.logger.Warn("zone mirror: slave play failed", "slave", m.IP, "err", err)
		} else {
			s.logger.Info("zone mirror: slave mirroring master stream (beta)", "slave", m.IP, "url", slaveURL)
		}
	}
}

// mirrorStreamPort is the port a SLAVE box uses to reach the master agent's
// stream proxy. The proxy listens on :8888, but a remote box cannot use that
// directly: on a BCO/whitelisted chassis (ST20 spotty/scm, Portable) the SMSC
// chipset drops an external :8888 connection, routing external TCP only to
// Bose-binary-owned listeners. Every chassis instead REDIRECTs :17008
// (SoftwareUpdate, whitelisted) to the agent's loopback :8888, which is exactly
// how the desktop app already reaches every box, so the mirror uses it too.
const mirrorStreamPort = "17008"

// mirrorURLForSlaves rewrites the master's own loopback stream URL
// (http://127.0.0.1:8888/...) into one a SLAVE box can fetch over the LAN: the
// master's LAN IP on the externally reachable :17008 redirect (mirrorStreamPort).
// masterIP comes from the persisted zone; when it is empty we fall back to the
// firmware /info IP. If no LAN IP can be resolved we return the URL unchanged
// (a no-op push beats pointing a slave at the wrong host).
func (s *Server) mirrorURLForSlaves(ctx context.Context, boxURL, masterIP string) string {
	u, err := url.Parse(boxURL)
	if err != nil {
		return boxURL
	}
	if strings.TrimSpace(masterIP) == "" {
		if info, ierr := boxapi.New(s.boxHost).GetInfo(ctx); ierr == nil {
			masterIP = strings.TrimSpace(info.IP)
		}
	}
	if masterIP == "" {
		return boxURL
	}
	u.Host = net.JoinHostPort(masterIP, mirrorStreamPort)
	return u.String()
}

// defaultZoneReconcilePath is the NAND flag file that opts a box INTO the
// periodic zone reconcile (#70 beta). Absent (the default) means OFF: the box
// never re-asserts a persisted native zone, so a speaker the user plays on its
// own is never dragged back into a group. Only an explicit "1"/"true"/"on"/"yes"
// turns it on. The default is OFF after multi-speaker users (Albrecht 5-box,
// Michal multi-ST10, 2026-06-19) reported standalone speakers being pulled into
// the master's zone every few minutes: when a member leaves to play its own
// source the master's match-before-assert guard sees a missing member and
// re-asserts setZone, dragging it back. On 0.8.x the native setZone does not even
// distribute (slaves never join, "master read-back empty"), so the periodic
// re-assert is pure churn with a real downside and no upside. Re-enable per box
// once the native path is verified on hardware (#70).
const defaultZoneReconcilePath = "/mnt/nv/streborn/zone-reconcile"

// zoneReconcileEnabled reports whether the periodic NATIVE zone re-assert runs on
// this box. Default OFF (opt-in): the flag file must explicitly say
// "1"/"true"/"on"/"yes" to turn it on. See defaultZoneReconcilePath for why the
// default flipped to OFF. A mirror zone is not gated here: its re-push has its
// own per-tick state guards (see mirrorToSlaves/slaveMirrorAction — the master
// must be actively playing, and standby or busy slaves are left alone, #342),
// so this gate only governs the broken/harmful native re-assert.
func (s *Server) zoneReconcileEnabled() bool {
	b, err := os.ReadFile(defaultZoneReconcilePath)
	if err != nil {
		return false // default OFF (opt-in)
	}
	switch strings.ToLower(strings.TrimSpace(string(b))) {
	case "1", "true", "on", "yes":
		return true // explicit opt-in
	default:
		return false
	}
}

// PeriodicZoneReconcile re-pushes a persisted mirror group so it survives
// reboot/standby/Wi-Fi outage (#70 beta), and re-asserts a native zone only when
// the box is opted in (see zoneReconcileEnabled, default OFF). No-op when
// standalone. Started by cmd/agent after the server is built. Lives on the Server
// so the mirror path can reach s.lastPlay + the UPnP renderer.
func (s *Server) PeriodicZoneReconcile() {
	if s.zones == nil || s.boxHost == "" {
		return
	}
	time.Sleep(45 * time.Second) // let the box finish booting
	s.reconcileZoneOnce()
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		s.reconcileZoneOnce()
	}
}

func (s *Server) reconcileZoneOnce() {
	z, ok := s.zones.Get()
	if !ok {
		return // standalone
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if z.Mirror() {
		// Re-form the mirror group, guarded (best-effort): the master must be
		// actively playing the mirrored stream, and a slave is only (re)pointed
		// when it is idle, dropped off the mirror, or on a stale master stream.
		// A standby or otherwise-busy speaker is left alone — the unguarded
		// version of this path hijacked a slave's Spotify playback with the
		// master's persisted last station every 5 minutes (#342). Not gated by
		// the native opt-in below; the guards make it safe on their own.
		s.mirrorToSlaves(ctx, z, true)
		return
	}
	if z.Stereo {
		// A left/right stereo pair is a firmware-native group, not a multiroom
		// zone. Re-asserting it with the zone API (/setZone) would use the wrong
		// endpoint and could fight the firmware's own pairing, so leave a native
		// stereo pair alone; the firmware persists it across reboot/standby itself.
		return
	}
	if !s.zoneReconcileEnabled() {
		// Native re-assert is opt-in (default OFF): re-asserting setZone whenever a
		// member is missing dragged solo speakers back into the group, and on 0.8.x
		// native zones do not distribute anyway. See zoneReconcileEnabled.
		return
	}
	// Native: only re-assert when the live zone does not already match.
	c := boxapi.New(s.boxHost)
	if live, err := c.GetZone(ctx); err == nil && live.Master == z.Master && len(live.Members) == len(z.Slaves) {
		return
	}
	master := boxapi.ZoneMember{DeviceID: z.Master, IP: z.MasterIP}
	slaves := make([]boxapi.ZoneMember, 0, len(z.Slaves))
	for _, m := range z.Slaves {
		slaves = append(slaves, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
	}
	s.logger.Info("zone reconcile: re-asserting native zone (beta)", "master", z.Master, "slaves", len(slaves))
	if err := c.SetZone(ctx, master, slaves); err != nil {
		s.logger.Warn("zone reconcile: setZone failed", "err", err, "master", z.Master)
	}
}

// handleZoneDissolve tears down the zone this box leads and stops re-forming it.
func (s *Server) handleZoneDissolve(w http.ResponseWriter, r *http.Request) {
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var master boxapi.ZoneMember
	var slaves []boxapi.ZoneMember
	stereo := false
	// Prefer the persisted membership; fall back to the live zone so a dissolve
	// still works after an agent restart.
	if s.zones != nil {
		if z, ok := s.zones.Get(); ok {
			master = boxapi.ZoneMember{DeviceID: z.Master, IP: z.MasterIP}
			stereo = z.Stereo
			for _, m := range z.Slaves {
				slaves = append(slaves, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
			}
		}
	}
	if stereo {
		// A stereo pair is a firmware-native L/R group, so tear it down with the
		// matching endpoint (GET /removeGroup), not the multiroom /removeZoneSlave.
		// Always clear our store afterwards so we stop honoring the pair.
		s.logger.Info("stereo: dissolving pair via /removeGroup (beta)", "master", master.DeviceID)
		if err := c.RemoveGroup(ctx); err != nil {
			s.logger.Warn("stereo: removeGroup failed (the user may need to undo the pair in the Bose app)", "err", err)
		}
		if s.zones != nil {
			if err := s.zones.Clear(); err != nil {
				s.logger.Warn("stereo: clear store failed", "err", err)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stereo": true})
		return
	}
	if master.DeviceID == "" {
		if z, err := c.GetZone(ctx); err == nil && z.Master != "" {
			master = boxapi.ZoneMember{DeviceID: z.Master, IP: z.SenderIP}
			for _, m := range z.Members {
				slaves = append(slaves, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
			}
		}
	}
	s.logger.Info("zone: dissolving (beta)", "master", master.DeviceID, "slaves", len(slaves))
	if master.DeviceID != "" && len(slaves) > 0 {
		// Loop until the firmware reports an empty zone (or the ctx deadline): a
		// single RemoveZoneSlave can leave a straggler, which forced a SECOND
		// ungroup press to clear the speaker's display (#70). Bounded by the 8s
		// ctx and a small attempt cap so a box that never lets go cannot hang.
		cur := slaves
		for attempt := 0; attempt < 4 && len(cur) > 0; attempt++ {
			if err := c.RemoveZoneSlave(ctx, master, cur); err != nil {
				// Log but keep going; the store is cleared below regardless so we
				// stop re-forming a broken zone.
				s.logger.Warn("zone: removeZoneSlave failed", "err", err, "attempt", attempt)
			}
			z, err := c.GetZone(ctx)
			if err != nil || z.Master == "" || len(z.Members) == 0 {
				break // zone gone (or unreadable): done
			}
			cur = z.Members
			s.logger.Info("zone: members still present after removeZoneSlave, retrying", "remaining", len(cur), "attempt", attempt)
		}
	}
	if s.zones != nil {
		if err := s.zones.Clear(); err != nil {
			s.logger.Warn("zone: clear store failed", "err", err)
		}
	}
	// Also clear the group from every member's own persisted store
	// (best-effort, background): a member that itself persisted a zone naming
	// this box would otherwise keep re-forming the group forever (#342).
	if master.DeviceID != "" || master.IP != "" {
		s.purgePeerZones(master, slaves)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleBoxGroup reads the box's current stereo pair group.
// Read-only. Response is {"id":"...","name":"...","members":[...]}.
// For a box without a pair, id is empty and members is empty.
func (s *Server) handleBoxGroup(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	g, err := c.GetGroup(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// sysBlockRoot, mediaRoot and nvRoot are the sysfs block-device root, the mount
// root, and the writable NAND root. They are vars (not consts) so the
// stick-detection test can point them at a temp tree; in production they are the
// real paths.
var (
	sysBlockRoot = "/sys/block"
	mediaRoot    = "/media"
	nvRoot       = "/mnt/nv"
)

// sshPersistentEnabled reports whether root SSH is configured to stay open across
// reboots on this box, independent of any inserted stick. Two persistent NAND
// markers count: STR's own opt-in (/mnt/nv/streborn/enable-ssh, honored by
// run.sh) and a maintainer-placed /mnt/nv/remote_services. Both live on NAND and
// survive a reboot, so when SSH is open because of one of them the "pull the
// stick and reboot to close it" advice is wrong — the box is deliberately left
// open (#381, #385). Transient stick-driven SSH (the /media/sda1 or /tmp
// remote_services marker) leaves neither file, so it still reads as "still
// inserted".
func sshPersistentEnabled() bool {
	for _, p := range []string{
		filepath.Join(nvRoot, "streborn", "enable-ssh"),
		filepath.Join(nvRoot, "remote_services"),
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// diskIsRemovableUSB reports whether the named block disk (e.g. "sda") is a
// REMOVABLE USB device, i.e. a USB stick, rather than the speaker's built-in
// storage. A disk qualifies when /sys/block/<disk>/removable reads "1", or when
// its sysfs device path sits on the USB bus. This is the fix for #105: several
// speakers (deqw's ST10 + both ST20s, no stick inserted) enumerate an INTERNAL
// disk as sda, so the old "any sd* block device exists" check raised the
// "remove the USB stick" banner permanently with nothing to remove.
func diskIsRemovableUSB(disk string) bool {
	base := filepath.Join(sysBlockRoot, disk)
	if _, err := os.Stat(base); err != nil {
		return false // no such disk
	}
	if b, err := os.ReadFile(filepath.Join(base, "removable")); err == nil &&
		strings.TrimSpace(string(b)) == "1" {
		return true
	}
	// Fallback: a USB stick's /sys/block/<disk> resolves through the USB bus, while
	// internal eMMC/SD/SATA does not. Some sticks report removable=0, so also
	// accept a USB device path.
	if real, err := filepath.EvalSymlinks(base); err == nil && strings.Contains(real, "/usb") {
		return true
	}
	return false
}

// stickReallyMounted reports whether a real STR USB stick is in the speaker right
// now, and returns its version.txt when readable. It requires POSITIVE proof: a
// readable STR marker on a mounted /media/<disk>1 filesystem. A bare removable /
// USB block device is NOT enough.
//
// #179: deqw's ST10 + both ST20s (no stick inserted) expose an internal disk as
// a removable/USB sda that is never mounted (the diagnostic showed no /media/sda1
// and no sd* mount at all), so diskIsRemovableUSB("sda") returned true and the
// old "removable USB present" check kept the "remove the USB stick" banner up
// forever with nothing to remove. Reading an STR marker off the mount instead
// keys on the one thing only a real, inserted STR stick produces.
func stickReallyMounted() (bool, string) {
	mnt := stickMountDir()
	if mnt == "" {
		return false, ""
	}
	// version.txt is the authoritative marker and carries the stick version.
	if b, err := os.ReadFile(filepath.Join(mnt, "version.txt")); err == nil {
		return true, strings.TrimSpace(string(b))
	}
	return true, ""
}

// stickMountDir returns the mount directory of a real, inserted STR stick, or
// "" when none is present. Same positive-proof contract as stickReallyMounted
// (#179): a readable STR marker on a mounted /media/<disk>1, never a bare
// removable/USB block device.
func stickMountDir() string {
	for _, disk := range []string{"sda", "sdb"} {
		if !diskIsRemovableUSB(disk) {
			continue
		}
		mnt := filepath.Join(mediaRoot, disk+"1")
		// Sticks that predate version.txt count via the STR stick layout itself.
		// All markers only exist on a real, inserted stick, so the #179 phantom
		// sda with no mount stays false.
		for _, marker := range []string{"version.txt", "install.sh", "run.sh", "streborn-armv7l"} {
			if _, err := os.Stat(filepath.Join(mnt, marker)); err == nil {
				return mnt
			}
		}
	}
	return ""
}

// handleStickStatus reports whether the USB stick is actually in the box right
// now, plus the stick version when readable. It must NOT use a bare
// os.Stat("/media/sda1")+IsDir: the box leaves the empty mountpoint directory
// behind after `umount` (run.sh cleanup), so IsDir kept reporting mounted:true
// forever after the stick was pulled, which made the "remove the USB stick and
// restart" banner stick around permanently even with the stick already out
// (#105). stickReallyMounted requires real evidence instead.
func (s *Server) handleStickStatus(w http.ResponseWriter, _ *http.Request) {
	mounted, version := stickReallyMounted()
	out := map[string]any{"mounted": mounted}
	if mounted && version != "" {
		out["version"] = version
	}
	// SSH status — check whether port 22 is currently listening. If so
	// someone on the LAN can access the box, the app shows a warning banner.
	// We try a TCP connect to localhost with a 200 ms timeout.
	if conn, dialErr := net.DialTimeout("tcp", "127.0.0.1:22", 200*time.Millisecond); dialErr == nil {
		_ = conn.Close()
		out["sshOpen"] = true
		// Distinguish transient stick-driven SSH (closes on the next stickless
		// reboot) from a persistent NAND opt-in (survives reboots). The app uses
		// this to stop telling remote_services users to "pull the stick and
		// reboot" when no stick is involved and a reboot would not close SSH.
		if sshPersistentEnabled() {
			out["sshPersistent"] = true
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDebugState returns important box state files as JSON so we can
// debug from outside without SSH when the stick is installed.
//
// Used only for interactive diagnosis — the app itself does not call
// this regularly. Limit per file: 8 KB so the response stays compact.
func (s *Server) handleDebugState(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "debug only from LAN", http.StatusForbidden)
		return
	}
	const maxRead = 8 * 1024
	readTail := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			return "ERR: " + err.Error()
		}
		if len(b) > maxRead {
			return "...(truncated)\n" + string(b[len(b)-maxRead:])
		}
		return string(b)
	}
	listDir := func(path string) []string {
		entries, err := os.ReadDir(path)
		if err != nil {
			return []string{"ERR: " + err.Error()}
		}
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			fi, _ := e.Info()
			size := int64(0)
			if fi != nil {
				size = fi.Size()
			}
			out = append(out, fmt.Sprintf("%s  %d  %s", e.Type().String(), size, e.Name()))
		}
		return out
	}

	state := map[string]any{
		"agent_log_tail": readTail("/tmp/streborn-agent.log"),
		"previous_log":   readTail("/mnt/nv/streborn/previous.log"),
		"setup_log":      readTail("/mnt/nv/streborn/setup.log"),
		"boot_log":       readTail("/mnt/nv/streborn/boot.log"),
		"wpa_supplicant": readTail("/mnt/nv/wpa_supplicant.conf"),
		"region_txt":     readTail("/mnt/nv/streborn/region.txt"),
		"name_txt":       readTail("/mnt/nv/streborn/name.txt"),
		"stick_listing":  listDir("/media/sda1"),
		"media_listing":  listDir("/media"),
		"nv_listing":     listDir("/mnt/nv/streborn"),
		// The /mnt/nv ROOT, not just STR's own subdir: a stock or STR-only box
		// carries only Bose's persistent state and streborn/ here, so anything
		// else (e.g. an aftertouch/ dir) is a leftover from another mod that can
		// clash with STR's Wi-Fi/marge path. Surfacing it lets a bundle spot such
		// remnants without SSH (do NOT blanket-wipe /mnt/nv: it holds the box's
		// own Wi-Fi/AirPlay/account persistence).
		"nv_root_listing": listDir("/mnt/nv"),
		"proc_mounts":     readTail("/proc/mounts"),
		// Writable-volume usage: df for /mnt/nv + / and the per-entry sizes that
		// answer "is this box genuinely tighter or carrying foreign firmware
		// leftovers" without needing SSH (#ST30 OTA no-space, 2026-06-24).
		"disk_usage": nandInventory(),
	}
	// Preset store summary: one compact line per slot so a diagnostic bundle
	// shows dead presets (empty/invalid stream URL) directly. Before this the
	// store's content was invisible in bundles and a preset that saved wrong
	// could only be diagnosed by asking the user to fetch presets.json (#252).
	// Stream URLs are included in full; the app's exporter anonymizes bundles.
	if s.presets != nil {
		all := s.presets.All()
		lines := make([]string, 0, len(all))
		for _, p := range all {
			lines = append(lines, fmt.Sprintf("slot %d: type=%s name=%q codec=%q stream=%q uri=%q items=%d",
				p.Slot, p.Type, p.Name, p.Codec, p.StreamURL, p.URI, len(p.Items)))
		}
		state["presets"] = lines
	}
	writeJSON(w, http.StatusOK, state)
}

// handleDebugProbe issues an HTTP request from inside the box to a
// caller-supplied URL and returns the raw response (status, headers,
// body) as JSON. Built as a temporary diagnostic to verify whether
// the BCO wifi-chipset HTTP responder on :80 also answers on the
// loopback interface (it is documented as not having a Linux socket,
// but the chipset may intercept lo traffic too). LAN-only, 5 s
// timeout, body capped to keep the JSON small.
//
// Query parameters:
//
//	url     full URL to probe (required)
//	method  HTTP method (default GET)
//	body    request body, sent verbatim
func (s *Server) handleDebugProbe(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "debug only from LAN", http.StatusForbidden)
		return
	}
	target := r.URL.Query().Get("url")
	if target == "" {
		http.Error(w, "missing url query parameter", http.StatusBadRequest)
		return
	}
	// Scheme gate: this is a deliberate diagnostic that DOES probe the box's own
	// loopback services (Bose :8090, STR :8888), so we do NOT use the SSRF dial
	// guard here, but we still reject non-http(s) schemes (file://, gopher://,
	// ...) so the LAN-gated debug endpoint cannot be turned into a local-file or
	// arbitrary-protocol reader.
	if err := netutil.SafeHTTPURL(target); err != nil {
		http.Error(w, "invalid url: "+err.Error(), http.StatusBadRequest)
		return
	}
	method := r.URL.Query().Get("method")
	if method == "" {
		method = http.MethodGet
	}
	bodyStr := r.URL.Query().Get("body")
	timeoutSec := 5
	if t := r.URL.Query().Get("timeout"); t != "" {
		if v, err := strconv.Atoi(t); err == nil && v >= 1 && v <= 120 {
			timeoutSec = v
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	var reqBody io.Reader
	if bodyStr != "" {
		reqBody = strings.NewReader(bodyStr)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reqBody)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"target": target,
			"phase":  "build-request",
			"error":  err.Error(),
		})
		return
	}
	client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"target": target,
			"method": method,
			"phase":  "do-request",
			"error":  err.Error(),
		})
		return
	}
	defer resp.Body.Close()
	const maxBody = 8 * 1024
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	hdrs := map[string]string{}
	for k, v := range resp.Header {
		if len(v) > 0 {
			hdrs[k] = v[0]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"target":      target,
		"method":      method,
		"status_code": resp.StatusCode,
		"status":      resp.Status,
		"headers":     hdrs,
		"body":        string(respBody),
		"body_bytes":  len(respBody),
	})
}

// boxPresetURL returns the stable agent-loopback URL the box should store for a
// preset slot, so a hardware press streams through STR's proxy (which survives
// CDN token expiry) rather than the raw CDN URL. Spotify presets point at the
// per-slot Ogg endpoint because /stream/<slot> has no Spotify source and the
// box's own activation would otherwise flash "service unavailable" (#22). This
// is the one place the box-side preset location is built; both the per-slot
// SetSlot sync and the bulk handleBoxSyncPresets go through it.
func boxPresetURL(slot int, isSpotify bool) string {
	return boxurl.Preset(slot, isSpotify)
}

// handleBoxSyncPresets overwrites the box's own preset list with all
// current stick presets via the Bose CLI. This makes hardware buttons
// 1-6 work again when the initial sync at boot did not run for some
// reason (e.g. the box was not yet reachable).
func (s *Server) handleBoxSyncPresets(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.presets == nil || s.boxHost == "" {
		http.Error(w, "presets store or box host not configured", http.StatusServiceUnavailable)
		return
	}
	var specs []boxcli.PresetSpec
	for _, p := range s.presets.All() {
		// Push the agent-loopback proxy URL, NOT p.StreamURL. The raw value is
		// the CDN URL (or, post-v0.7.16, the self-proxy wrapper); storing it on
		// the box defeats the whole point of the proxy slot (token-expiry
		// survival) and a Spotify preset would have no playable box-side source
		// at all. This path must match the per-slot SetSlot sync above.
		specs = append(specs, boxcli.PresetSpec{
			Slot:      p.Slot,
			Name:      p.Name,
			StreamURL: boxPresetURL(p.Slot, p.Type == "spotify"),
		})
	}
	syncCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	errs := boxcli.SyncAllPresets(syncCtx, s.boxHost, specs)
	var failed []int
	for slot, err := range errs {
		if err != nil {
			failed = append(failed, slot)
			s.logger.Warn("preset sync failed", "slot", slot, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"synced": len(specs) - len(failed),
		"failed": failed,
	})
}

// handleBoxReboot restarts the box via shell `reboot`. Used so that
// conf files from the stick (wlan / region / name) are applied on the
// run.sh boot path — this avoids a permanently running USB watcher
// polling loop.
func (s *Server) handleBoxReboot(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "reboot only allowed from LAN", http.StatusForbidden)
		return
	}
	s.logger.Info("Box reboot requested by user")
	writeJSON(w, http.StatusOK, map[string]string{"status": "rebooting"})
	// Execute 1s later so our HTTP response still gets out.
	go func() {
		time.Sleep(1 * time.Second)
		_ = exec.Command("reboot").Run()
	}()
}

// handleBoxAirplayOpt reads or sets the "AirPlay optimization" setting,
// which is the iOS app's advanced toggle stored as the
// BCOResetTimerEnabled attribute on the <AirplayConfiguration> root in
// /mnt/nv/BoseApp-Persistence/<N>/AirplayConfiguration.xml (a BCO
// coprocessor keepalive). It exists only on BCO speakers (Portable,
// ST20-spotty); on other models the file is absent and GET reports
// supported=false. The value is read by BoseApp at boot, so a POST
// rewrites the attribute and reboots, exactly like the iOS app.
func (s *Server) handleBoxAirplayOpt(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	var path string
	if m, _ := filepath.Glob("/mnt/nv/BoseApp-Persistence/*/AirplayConfiguration.xml"); len(m) > 0 {
		path = m[0]
	}
	switch r.Method {
	case http.MethodGet:
		if path == "" {
			writeJSON(w, http.StatusOK, map[string]any{"supported": false})
			return
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"supported": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"supported": true,
			"enabled":   strings.Contains(string(raw), `BCOResetTimerEnabled="true"`),
		})
	case http.MethodPost:
		if path == "" {
			http.Error(w, "no AirplayConfiguration.xml (not a BCO speaker)", http.StatusBadRequest)
			return
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		val := "false"
		if body.Enabled {
			val = "true"
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "read: "+err.Error(), http.StatusInternalServerError)
			return
		}
		out := string(raw)
		switch {
		case strings.Contains(out, `BCOResetTimerEnabled="true"`):
			out = strings.ReplaceAll(out, `BCOResetTimerEnabled="true"`, `BCOResetTimerEnabled="`+val+`"`)
		case strings.Contains(out, `BCOResetTimerEnabled="false"`):
			out = strings.ReplaceAll(out, `BCOResetTimerEnabled="false"`, `BCOResetTimerEnabled="`+val+`"`)
		default:
			// Attribute absent (older file): add it to the root element.
			out = strings.Replace(out, "<AirplayConfiguration ", `<AirplayConfiguration BCOResetTimerEnabled="`+val+`" `, 1)
		}
		tmp := path + ".str-new"
		if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
			http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			http.Error(w, "rename: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = exec.Command("sync").Run()
		s.logger.Info("airplay-opt set, rebooting to apply", "enabled", body.Enabled)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": body.Enabled, "rebooting": true})
		// BoseApp reads BCOResetTimerEnabled at boot, so reboot to apply
		// (the iOS app does the same). Delay so the response flushes.
		go func() {
			time.Sleep(1500 * time.Millisecond)
			_ = exec.Command("reboot").Run()
		}()
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleResumeOnPowerOn reads or sets the per-box "resume the last station when
// the speaker is switched on" preference (default ON). Stored as a plain flag
// file on NAND ("1" / "0"), like region.txt, so it survives reboots. GET returns
// {supported, enabled}; POST {enabled} persists it. This is the opt-out for the
// power-on resume: a real power press brings back the last stream unless the user
// turns it off here.
func (s *Server) handleResumeOnPowerOn(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"supported": true, "enabled": s.resumeOnPowerOnEnabled()})
	case http.MethodPost:
		var body struct {
			Enabled bool `json:"enabled"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		path := s.resumeOnPowerOnPath
		if path == "" {
			path = defaultResumeOnPowerOnPath
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
			return
		}
		val := "1"
		if !body.Enabled {
			val = "0"
		}
		tmp := path + ".str-new"
		if err := os.WriteFile(tmp, []byte(val+"\n"), 0o644); err != nil {
			http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			http.Error(w, "rename: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = exec.Command("sync").Run()
		s.logger.Info("resume-on-power-on set", "enabled", body.Enabled)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": body.Enabled})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleDisplayTrack reads or sets the per-box "show the live radio track on the
// speaker's display" opt-in (default OFF). Stored as a plain NAND flag file
// ("1"/"0"). GET returns {supported, enabled}; POST {enabled} persists it.
// Enabling it makes STR re-push the now-playing metadata on each ICY title
// change, which briefly re-buffers the box, so it is the user's explicit choice.
func (s *Server) handleDisplayTrack(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"supported": true, "enabled": s.displayTrackEnabled(), "mode": s.displayTrackMode()})
	case http.MethodPost:
		var body struct {
			Enabled bool   `json:"enabled"`
			Mode    string `json:"mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		path := s.displayTrackPath
		if path == "" {
			path = defaultDisplayTrackPath
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
			return
		}
		val := "0"
		if body.Enabled {
			val = "1"
		}
		if err := writeFlagFile(path, val); err != nil {
			http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Persist the display mode too, when a valid one is supplied.
		mode := strings.ToLower(strings.TrimSpace(body.Mode))
		if mode == "title" || mode == "artist" || mode == "both" {
			if err := writeFlagFile(modePathFor(path), mode); err != nil {
				http.Error(w, "write mode: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		_ = exec.Command("sync").Run()
		s.logger.Info("display-track set", "enabled", body.Enabled, "mode", s.displayTrackMode())
		// Update the speaker display right away instead of waiting for the next
		// song: enable / mode change pushes the current title; disable reverts to
		// the box's default text. Async so the POST returns before the box I/O.
		if body.Enabled {
			go s.pushDisplayNow()
		} else {
			go s.pushDisplayDefault()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": body.Enabled, "mode": s.displayTrackMode()})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// writeFlagFile atomically writes a one-line value to a NAND flag file.
func writeFlagFile(path, val string) error {
	tmp := path + ".str-new"
	if err := os.WriteFile(tmp, []byte(val+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// NoteBoxPresets records the box's own preset list (from the gabbo
// presetsUpdated frame, via the agent). Replaces the previous snapshot wholesale;
// the box always reports the full list. A slot the user just deleted is dropped
// while its tombstone is fresh, so a trailing presetsUpdated does not resurrect
// it before the box-side removal settles.
func (s *Server) NoteBoxPresets(ps []BoxPreset) {
	s.boxPresetsMu.Lock()
	defer s.boxPresetsMu.Unlock()
	now := time.Now()
	for slot, when := range s.deletedBoxSlots {
		if now.Sub(when) > boxPresetTombstoneTTL {
			delete(s.deletedBoxSlots, slot)
		}
	}
	if len(s.deletedBoxSlots) == 0 {
		s.boxPresets = ps
		return
	}
	filtered := make([]BoxPreset, 0, len(ps))
	for _, p := range ps {
		if _, tombstoned := s.deletedBoxSlots[p.Slot]; tombstoned {
			continue
		}
		filtered = append(filtered, p)
	}
	s.boxPresets = filtered
}

// forgetBoxPreset removes a slot from the box-preset snapshot immediately and
// tombstones it, so a just-deleted preset disappears from the app's merged view
// at once and a trailing box presetsUpdated cannot bring it straight back.
func (s *Server) forgetBoxPreset(slot int) {
	s.boxPresetsMu.Lock()
	defer s.boxPresetsMu.Unlock()
	if s.deletedBoxSlots == nil {
		s.deletedBoxSlots = make(map[int]time.Time)
	}
	s.deletedBoxSlots[slot] = time.Now()
	if len(s.boxPresets) == 0 {
		return
	}
	kept := make([]BoxPreset, 0, len(s.boxPresets))
	for _, p := range s.boxPresets {
		if p.Slot == slot {
			continue
		}
		kept = append(kept, p)
	}
	s.boxPresets = kept
}

// handleBoxPresets serves the box's OWN presets (incl. foreign sources like
// DEEZER that STR did not set), so the app can show and preserve them and recall
// a foreign one via the hardware preset key (Option C). Oldest source of truth is
// the box's gabbo presetsUpdated frame; empty until the box has reported once.
func (s *Server) handleBoxPresets(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	s.boxPresetsMu.Lock()
	// make() never returns nil, so an empty list still marshals to [] not null.
	out := make([]BoxPreset, len(s.boxPresets))
	copy(out, s.boxPresets)
	s.boxPresetsMu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// handleBoxSnapshot serves the pre-takeover snapshot of the box's presets +
// sources (internal/boxsnapshot) so the app can warn about account-linked cloud
// sources (Deezer, ...) STR cannot carry over and show what was there. Returns
// {"captured":false} when no snapshot exists (feature off, or the box answered
// nothing capturable), so the app can tell "checked, none" from "not yet".
func (s *Server) handleBoxSnapshot(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if s.snapshotPath == "" {
		writeJSON(w, http.StatusOK, map[string]any{"captured": false})
		return
	}
	data, err := os.ReadFile(s.snapshotPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"captured": false})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// serviceKeyOf normalises a source name to the key SourceStatuses uses: upper-
// cased and truncated at the first underscore, so e.g. DEEZER_HIFI maps to the
// same DEEZER status entry.
func serviceKeyOf(source string) string {
	k := strings.ToUpper(strings.TrimSpace(source))
	if i := strings.IndexByte(k, '_'); i > 0 {
		k = k[:i]
	}
	return k
}

// partitionRestorable splits account-linked cloud presets by whether the box's
// own saved login for their service is still valid. A preset whose source the
// box already reports UNAVAILABLE must NOT be written back: the firmware drops a
// preset bound to a dead source within seconds, so writing it only makes the
// button flash and vanish (a reporter watched exactly that). Those services are
// returned as expired (normalised + deduplicated) so the caller reports them
// honestly instead of claiming a restore a reboot can never make stick. A source
// the box does not list at all counts as restorable (unknown, not proven dead).
func partitionRestorable(cloud []boxsnapshot.Preset, statuses map[string]string) (writable []boxsnapshot.Preset, expired []string) {
	expSet := map[string]bool{}
	for _, p := range cloud {
		key := serviceKeyOf(p.Source)
		if statuses[key] == "UNAVAILABLE" {
			expSet[key] = true
			continue
		}
		writable = append(writable, p)
	}
	for k := range expSet {
		expired = append(expired, k)
	}
	return writable, expired
}

// handleBoxSnapshotRestore (EXPERIMENTAL) writes account-linked cloud presets
// (e.g. Deezer) back onto their original slots and re-advertises their sources
// via the reflect-sources file, so the box plays them again through its own
// cached account token. Source of the presets: a posted box /presets XML the
// user saved (presetsXML), or the agent's snapshot when no XML is given. The box
// usually needs a reboot afterwards to re-sync the restored source, so the
// response sets rebootRecommended. LAN-only (it writes to the box).
func (s *Server) handleBoxSnapshotRestore(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "restore only allowed from LAN", http.StatusForbidden)
		return
	}
	if s.boxHost == "" {
		http.Error(w, "box host not configured", http.StatusServiceUnavailable)
		return
	}
	// Body is optional: read raw so an empty body falls back to the snapshot.
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 512*1024))
	var body struct {
		PresetsXML string `json:"presetsXML"`
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
	}

	var presets []boxsnapshot.Preset
	switch {
	case strings.TrimSpace(body.PresetsXML) != "":
		p, err := boxsnapshot.ParsePresetsXML([]byte(body.PresetsXML))
		if err != nil {
			http.Error(w, "could not parse presets XML: "+err.Error(), http.StatusBadRequest)
			return
		}
		presets = p
	case s.snapshotPath != "":
		if snap, err := boxsnapshot.Load(s.snapshotPath); err == nil {
			presets = snap.Presets
		}
	}

	cloud := boxsnapshot.CloudPresets(presets)
	if len(cloud) == 0 {
		// Distinguish "I read buttons but none are account-bound" from "I could
		// not read any buttons at all" (usually a paste of the wrong text), so the
		// UI can give precise guidance instead of one ambiguous message. parsed=0
		// is the case a single <preset> block used to hit before ParsePresetsXML
		// learned to wrap it.
		writeJSON(w, http.StatusOK, map[string]any{
			"restored": []int{}, "parsed": len(presets),
			"message": "no account-linked cloud presets found to restore",
		})
		return
	}

	// Read the box's CURRENT source availability BEFORE writing anything. STR can
	// re-assert a cloud preset (Deezer, ...) only while the box's own saved login
	// for that service is still valid. Once that login has expired with the Bose
	// cloud the source reports UNAVAILABLE, and the firmware then drops any preset
	// bound to it within seconds, so writing such a button just makes it appear and
	// vanish (a reporter watched exactly that on :8090/presets). Detect the expired
	// services up front and DO NOT write their buttons; report them honestly
	// instead. A reboot cannot revive an expired box-side login.
	statuses, _ := boxsnapshot.SourceStatuses(r.Context(), s.boxHost)
	writable, expired := partitionRestorable(cloud, statuses)

	// Re-advertise the still-valid sources so a reboot re-registers them (Path A);
	// harmless for the expired ones.
	if s.reflectPath != "" {
		if err := boxsnapshot.MergeReflect(s.reflectPath, boxsnapshot.ReflectFromPresets(cloud)); err != nil {
			s.logger.Warn("restore: reflect-sources merge failed", "err", err)
		}
	}

	restored := []int{}
	failed := map[string]string{}
	services := map[string]bool{}
	for _, p := range writable {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		err := boxcli.AddPresetRaw(ctx, s.boxHost, p.Slot, p.Source, p.Type, p.Location, p.Name, p.SourceAccount)
		cancel()
		if err != nil {
			failed[fmt.Sprintf("%d", p.Slot)] = err.Error()
			continue
		}
		restored = append(restored, p.Slot)
		services[strings.ToUpper(p.Source)] = true
	}
	svcList := make([]string, 0, len(services))
	for k := range services {
		svcList = append(svcList, k)
	}
	// A source that was valid at the pre-check can still come back UNAVAILABLE if
	// the box rejected it on write: re-read and report those too, kept distinct
	// from the expired ones we never wrote.
	unavailable := []string{}
	if post, err := boxsnapshot.SourceStatuses(r.Context(), s.boxHost); err == nil {
		for svc := range services {
			if post[serviceKeyOf(svc)] == "UNAVAILABLE" {
				unavailable = append(unavailable, svc)
			}
		}
	}
	// A reboot only helps when a button was actually written to a still-valid
	// source; it cannot revive an expired login, so do not offer it otherwise.
	rebootRecommended := len(restored) > 0
	s.logger.Info("box snapshot restore (experimental)", "restored", restored, "failed", len(failed), "services", svcList, "unavailable", unavailable, "expired", expired)
	writeJSON(w, http.StatusOK, map[string]any{
		"restored":          restored,
		"failed":            failed,
		"services":          svcList,
		"unavailable":       unavailable,
		"expired":           expired,
		"rebootRecommended": rebootRecommended,
	})
}

// handleBoxPresetRecall plays one of the box's OWN presets by pressing its
// hardware preset key over the TAP CLI (POST {slot}). The box then plays that
// slot through its own source, which is how a foreign preset (Deezer, played via
// the box's cached account) is recalled from the app without STR having to be a
// Deezer player (Option C). For STR-managed presets the app uses /api/play/<slot>
// instead; this is specifically the path for box-native ones.
func (s *Server) handleBoxPresetRecall(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.boxHost == "" {
		http.Error(w, "box host not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Slot int `json:"slot"`
	}
	if !decodeJSONRequest(w, r, 1<<10, &body) {
		return
	}
	if body.Slot < 1 || body.Slot > 6 {
		http.Error(w, "slot must be 1..6", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := boxcli.PresetKey(ctx, s.boxHost, body.Slot, "p"); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "could not recall preset", "detail": err.Error(), "slot": body.Slot})
		return
	}
	s.logger.Info("box preset recalled via hardware key", "slot", body.Slot)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "slot": body.Slot})
}

// handleBoxWLAN sets the box's WLAN configuration at runtime.
// Body: {"ssid":"...", "password":"..."}
//
// Robust across the two Wi-Fi stacks SoundTouch ships (see run.sh's WLAN
// section):
//   - wpa_supplicant boxes (wlan0): the new network is applied LIVE via wpa_cli
//     reconfigure, verified, and rolled back to the previous network if it does
//     not associate, so a wrong password leaves the box on its old Wi-Fi rather
//     than stranded.
//   - BCO boxes (eth0, e.g. Portable): no usable runtime channel exists
//     (wpa_supplicant is absent), so the credentials are persisted and the box
//     reboots to apply them through the proven boot-time provisioning path.
//
// In ALL cases the SSID/PASS are written to the canonical NAND wlan-creds that
// the boot path replays, so the change survives a reboot. The previous version
// only wrote a runtime wpa file at the wrong path and never updated wlan-creds,
// so a reboot reverted it AND on BCO boxes it poked a non-existent
// wpa_supplicant and silently did nothing while reporting success.
//
// The switch runs in the background and the response returns immediately: the
// box leaves the current network as it switches, so the client must rediscover
// it on the new IP rather than wait on this request. LAN-only so a stray
// internet call can never move the speaker's Wi-Fi.
func (s *Server) handleBoxWLAN(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPut) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "wlan switch only allowed from LAN", http.StatusForbidden)
		return
	}
	var req struct {
		SSID     string `json:"ssid"`
		Password string `json:"password"`
		// Force skips the site-survey pre-flight (the user chose to switch to a
		// network the speaker cannot currently see, e.g. a momentarily-missed one).
		Force bool `json:"force"`
		// Hidden marks the target as a hidden network (SSID broadcast disabled).
		// A hidden SSID never appears in the box's site survey, so it implies
		// skipping the pre-flight, and the wpa config gains scan_ssid=1 so
		// wpa_supplicant probes for the SSID directly instead of waiting for a
		// beacon that never carries it.
		Hidden bool `json:"hidden"`
	}
	if !decodeJSONRequest(w, r, 2048, &req) {
		return
	}
	req.SSID = strings.TrimSpace(req.SSID)
	if req.SSID == "" {
		http.Error(w, "ssid must not be empty", http.StatusBadRequest)
		return
	}
	// WPA requires a PSK of at least 8 characters; an empty password means an
	// open network (key_mgmt=NONE in buildWPAConfig).
	if req.Password != "" && len(req.Password) < 8 {
		http.Error(w, "password too short (at least 8 characters)", http.StatusBadRequest)
		return
	}

	// Pre-flight: confirm the box can actually SEE the target network before the
	// switch. SoundTouch speakers are 2.4 GHz only, so pointing one at a 5 GHz-only
	// network strands it (it leaves the current network, cannot join the new one,
	// and then needs a Bose-app re-pair). The box's own site survey only lists the
	// bands it supports, so an invisible SSID is the clean signal to refuse; the
	// `force` flag lets the user override a momentarily-missed but real network,
	// and `hidden` implies the same skip: a hidden SSID is invisible to the
	// survey BY DESIGN, so refusing on invisibility would refuse every hidden
	// network forever.
	if wlanPreflightApplies(req.Force, req.Hidden) {
		sctx, scancel := context.WithTimeout(r.Context(), 12*time.Second)
		ssids, serr := boxapi.New(s.boxHost).SiteSurvey(sctx)
		scancel()
		if serr != nil {
			s.logger.Warn("WLAN switch preflight: site survey failed, proceeding without it", "err", serr)
		} else {
			visible := false
			for _, sid := range ssids {
				if sid == req.SSID {
					visible = true
					break
				}
			}
			if !visible {
				s.logger.Info("WLAN switch refused: target SSID not visible to the speaker", "ssid", req.SSID, "visible", ssids)
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error":   "The speaker can't see that network. SoundTouch speakers only support 2.4 GHz Wi-Fi, so a 5 GHz network will not work. Pick a network the speaker can see, or switch anyway.",
					"code":    "ssid-not-visible",
					"ssid":    req.SSID,
					"visible": ssids,
				})
				return
			}
		}
	}

	iface, mech := detectWlanMechanism()
	// Persist to NAND (with .bak backup) BEFORE responding: the response triggers
	// the client to rediscover the box on its new network, and the actual switch
	// runs in a background goroutine, so committing the canonical creds first
	// means a crash after the response can never leave the client believing the
	// switch happened while NAND still holds the old creds.
	if err := backupAndWriteWlanCreds(req.SSID, req.Password, req.Hidden); err != nil {
		http.Error(w, "persist wlan creds: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Respond before switching: the box drops off the current network mid-switch,
	// so the client rediscovers it on its new IP instead of waiting on this socket.
	status := "switching"
	if mech == "bco" {
		status = "rebooting"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":    status,
		"ssid":      req.SSID,
		"mechanism": mech,
	})
	s.logger.Info("WLAN switch requested", "ssid", req.SSID, "mechanism", mech, "iface", iface, "hidden", req.Hidden)
	go s.applyWLANChange(iface, mech, req.SSID, req.Password, req.Hidden)
}

// wlanPreflightApplies reports whether the site-survey visibility pre-flight
// should run for a WLAN change request. `force` is the user's explicit
// override; `hidden` networks never show up in a site survey, so the
// pre-flight is meaningless for them and must be skipped.
func wlanPreflightApplies(force, hidden bool) bool {
	return !force && !hidden
}

const (
	wlanCredsPath = "/mnt/nv/streborn/wlan-creds"
	wpaConfPath   = "/etc/wpa_supplicant.conf"
	// wpaBackupPath holds the rollback copy of the live wpa_supplicant.conf on
	// writable NAND. /etc is read-only on several chassis (rhino/scm), so the
	// backup cannot live next to the conf: writing it to /etc is exactly what
	// aborted runtime Wi-Fi switches before ("read-only file system"). run.sh's
	// M3 boot path uses the same NAND location.
	wpaBackupPath = "/mnt/nv/streborn/wpa_supplicant.conf.bak"
	// wlanApplyMarkerPath is a one-shot marker the app-initiated Wi-Fi change
	// drops before a reboot so run.sh's boot path treats that boot as an active
	// "program the new SSID" provision instead of a passive replay of the current
	// network. run.sh deletes it on read, so a wrong password cannot loop (#184).
	wlanApplyMarkerPath = "/mnt/nv/streborn/.wlan-apply-pending"
)

// touchWLANApplyMarker drops the one-shot boot marker that makes run.sh actively
// provision the new Wi-Fi after an app "apply now" change, rather than replaying
// the old network and exiting hands-off (which left an app Wi-Fi change dead-
// ending when the old AP was still in range, #184).
func touchWLANApplyMarker() {
	if err := os.WriteFile(wlanApplyMarkerPath, []byte("1\n"), 0o600); err != nil {
		// Non-fatal: without the marker the boot falls back to the old replay
		// behaviour, i.e. the pre-fix behaviour, never worse.
		return
	}
}

// detectWlanMechanism mirrors run.sh's interface detection: a wlan* iface means
// a wpa_supplicant stack (live switch possible); eth0-only is the BCO pattern
// (Wi-Fi via the chip exposed as eth0, no wpa_supplicant -> reboot to apply).
func detectWlanMechanism() (iface, mech string) {
	for _, w := range []string{"wlan0", "wlan1"} {
		if _, err := os.Stat("/sys/class/net/" + w); err == nil {
			return w, "wpa"
		}
	}
	return "eth0", "bco"
}

// applyWLANChange applies the (already persisted) credentials by the box's
// mechanism. Serialized by wlanMu so two switches cannot interleave their writes
// to wlan-creds / wpa_supplicant.conf and leave the box on an unpredictable
// network. The creds were committed synchronously by the handler before this
// runs, so this only drives the live switch / reboot.
func (s *Server) applyWLANChange(iface, mech, ssid, password string, hidden bool) {
	s.wlanMu.Lock()
	defer s.wlanMu.Unlock()
	switch mech {
	case "wpa":
		switch s.applyWlanWPALive(iface, ssid, password, hidden) {
		case wpaConfirmed:
			s.logger.Info("WLAN: live switch confirmed", "ssid", ssid, "iface", iface)
			_ = os.Remove(wlanCredsPath + ".bak")
			_ = os.Remove(wpaBackupPath)
		case wpaCannotApply:
			// The conf could not be written live (read-only /etc and the
			// bind-mount overlay both failed). The new creds are already on NAND,
			// so reboot and let run.sh's boot path provision them (M3 applies the
			// same bind workaround from a clean boot). Keep the new creds: do NOT
			// roll back.
			s.logger.Warn("WLAN: cannot switch live, rebooting to apply new network via boot path", "ssid", ssid)
			// Drop the rollback backup before the reboot: we are committing the new
			// creds via the boot path, so a stale backup must not survive on NAND and
			// be used to roll a future switch back to this now-superseded conf.
			_ = os.Remove(wpaBackupPath)
			// Mark this as an active apply so run.sh programs the new SSID on boot
			// instead of replaying the old network hands-off (#184).
			touchWLANApplyMarker()
			rebootBox()
		default: // wpaNotAssociated
			// Did not associate (e.g. wrong password): roll all the way back so
			// the box stays on its previous network instead of unreachable. The
			// agent runs ON the box, so it can do this even while the box is
			// briefly off the LAN.
			s.logger.Warn("WLAN: new network did not associate, rolling back to previous", "ssid", ssid)
			restoreWlanCreds()
			s.restoreWPAConfAndReload(iface)
		}
	default:
		s.logger.Info("WLAN: BCO chassis, rebooting to apply via boot path", "ssid", ssid)
		// Mark this as an active apply so run.sh programs the new SSID on boot
		// instead of replaying the old network hands-off (#184).
		touchWLANApplyMarker()
		rebootBox()
	}
}

// backupAndWriteWlanCreds writes the canonical NAND wlan-creds (the SSID=/PASS=
// format the boot path replays, plus HIDDEN=1 for hidden networks), keeping the
// previous set as .bak for rollback.
func backupAndWriteWlanCreds(ssid, password string, hidden bool) error {
	_ = os.Rename(wlanCredsPath, wlanCredsPath+".bak") // best-effort backup
	return writeWlanCredsFile(wlanCredsPath, ssid, password, hidden)
}

// writeWlanCredsFile is the path-injectable core of backupAndWriteWlanCreds,
// kept separate so the file format stays unit-testable off-box. run.sh's boot
// replay parses these exact SSID=/PASS=/HIDDEN= lines.
func writeWlanCredsFile(path, ssid, password string, hidden bool) error {
	body := fmt.Sprintf("SSID=%s\nPASS=%s\n", ssid, password)
	if hidden {
		body += "HIDDEN=1\n"
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func restoreWlanCreds() {
	if _, err := os.Stat(wlanCredsPath + ".bak"); err == nil {
		_ = os.Rename(wlanCredsPath+".bak", wlanCredsPath)
	}
}

// wpaApplyResult reports how a live wpa switch ended so applyWLANChange can pick
// the right recovery.
type wpaApplyResult int

const (
	wpaConfirmed     wpaApplyResult = iota // box associated to the new SSID
	wpaNotAssociated                       // conf written, but no association -> roll back
	wpaCannotApply                         // conf could not be written live -> reboot to apply
)

// applyWlanWPALive writes the new wpa_supplicant.conf, reloads wpa_supplicant,
// and reports whether the box associated to the new SSID within the timeout. The
// running conf is backed up to writable NAND first (NOT next to the conf: /etc is
// read-only on rhino/scm) so a failed switch can roll back. A failed backup never
// blocks the switch — it only forfeits the rollback.
func (s *Server) applyWlanWPALive(iface, ssid, password string, hidden bool) wpaApplyResult {
	if cur, err := os.ReadFile(wpaConfPath); err == nil {
		if werr := os.WriteFile(wpaBackupPath, cur, 0o600); werr != nil {
			// Read-only NAND would be unexpected, but never let a backup failure
			// abort the switch (the old /etc backup did, breaking every switch on
			// read-only-/etc boxes). Drop any stale backup so rollback won't
			// restore an unrelated conf.
			s.logger.Warn("WLAN: could not back up wpa conf, switching anyway (rollback unavailable)", "err", werr)
			_ = os.Remove(wpaBackupPath)
		}
	} else {
		// Could not read the current conf to back it up (e.g. it does not exist
		// yet): drop any stale backup from an earlier switch so a later rollback
		// can never restore an unrelated/older conf. Symmetric with the
		// write-failure path above.
		_ = os.Remove(wpaBackupPath)
	}
	method, err := writeWPAConf(buildWPAConfig(ssid, password, hidden))
	if err != nil {
		s.logger.Warn("WLAN: write wpa conf failed, will reboot to apply via boot path", "err", err, "path", wpaConfPath)
		return wpaCannotApply
	}
	s.logger.Info("WLAN: wpa conf written", "method", method, "iface", iface)
	// Escalate the way run.sh's boot path does (M3 restart, M4 add_network)
	// instead of a single reconfigure+reassociate then rollback. A bare
	// reconfigure does not dislodge a config NetManager reverted, so a runtime
	// switch that run.sh would have completed on the next boot used to fail live
	// and roll straight back (#288). Each stage only runs if the previous one did
	// not associate, and the rollback in applyWLANChange still protects a genuine
	// failure (e.g. a wrong password), so this can only help, never strand.
	reloadWPA(iface)
	if waitWPAAssociated(iface, ssid, 12*time.Second) {
		return wpaConfirmed
	}
	s.logger.Warn("WLAN: reconfigure did not associate, restarting wpa_supplicant (M3)", "ssid", ssid)
	restartWPA(iface)
	if waitWPAAssociated(iface, ssid, 12*time.Second) {
		return wpaConfirmed
	}
	s.logger.Warn("WLAN: restart did not associate, trying wpa_cli add_network fallback (M4)", "ssid", ssid)
	if wpaAddNetwork(iface, ssid, password, hidden) && waitWPAAssociated(iface, ssid, 12*time.Second) {
		return wpaConfirmed
	}
	return wpaNotAssociated
}

// waitWPAAssociated polls wpaAssociatedTo until the box is COMPLETED on ssid or
// the window elapses.
func waitWPAAssociated(iface, ssid string, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		if wpaAssociatedTo(iface, ssid) {
			return true
		}
	}
	return false
}

// restartWPA fully restarts wpa_supplicant (run.sh M3): a reconfigure keeps a
// stale/NetManager-reverted association, a clean relaunch reads the new conf from
// scratch. Independent of wpa_cli being present.
func restartWPA(iface string) {
	_ = exec.Command("killall", "wpa_supplicant").Run()
	time.Sleep(time.Second)
	_ = exec.Command("wpa_supplicant", "-B", "-i", iface, "-s", "-c", wpaConfPath, "-D", "nl80211").Start()
}

// wpaAddNetwork is run.sh's M4 fallback: build the network directly through
// wpa_cli (add_network / set_network / enable / select / save_config) when the
// conf-file path did not take. scan_ssid=1 lets a hidden SSID be found. Returns
// false if wpa_cli is absent or add_network fails.
func wpaAddNetwork(iface, ssid, password string, hidden bool) bool {
	if _, err := exec.LookPath("wpa_cli"); err != nil {
		return false
	}
	run := func(args ...string) string {
		out, _ := exec.Command("wpa_cli", append([]string{"-i", iface}, args...)...).Output()
		return strings.TrimSpace(string(out))
	}
	id := run("add_network")
	if id == "" || strings.Contains(id, "FAIL") {
		return false
	}
	// wpa_cli set_network wants the ssid/psk quoted; %q emits the surrounding
	// double quotes wpa_cli expects, and exec passes the arg verbatim (no shell).
	run("set_network", id, "ssid", fmt.Sprintf("%q", ssid))
	if password != "" {
		run("set_network", id, "psk", fmt.Sprintf("%q", password))
	} else {
		run("set_network", id, "key_mgmt", "NONE")
	}
	if hidden {
		run("set_network", id, "scan_ssid", "1")
	}
	run("enable_network", id)
	run("select_network", id)
	run("save_config")
	return true
}

// writeWPAConf installs content as /etc/wpa_supplicant.conf, working around a
// read-only /etc the way run.sh's M3 boot path does: a direct write first, and
// if that fails, a bind mount overlaying the conf from a tmpfs copy so
// wpa_supplicant reads the new content without /etc ever being written. Returns
// the method used ("direct"/"bind") or an error if both fail.
func writeWPAConf(content string) (string, error) {
	return writeWPAConfAt(wpaConfPath, "/tmp/wpa_supplicant.conf.str", content)
}

// writeWPAConfAt is the path-injectable core of writeWPAConf, kept separate so
// the direct-write path is unit-testable off-box.
func writeWPAConfAt(confPath, tmpPath, content string) (string, error) {
	directErr := os.WriteFile(confPath, []byte(content), 0o600)
	if directErr == nil {
		return "direct", nil
	}
	// /etc is read-only (rhino/scm): stage the conf in tmpfs and bind-mount it
	// over the existing path so wpa_supplicant reads the new content.
	if terr := os.WriteFile(tmpPath, []byte(content), 0o600); terr != nil {
		return "", terr
	}
	if berr := exec.Command("mount", "--bind", tmpPath, confPath).Run(); berr != nil {
		return "", fmt.Errorf("direct write (%w) and bind-mount (%v) both failed", directErr, berr)
	}
	return "bind", nil
}

// reloadWPA reloads the new conf in place via wpa_cli (preferred, keeps the
// daemon up), or restarts wpa_supplicant if wpa_cli is absent. Same commands
// run.sh uses in its M3/M6 approaches.
func reloadWPA(iface string) {
	if _, err := exec.LookPath("wpa_cli"); err == nil {
		_ = exec.Command("wpa_cli", "-i", iface, "reconfigure").Run()
		_ = exec.Command("wpa_cli", "-i", iface, "reassociate").Run()
		return
	}
	_ = exec.Command("killall", "wpa_supplicant").Run()
	time.Sleep(time.Second)
	_ = exec.Command("wpa_supplicant", "-B", "-i", iface, "-s", "-c", wpaConfPath, "-D", "nl80211").Start()
}

// wpaAssociatedTo reports whether wpa_supplicant is COMPLETED on the given SSID.
func wpaAssociatedTo(iface, ssid string) bool {
	out, err := exec.Command("wpa_cli", "-i", iface, "status").Output()
	if err != nil {
		return false
	}
	st := string(out)
	if !strings.Contains(st, "wpa_state=COMPLETED") {
		return false
	}
	for _, line := range strings.Split(st, "\n") {
		if strings.TrimSpace(line) == "ssid="+ssid {
			return true
		}
	}
	return false
}

func (s *Server) restoreWPAConfAndReload(iface string) {
	if b, err := os.ReadFile(wpaBackupPath); err == nil {
		if _, werr := writeWPAConf(string(b)); werr != nil {
			s.logger.Warn("WLAN: rollback write failed", "err", werr)
		}
		_ = os.Remove(wpaBackupPath)
	}
	reloadWPA(iface)
}

// rebootBox triggers a detached reboot so BCO boxes apply the persisted creds
// through the boot-time provisioning path. sync flushes the NAND creds first.
func rebootBox() {
	_ = exec.Command("sh", "-c", "(sleep 1; sync; /sbin/reboot) </dev/null >/dev/null 2>&1 &").Start()
}

// buildWPAConfig generates a minimal wpa_supplicant.conf. With an empty
// password key_mgmt=NONE is set (open WLAN). hidden adds scan_ssid=1 to the
// network block so wpa_supplicant sends SSID-specific probe requests, which is
// the only way to find a network that does not broadcast its SSID.
func buildWPAConfig(ssid, psk string, hidden bool) string {
	var b strings.Builder
	b.WriteString("ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=root\n")
	b.WriteString("update_config=1\n")
	b.WriteString("network={\n")
	b.WriteString("    ssid=\"" + escapeWPAValue(ssid) + "\"\n")
	if hidden {
		b.WriteString("    scan_ssid=1\n")
	}
	if psk == "" {
		b.WriteString("    key_mgmt=NONE\n")
	} else {
		b.WriteString("    psk=\"" + escapeWPAValue(psk) + "\"\n")
		b.WriteString("    key_mgmt=WPA-PSK\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func escapeWPAValue(s string) string {
	// Escape backslash and double-quote, plus the control characters that would
	// otherwise break the single-line key="value" form and corrupt the conf (a
	// JSON body can carry a literal newline/tab in an SSID).
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	return r.Replace(s)
}

// countryToLanguage returns the default language code for the radio-browser
// "language" filter field from an ISO 3166-1 country code. Fallback is
// "english" if unknown — the world understands that.
var countryToLanguage = map[string]string{
	"DE": "german", "AT": "german", "CH": "german", "LI": "german",
	"GB": "english", "US": "english", "IE": "english", "AU": "english",
	"NZ": "english", "CA": "english", "ZA": "english",
	"FR": "french", "BE": "french", "LU": "french", "MC": "french",
	"IT": "italian", "SM": "italian", "VA": "italian",
	"ES": "spanish", "MX": "spanish", "AR": "spanish", "CO": "spanish",
	"CL": "spanish", "PE": "spanish", "VE": "spanish",
	"PT": "portuguese", "BR": "portuguese",
	"NL": "dutch", "SR": "dutch",
	"DK": "danish", "SE": "swedish", "NO": "norwegian", "FI": "finnish",
	"IS": "icelandic",
	"PL": "polish", "CZ": "czech", "SK": "slovak", "HU": "hungarian",
	"RO": "romanian", "BG": "bulgarian", "HR": "croatian", "SI": "slovenian",
	"GR": "greek", "TR": "turkish",
	"RU": "russian", "UA": "ukrainian", "BY": "belarusian",
	"JP": "japanese", "CN": "chinese", "TW": "chinese", "HK": "chinese",
	"KR": "korean", "IN": "hindi", "ID": "indonesian", "TH": "thai",
	"VN": "vietnamese", "PH": "tagalog", "MY": "malay",
	"IL": "hebrew", "AE": "arabic", "SA": "arabic", "EG": "arabic", "MA": "arabic",
}

func languageForCountry(cc string) string {
	if cc == "" {
		return "english"
	}
	if l, ok := countryToLanguage[strings.ToUpper(cc)]; ok {
		return l
	}
	return "english"
}

// handleRegion returns the region saved by the setup wizard together
// with the derived default language, or sets it anew via PUT.
func (s *Server) handleRegion(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.regionMu.RLock()
		cc := s.region
		s.regionMu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]string{
			"country":  cc,
			"language": languageForCountry(cc),
		})
	case http.MethodPut:
		var req struct {
			Country string `json:"country"`
		}
		if !decodeJSONRequest(w, r, 256, &req) {
			return
		}
		cc := strings.ToUpper(strings.TrimSpace(req.Country))
		if len(cc) != 2 {
			http.Error(w, "country must be ISO 3166-1 alpha-2", http.StatusBadRequest)
			return
		}
		s.regionMu.Lock()
		s.region = cc
		path := s.regionFile
		s.regionMu.Unlock()
		if path != "" {
			if err := os.WriteFile(path, []byte(cc+"\n"), 0o644); err != nil {
				s.logger.Warn("region.txt write failed", "err", err)
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"country":  cc,
			"language": languageForCountry(cc),
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// agentVersion and agentBuild are supplied via setters from main.go.
var (
	agentVersion = func() string { return "1.0.0" }
	agentBuild   = func() string { return "dev" }
)

// SetAgentVersion allows main.go to set the semver version at startup.
func SetAgentVersion(v string) { agentVersion = func() string { return v } }

// SetAgentBuild sets the build stamp (date/commit) as additional info.
func SetAgentBuild(b string) { agentBuild = func() string { return b } }

// handleStatus proxies the box's now_playing XML, with a short
// micro-cache (statusCacheTTL) in front. Multiple or too-rapidly polling
// clients thus share the same box roundtrip instead of hitting the
// fragile BoseApp (:8090) anew on every request.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if s.boxHost == "" {
		http.Error(w, "box host not configured", http.StatusServiceUnavailable)
		return
	}

	// Serve from cache if a recent good body exists.
	s.statusMu.Lock()
	if s.statusBody != nil && time.Since(s.statusAt) < statusCacheTTL {
		body, code := s.statusBody, s.statusCode
		s.statusMu.Unlock()
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(code)
		_, _ = w.Write(body)
		return
	}
	s.statusMu.Unlock()

	// A bounded client, NOT http.Get: a BoseApp that accepts the connection
	// but never answers (the documented Portable freeze) would otherwise hang
	// every /api/status poll forever.
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get(fmt.Sprintf("http://%s:8090/now_playing", s.boxHost))
	if err != nil {
		// Fall back to the last cached body on a box error so a brief BoseApp
		// hiccup does not blank the now-playing display. The body must stay
		// the box's own XML (clients regex-parse it), so once the outage is no
		// longer brief the staleness is signalled OUT OF BAND: response
		// headers carry the age, and one WARN marks the transition. Without
		// this, a box whose BoseApp died kept showing hours-old "playing"
		// state on every client with nothing in the log.
		s.statusMu.Lock()
		body, code, have := s.statusBody, s.statusCode, s.statusBody != nil
		age := time.Since(s.statusAt)
		stale := have && age >= statusStaleAfter
		warn := stale && !s.statusStaleWarned
		if warn {
			s.statusStaleWarned = true
		}
		s.statusMu.Unlock()
		if have {
			if warn {
				s.logger.Warn("box now_playing unreachable; /api/status keeps serving the last cached body, now marked stale",
					"ageSec", int(age.Seconds()), "err", err)
			}
			w.Header().Set("X-STR-Status-Age", strconv.Itoa(int(age.Seconds())))
			if stale {
				w.Header().Set("X-STR-Status-Stale", "1")
			}
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(code)
			_, _ = w.Write(body)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	// Cache only successful bodies; an error status is served through but
	// not memoised, so the next poll retries the box.
	if resp.StatusCode == http.StatusOK {
		s.statusMu.Lock()
		s.statusBody = body
		s.statusCode = resp.StatusCode
		s.statusAt = time.Now()
		s.statusStaleWarned = false
		s.statusMu.Unlock()
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// ---- Helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- Index Page (minimal HTML for direct browser use) ----

// remoteDisplayName is the speaker's friendly name for the phone remote's
// identity (page title, iOS home-screen label, PWA manifest). Empty when the
// box has not told us a name (yet).
func (s *Server) remoteDisplayName() string {
	if s.boxNameFn == nil {
		return ""
	}
	name, _ := s.boxNameFn()
	return strings.TrimSpace(name)
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := indexHTML
	// Stamp the speaker's name into the page identity so "Add to Home Screen"
	// saves one distinguishable app PER SPEAKER: iOS takes its label from the
	// apple-mobile-web-app-title meta (and the title tag) at save time; each
	// speaker is its own origin, so the phone keeps them apart as separate
	// apps. The generic "ST Reborn" is only the fallback for a box whose name
	// is not known yet.
	if name := s.remoteDisplayName(); name != "" {
		esc := html.EscapeString(name)
		page = strings.Replace(page, "<title>ST Reborn</title>", "<title>"+esc+"</title>", 1)
		page = strings.Replace(page,
			`<meta name="apple-mobile-web-app-title" content="ST Reborn">`,
			`<meta name="apple-mobile-web-app-title" content="`+esc+`">`, 1)
	}
	_, _ = fmt.Fprint(w, page)
}

// handlePeers lists the other STR speakers on the LAN so the page can offer
// links to hop between them. Returns [] when no resolver is wired.
func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	if s.peersFn == nil {
		writeJSON(w, http.StatusOK, []PeerLink{})
		return
	}
	peers := s.peersFn(r.Context())
	if peers == nil {
		peers = []PeerLink{}
	}
	writeJSON(w, http.StatusOK, peers)
}

// handleManifest serves the PWA manifest so a phone can install the controller
// page as a standalone home-screen app. The app name is the SPEAKER's name, so
// a user with several speakers saves several distinguishable apps (one per
// origin); the generic branding is only the fallback while the box name is
// unknown. Cache short: a rename should reach the next home-screen save.
func (s *Server) handleManifest(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	name, short := "ST Reborn", "STR"
	if n := s.remoteDisplayName(); n != "" {
		name = n
		// short_name shows under the icon; Android truncates around 12-15
		// characters itself, but a deliberate cap keeps the label readable.
		short = n
		if r := []rune(short); len(r) > 14 {
			short = string(r[:14])
		}
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":             name,
		"short_name":       short,
		"description":      "Control your Bose SoundTouch speaker",
		"start_url":        "/",
		"scope":            "/",
		"display":          "standalone",
		"orientation":      "portrait",
		"background_color": "#1a1a1a",
		"theme_color":      "#1a1a1a",
		"icons": []map[string]string{
			{"src": "/icon.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			{"src": "/icon.png", "sizes": "192x192", "type": "image/png", "purpose": "maskable"},
		},
	})
}

// handleIcon serves the embedded STR app icon (favicon, iOS apple-touch-icon and
// the PWA manifest icon). Tiny (a few KB) and cached hard to spare the NAND-bound
// box repeat reads.
func (s *Server) handleIcon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	_, _ = w.Write(iconPNG)
}
