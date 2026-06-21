// Package webui provides the config web interface on port 8888.
// Contains the HTML UI plus a REST API that is later also used by the
// Wails desktop app.
package webui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	spotifyPlay func(ctx context.Context, uri, account string) error
	// spotifyUser returns go-librespot's currently logged-in account, used to
	// stamp the account onto a newly saved Spotify preset. nil when Spotify
	// is not configured.
	spotifyUser func(ctx context.Context) string
	// spotifyMeta resolves a stable cover image URL and the human title for a
	// Spotify context URI (the playlist image + name), stamped onto a newly
	// saved Spotify preset so its tile has a steady logo and a real name (not a
	// bare "Spotify"). nil when Spotify is not configured.
	spotifyMeta func(ctx context.Context, uri string) (cover, title string)
	// spotifyStreaming reports whether the box is currently pulling the Ogg
	// stream, the definitive "Spotify is playing" signal for verifyRecall.
	// nil when Spotify is not configured.
	spotifyStreaming func() bool
	// spotifyReady reports whether go-librespot has finished authenticating, so
	// a soft Spotify recall can wait out a cold start instead of pointing the box
	// at a not-yet-flowing stream (which starves and detaches). nil when Spotify
	// is not configured.
	spotifyReady func() bool
	// spotifyLoggedIn reports whether the speaker has ever completed a Spotify
	// Connect login (a credential is persisted). Without it go-librespot cannot
	// start playback on its own, so a recall does nothing; the handler returns a
	// clear "log this speaker into Spotify first" error instead of optimistically
	// reporting "playing" and failing silently in the background (#45 Pierre). nil
	// until wired.
	spotifyLoggedIn func() bool
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
	// spotifyInfo answers GET /spotify/info with the live Spotify state
	// (ready, measured bitrate, device name) the UI reads to show the real
	// stream bitrate on a Spotify preset tile. nil when not configured.
	spotifyInfo http.HandlerFunc
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

	// lastUserStop is when the user last DELIBERATELY stopped playback, so the
	// auto-re-push does not fight a wanted stop (v0.7.0: a single Stop
	// did not hold because the proxy disconnect that a stop causes looks
	// identical to a box-side drop). Set from the STR Stop/Pause endpoints
	// (definite intent) and from a gabbo STOP_STATE frame (the physical
	// remote / box button). maybeRePush suppresses a resume within
	// userStopWindow of this.
	lastUserStopMu sync.Mutex
	lastUserStop   time.Time

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

	// boxPresets is the box's OWN preset list as last reported over the gabbo
	// presetsUpdated frame, including foreign sources (DEEZER etc.) STR did not
	// set. Lets the app show/preserve/recall them (Option C). Guarded by boxPresetsMu.
	boxPresetsMu sync.Mutex
	boxPresets   []BoxPreset
}

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

// WithSpotifyControl registers the function that starts playback of a
// Spotify URI on a given account in go-librespot (the Spotify-preset
// control plane). An empty account plays with the current login.
func WithSpotifyControl(play func(ctx context.Context, uri, account string) error) Option {
	return func(s *Server) { s.spotifyPlay = play }
}

// WithSpotifyUser registers the resolver for go-librespot's current account,
// used to stamp the account onto a newly saved Spotify preset.
func WithSpotifyUser(user func(ctx context.Context) string) Option {
	return func(s *Server) { s.spotifyUser = user }
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

// WithSpotifyPremiumRequired registers the predicate that reports whether the
// logged-in Spotify account is free/open and so cannot do the autonomous recall
// playback a preset needs (#45). The recall handler uses it to answer with a
// clear "needs Premium" message instead of failing silently.
func WithSpotifyPremiumRequired(f func() bool) Option {
	return func(s *Server) { s.spotifyPremiumRequired = f }
}

// WithSpotifyLoggedIn registers the predicate that reports whether the speaker
// has a persisted Spotify login, so a recall on a never-logged-in speaker fails
// with a clear, actionable message instead of silently (#45).
func WithSpotifyLoggedIn(f func() bool) Option {
	return func(s *Server) { s.spotifyLoggedIn = f }
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

// WithRecent wires the recently-played ring (#135) so the play handlers record
// the user's listening history and GET /api/recent serves it.
func WithRecent(r *recent.Store) Option {
	return func(s *Server) { s.recent = r }
}

// ensureBoxReady wakes the box from standby (with retry+poll until
// really awake) and ensures the marge account is active.
// Called before every play call.
func (s *Server) ensureBoxReady(ctx context.Context) {
	if s.boxHost != "" {
		wakeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		if err := boxcli.WakeAndWait(wakeCtx, s.boxHost, 6*time.Second, s.logger); err != nil {
			s.logger.Warn("Box could not be woken from STANDBY", "err", err)
		}
		cancel()
	}
	if s.autoPair != nil {
		pairCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		s.autoPair.TriggerNow(pairCtx)
		cancel()
	}
}

// New creates a new webui server.
func New(addr string, logger *slog.Logger, opts ...Option) *Server {
	s := &Server{addr: addr, logger: logger}
	for _, o := range opts {
		o(s)
	}
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
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
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
	mux.HandleFunc("/api/stop", s.handleStop)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/recent", s.handleRecent)
	// Radio search/browse moved app-side (the app queries radio-browser
	// directly; see the app-first direction). The box no longer serves
	// /api/radio/* and no longer compiles in the radiobrowser package.
	mux.HandleFunc("/api/agent/version", s.handleAgentVersion)
	mux.HandleFunc("/api/agent/update", s.handleAgentUpdate)
	mux.HandleFunc("/api/box/settings", s.handleBoxSettings)
	mux.HandleFunc("/api/box/name", s.handleBoxName)
	mux.HandleFunc("/api/box/volume", s.handleBoxVolume)
	mux.HandleFunc("/api/box/bass", s.handleBoxBass)
	mux.HandleFunc("/api/box/source", s.handleBoxSource)
	mux.HandleFunc("/api/region", s.handleRegion)
	mux.HandleFunc("/api/box/wlan", s.handleBoxWLAN)
	mux.HandleFunc("/api/box/reboot", s.handleBoxReboot)
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
		} else if p.StreamURL != "" && !isHTTPURL(p.StreamURL) {
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
		// Stamp the account a Spotify preset belongs to (go-librespot's current
		// login) so a later recall can switch back to it on a multi-account box
		// (#27). The client may already supply it; only fill when empty.
		// Account + cover are best-effort enrichment: use a fresh background
		// context, not r.Context(), so a client that disconnects right after the
		// PUT (e.g. a raw one-shot request) does not cancel them mid-fetch.
		if p.Type == "spotify" && p.Account == "" && s.spotifyUser != nil {
			uctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if u := s.spotifyUser(uctx); u != "" {
				p.Account = u
			}
			cancel()
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
		if s.boxHost != "" {
			boxCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			_ = boxcli.RemovePreset(boxCtx, s.boxHost, slot)
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
	// Ad-hoc radio: the box leaves any Spotify source; suppress the #14
	// auto-attach so it does not jump back to Spotify.
	if s.spotifySwitchedAway != nil {
		s.spotifySwitchedAway(r.Context())
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
	s.logger.Info("play request", "direct", playDirect, "mime", req.Mime, "url", req.URL)
	// Advertise the real codec to the box when the caller knows it (a network
	// library track carries its DLNA-reported MIME, e.g. audio/flac, audio/mp4).
	// Radio leaves it empty and defaults to audio/mpeg. The box keys its decoder
	// off this protocolInfo MIME, so a FLAC/ALAC/M4A file mislabelled as
	// audio/mpeg is rejected (AUDIO_ERROR_BAD_URL) while an MP3 plays (#139).
	var playErr error
	if req.Mime != "" {
		playErr = s.renderer.PlayURLMime(r.Context(), playURL, req.Title, req.Icon, req.Mime)
	} else {
		playErr = s.renderer.PlayURL(r.Context(), playURL, req.Title, req.Icon)
	}
	if playErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "Station could not be played",
			"detail": guessErrorReason(playErr),
			"url":    req.URL,
		})
		return
	}
	s.setLastPlay(playURL, req.Title, req.Icon, req.Mime)
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
		// A speaker that has never been logged into Spotify has no credential for
		// go-librespot, so it cannot start playback on its own and the recall would
		// silently do nothing (#45 Pierre: saved preset account="" and go-librespot
		// not running). Tell the user how to fix it instead of optimistically
		// reporting "playing" and failing in the background.
		if s.spotifyLoggedIn != nil && !s.spotifyLoggedIn() {
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
		if err := s.renderer.PlayURLMime(r.Context(), slotURL, p.Name, p.Art, "audio/ogg"); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": "Spotify stream could not be played", "detail": guessErrorReason(err),
				"slot": slot, "name": p.Name,
			})
			return
		}
		s.setLastPlay(slotURL, p.Name, p.Art, "audio/ogg")
		s.recentNoteCard("spotify", p.URI, p.Name, p.Art, p.URI, p.Account, "") // #135
		uri, name, art, account := p.URI, p.Name, p.Art, p.Account
		go func() {
			bg := context.Background()
			if s.spotifyReady != nil && !s.spotifyReady() {
				s.logger.Info("spotify soft recall: waiting for go-librespot ready", "slot", slot)
				for i := 0; i < 24 && !s.spotifyReady(); i++ {
					time.Sleep(500 * time.Millisecond)
				}
			}
			if err := s.spotifyPlay(bg, uri, account); err != nil {
				s.logger.Warn("spotify play (initial) failed, will verify+retry", "slot", slot, "err", err)
			}
			s.verifyRecall(func(ctx context.Context, lastAttempt bool) {
				// Re-point the box at the stream WITHOUT re-Play on the early
				// tries: ServeOgg resumes go-librespot on attach, so this
				// re-attaches without reshuffling/restarting the track (a re-Play
				// every retry was the "same song restarts a few seconds in" bug,
				// fixed for hardware in v0.7.4 but previously still present here).
				// Only the last attempt does a full re-Play, to recover a genuine
				// cold-boot auth race where the playlist never loaded at all.
				if lastAttempt {
					_ = s.spotifyPlay(ctx, uri, account)
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
		s.spotifySwitchedAway(r.Context())
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
			if err := s.renderer.PlayURLMime(r.Context(), directURL, p.Name, p.Art, mime); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]any{
					"error": "Track could not be played", "detail": guessErrorReason(err),
					"slot": slot, "name": p.Name,
				})
				return
			}
			s.setLastPlay(directURL, p.Name, p.Art, mime)
			s.recentNoteCard("upnp", p.StreamURL, p.Name, p.Art, p.StreamURL, "", "") // #135
			name, art := p.Name, p.Art
			go s.verifyRecall(func(ctx context.Context, _ bool) {
				_ = s.renderer.PlayURLMime(ctx, directURL, name, art, mime)
			}, nil)
			writeJSON(w, http.StatusOK, map[string]any{"status": "playing", "slot": slot, "name": p.Name})
			return
		}
	}
	// Use the stream proxy URL so playback continues even after token
	// expiry (Bose sees the stable loopback URL).
	playURL := boxurl.StreamSlot(slot)
	if err := s.renderer.PlayURL(r.Context(), playURL, p.Name, p.Art); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":  "Station could not be played",
			"detail": guessErrorReason(err),
			"slot":   slot,
			"name":   p.Name,
		})
		return
	}
	s.setLastPlay(playURL, p.Name, p.Art, "")
	s.recentNoteCard("radio", p.StreamURL, p.Name, p.Art, p.StreamURL, "", p.Homepage) // #135
	name, art := p.Name, p.Art
	go s.verifyRecall(func(ctx context.Context, _ bool) {
		_ = s.renderer.PlayURL(ctx, playURL, name, art)
	}, nil)
	writeJSON(w, http.StatusOK, map[string]any{"status": "playing", "slot": slot, "name": p.Name})
}

// verifyRecall confirms the box reached a playing state shortly after a recall
// and re-issues the play a few times if not. Fixes the "first press after a
// reboot does nothing, second press works" race (box/go-librespot not ready
// yet) without any latency on the happy path (the initial play already ran).
//
// retry receives lastAttempt=true only on the final try. Spotify uses it to
// re-point the box without a full re-Play on the early tries (a re-Play
// reshuffles and restarts the track), reserving the disruptive re-Play for the
// last-resort recovery. This is the same policy the hardware recall settled on
// in v0.7.4 (cmd/agent verifySpotifyPlaying); routing both through one contract
// keeps the soft and hardware paths from drifting again.
func (s *Server) verifyRecall(retry func(ctx context.Context, lastAttempt bool), working func() bool) {
	const attempts = 3
	for attempt := 1; attempt <= attempts; attempt++ {
		time.Sleep(5 * time.Second)
		// working() is a source-specific "it is already fine" signal checked
		// before the box now_playing state. For Spotify it reports whether the
		// box is pulling the Ogg stream: now_playing flaps while the box
		// attaches, and a re-issue would reshuffle + restart the track (the
		// audible abort + UI play/stop/play flicker). Don't retry when working.
		if working != nil && working() {
			return
		}
		if _, busy := s.boxPlayState(); busy {
			return
		}
		s.logger.Warn("recall did not reach playing, retrying", "attempt", attempt)
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		retry(ctx, attempt == attempts)
		cancel()
	}
	s.logger.Warn("recall still not playing after retries")
}

// setLastPlay records the box-facing stream + metadata for the auto-re-push. A
// fresh play resets the re-push state (rePushes=0, failed=false), so a stream
// that was previously declared dead gets a clean slate when the user plays it
// again.
func (s *Server) setLastPlay(boxURL, title, art, mime string) {
	now := time.Now()
	s.lastPlayMu.Lock()
	s.lastPlay = &lastPlayInfo{boxURL: boxURL, title: title, art: art, mime: mime, ts: now}
	s.lastPlayMu.Unlock()
}

// NoteLastPlay records a stream the agent pushed to the box OUTSIDE the webui
// (the hardware preset recall in cmd/agent goes straight to the renderer). It
// lets the auto-re-push (#4) and the power-button wake-resume work for hardware
// presses too, which otherwise left lastPlay unset and the box un-resumable.
func (s *Server) NoteLastPlay(boxURL, title, art, mime string) {
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
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	if lp == nil || time.Since(lp.ts) >= 12*time.Hour {
		s.lastPlayMu.Unlock()
		s.logger.Info("wake resume: nothing recent to resume")
		return
	}
	lp.failed = false
	lp.rePushes = 0
	boxURL, title, art, mime := lp.boxURL, lp.title, lp.art, lp.mime
	s.lastPlayMu.Unlock()

	go func() {
		// Let the power transition settle so the box's reported state is
		// unambiguous before we decide. The DO_NOT_RESUME wake that triggers this
		// fires on a power-on, but the box can also reach standby again right
		// after, so settle then read the real state.
		time.Sleep(2 * time.Second)

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
	if s.userStoppedRecently() {
		return
	}
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	if lp == nil || time.Since(lp.ts) >= 12*time.Hour {
		s.lastPlayMu.Unlock()
		return
	}
	boxURL, title, art, mime := lp.boxURL, lp.title, lp.art, lp.mime
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
		if !s.boxSelectionStuck() {
			return // asleep, already playing, or on a native source: nothing to recover
		}
		s.boxCmdMu.Lock()
		defer s.boxCmdMu.Unlock()
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
	if s.boxHost == "" {
		return false
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get("http://" + s.boxHost + ":8090/now_playing")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	body := string(b)
	if strings.Contains(body, "STANDBY") {
		return false // box asleep: a routine idle reconnect, nothing to recover
	}
	if strings.Contains(body, "PLAY_STATE") || strings.Contains(body, "BUFFERING_STATE") || strings.Contains(body, "PAUSE_STATE") {
		return false // already playing/paused
	}
	// Only recover an STR-owned selection (UPNP) or the box's failed
	// self-activation of it (INVALID_SOURCE). A native source the user picked
	// (AUX, BLUETOOTH, ...) is left alone.
	return strings.Contains(body, `source="UPNP"`) || strings.Contains(body, "INVALID_SOURCE")
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

// userStoppedRecently reports whether a deliberate stop happened within
// userStopWindow.
func (s *Server) userStoppedRecently() bool {
	s.lastUserStopMu.Lock()
	defer s.lastUserStopMu.Unlock()
	return !s.lastUserStop.IsZero() && time.Since(s.lastUserStop) < userStopWindow
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
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
	writeJSON(w, http.StatusOK, out)
}

// handleAgentUpdate receives a new stick agent binary, writes it
// atomically to /mnt/nv/streborn/bin/streborn-armv7l and restarts the
// agent. Body must be the raw ARM binary.
//
// On success the stick still returns 200 OK and then exits. The
// rc.local bootstrap starts the new agent.
func (s *Server) handleAgentUpdate(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "update only allowed from LAN", http.StatusForbidden)
		return
	}
	const maxSize = 30 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSize+1))
	if err != nil {
		http.Error(w, "read: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > maxSize {
		http.Error(w, "binary too big", http.StatusRequestEntityTooLarge)
		return
	}
	if len(body) < 1024 {
		http.Error(w, "binary too small", http.StatusBadRequest)
		return
	}
	// ELF Magic Check
	if body[0] != 0x7f || body[1] != 'E' || body[2] != 'L' || body[3] != 'F' {
		http.Error(w, "not an ELF binary", http.StatusBadRequest)
		return
	}

	const dst = "/mnt/nv/streborn/bin/streborn-armv7l"
	// On a fresh speaker the parent /mnt/nv/streborn/bin directory may
	// not exist yet — first OTA after install hits this. Create it so
	// the write does not 500 on "no such file or directory".
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		http.Error(w, "mkdir parent: "+err.Error(), http.StatusInternalServerError)
		return
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, body, 0o755); err != nil {
		http.Error(w, "write tmp: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		http.Error(w, "rename: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.logger.Info("agent update written, rebooting box for a clean post-OTA state", "size", len(body))
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
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if serr := cmd.Start(); serr != nil {
				s.logger.Error("self-restart fallback also failed", "err", serr)
				return
			}
			time.Sleep(100 * time.Millisecond)
			os.Exit(0)
		}
	}()
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
	if !requireMethod(w, r, http.MethodPut) {
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	var req struct {
		Value int `json:"value"`
	}
	if !decodeJSONRequest(w, r, 256, &req) {
		return
	}
	c := boxapi.New(s.boxHost)
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
		s.mirrorToSlaves(ctx, z)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "mirror"})
		return
	}

	// Native: drive the firmware zone and read back what it actually formed.
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
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": ok, "mode": "native", "master": z2.Master, "senderIP": z2.SenderIP,
		"members": z2.Members, "requested": len(slaves),
		"verified": verified, "missing": missing, "unverifiable": unverifiable,
		"masterMissing": masterMissing,
	})
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
	const (
		perFollowerBudget = 4 * time.Second
		pollInterval      = 700 * time.Millisecond
		perCallTimeout    = 2 * time.Second
	)
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
	s.logger.Info("stereo: paired", "id", g.ID, "members", len(g.Members))
	writeJSON(w, http.StatusOK, g)
}

// mirrorToSlaves points each slave's box at the master's current stream URL over
// UPnP (the mirror path). The master's stream is whatever STR last told the
// master box to play (s.lastPlay), which the slaves can pull from the master
// agent's stream proxy. Looser than firmware sync, but works when the firmware
// refuses to distribute STR's source. Best-effort + heavily logged.
func (s *Server) mirrorToSlaves(ctx context.Context, z zones.Zone) {
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	s.lastPlayMu.Unlock()
	if lp == nil || lp.boxURL == "" {
		s.logger.Info("zone mirror: master is not playing yet; slaves will mirror once you start playback and the reconcile fires (beta)")
		return
	}
	for _, m := range z.Slaves {
		if m.IP == "" {
			continue
		}
		rr := upnp.NewBoseRenderer(m.IP)
		var err error
		if lp.mime != "" {
			err = rr.PlayURLMime(ctx, lp.boxURL, lp.title, lp.art, lp.mime)
		} else {
			err = rr.PlayURL(ctx, lp.boxURL, lp.title, lp.art)
		}
		if err != nil {
			s.logger.Warn("zone mirror: slave play failed", "slave", m.IP, "err", err)
		} else {
			s.logger.Info("zone mirror: slave mirroring master stream (beta)", "slave", m.IP, "url", lp.boxURL)
		}
	}
}

// defaultZoneReconcilePath is the NAND flag file that opts a box INTO the
// periodic zone reconcile (#70 beta). Absent (the default) means OFF: the box
// never re-asserts a persisted zone, so a speaker the user plays on its own is
// never dragged back into a group. Only an explicit "1"/"true"/"on"/"yes" turns
// it on. The default is OFF after a multi-ST10 user reported standalone speakers
// being pulled into the master's zone every few minutes (the master kept
// re-asserting its persisted zone whenever a member left to play its own source).
const defaultZoneReconcilePath = "/mnt/nv/streborn/zone-reconcile"

// zoneReconcileEnabled reports whether the periodic zone reconcile runs on this
// box. Default ON so a formed zone survives reboot/standby/Wi-Fi outage (v1.0
// gate #2), which is the v0.7.29 behavior the fleet relied on. The flag file is
// an explicit OPT-OUT (write "off"/"0") for a box whose members are often played
// solo, where re-asserting the master's group would drag a member back in. The
// match-before-assert guard in reconcileZoneOnce already skips a no-op re-assert.
func (s *Server) zoneReconcileEnabled() bool {
	b, err := os.ReadFile(defaultZoneReconcilePath)
	if err != nil {
		return true // default ON
	}
	switch strings.ToLower(strings.TrimSpace(string(b))) {
	case "0", "false", "off", "no":
		return false // explicit opt-out
	default:
		return true
	}
}

// PeriodicZoneReconcile re-asserts a persisted group so it survives
// reboot/standby/Wi-Fi outage (#70 beta). No-op when standalone OR when the box
// is explicitly opted out (see zoneReconcileEnabled, default on). Started by
// cmd/agent after the server is built. Lives on the Server so the mirror path
// can reach s.lastPlay + the UPnP renderer.
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
	if !s.zoneReconcileEnabled() {
		return // explicit opt-out only (default is on): a box flagged "off" never
		// auto-re-asserts, so a speaker the user plays solo is not dragged back.
	}
	z, ok := s.zones.Get()
	if !ok {
		return // standalone
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if z.Mirror() {
		// Re-push the master's current stream to the slaves (best-effort).
		s.mirrorToSlaves(ctx, z)
		return
	}
	if z.Stereo {
		// A left/right stereo pair is a firmware-native group, not a multiroom
		// zone. Re-asserting it with the zone API (/setZone) would use the wrong
		// endpoint and could fight the firmware's own pairing, so leave a native
		// stereo pair alone; the firmware persists it across reboot/standby itself.
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

// sysBlockRoot and mediaRoot are the sysfs block-device root and the mount root.
// They are vars (not consts) so the stick-detection test can point them at a temp
// tree; in production they are the real paths.
var (
	sysBlockRoot = "/sys/block"
	mediaRoot    = "/media"
)

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
	for _, disk := range []string{"sda", "sdb"} {
		if !diskIsRemovableUSB(disk) {
			continue
		}
		mnt := filepath.Join(mediaRoot, disk+"1")
		// version.txt is the authoritative marker and carries the stick version.
		if b, err := os.ReadFile(filepath.Join(mnt, "version.txt")); err == nil {
			return true, strings.TrimSpace(string(b))
		}
		// Sticks that predate version.txt: accept the STR stick layout itself.
		// Still requires the stick to be mounted (these paths only exist on a
		// real, inserted stick), so the #179 phantom sda with no mount stays false.
		for _, marker := range []string{"install.sh", "run.sh", "streborn-armv7l"} {
			if _, err := os.Stat(filepath.Join(mnt, marker)); err == nil {
				return true, ""
			}
		}
	}
	return false, ""
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
		"proc_mounts":    readTail("/proc/mounts"),
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
// the box always reports the full list.
func (s *Server) NoteBoxPresets(ps []BoxPreset) {
	s.boxPresetsMu.Lock()
	s.boxPresets = ps
	s.boxPresetsMu.Unlock()
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
		writeJSON(w, http.StatusOK, map[string]any{
			"restored": []int{}, "message": "no account-linked cloud presets found to restore",
		})
		return
	}

	// Re-advertise the sources first so a reboot re-registers them (Path A).
	if s.reflectPath != "" {
		if err := boxsnapshot.MergeReflect(s.reflectPath, boxsnapshot.ReflectFromPresets(cloud)); err != nil {
			s.logger.Warn("restore: reflect-sources merge failed", "err", err)
		}
	}

	restored := []int{}
	failed := map[string]string{}
	services := map[string]bool{}
	for _, p := range cloud {
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
	s.logger.Info("box snapshot restore (experimental)", "restored", restored, "failed", len(failed), "services", svcList)
	writeJSON(w, http.StatusOK, map[string]any{
		"restored":          restored,
		"failed":            failed,
		"services":          svcList,
		"rebootRecommended": true,
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

	iface, mech := detectWlanMechanism()
	// Persist to NAND (with .bak backup) BEFORE responding: the response triggers
	// the client to rediscover the box on its new network, and the actual switch
	// runs in a background goroutine, so committing the canonical creds first
	// means a crash after the response can never leave the client believing the
	// switch happened while NAND still holds the old creds.
	if err := backupAndWriteWlanCreds(req.SSID, req.Password); err != nil {
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
	s.logger.Info("WLAN switch requested", "ssid", req.SSID, "mechanism", mech, "iface", iface)
	go s.applyWLANChange(iface, mech, req.SSID, req.Password)
}

const (
	wlanCredsPath = "/mnt/nv/streborn/wlan-creds"
	wpaConfPath   = "/etc/wpa_supplicant.conf"
)

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
func (s *Server) applyWLANChange(iface, mech, ssid, password string) {
	s.wlanMu.Lock()
	defer s.wlanMu.Unlock()
	switch mech {
	case "wpa":
		if s.applyWlanWPALive(iface, ssid, password) {
			s.logger.Info("WLAN: live switch confirmed", "ssid", ssid, "iface", iface)
			_ = os.Remove(wlanCredsPath + ".bak")
			_ = os.Remove(wpaConfPath + ".bak")
			return
		}
		// Did not associate: roll all the way back so a wrong password leaves the
		// box on its previous network instead of unreachable. The agent runs ON
		// the box, so it can do this even while the box is briefly off the LAN.
		s.logger.Warn("WLAN: new network did not associate, rolling back to previous", "ssid", ssid)
		restoreWlanCreds()
		s.restoreWPAConfAndReload(iface)
	default:
		s.logger.Info("WLAN: BCO chassis, rebooting to apply via boot path", "ssid", ssid)
		rebootBox()
	}
}

// backupAndWriteWlanCreds writes the canonical NAND wlan-creds (the SSID=/PASS=
// format the boot path replays), keeping the previous set as .bak for rollback.
func backupAndWriteWlanCreds(ssid, password string) error {
	_ = os.Rename(wlanCredsPath, wlanCredsPath+".bak") // best-effort backup
	body := fmt.Sprintf("SSID=%s\nPASS=%s\n", ssid, password)
	tmp := wlanCredsPath + ".new"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, wlanCredsPath)
}

func restoreWlanCreds() {
	if _, err := os.Stat(wlanCredsPath + ".bak"); err == nil {
		_ = os.Rename(wlanCredsPath+".bak", wlanCredsPath)
	}
}

// applyWlanWPALive writes the new wpa_supplicant.conf, reloads wpa_supplicant,
// and reports whether the box associated to the new SSID within the timeout.
// Backs up the running conf so applyWLANChange can roll back on failure.
func (s *Server) applyWlanWPALive(iface, ssid, password string) bool {
	if cur, err := os.ReadFile(wpaConfPath); err == nil {
		// Abort if we have a config to roll back to but cannot save the backup
		// (e.g. a read-only /etc): proceeding would make a failed switch
		// unrecoverable because restoreWPAConfAndReload would find no .bak.
		if werr := os.WriteFile(wpaConfPath+".bak", cur, 0o600); werr != nil {
			s.logger.Warn("WLAN: could not back up wpa conf, aborting switch", "err", werr)
			return false
		}
	}
	if err := os.WriteFile(wpaConfPath, []byte(buildWPAConfig(ssid, password)), 0o600); err != nil {
		s.logger.Warn("WLAN: write wpa conf failed", "err", err, "path", wpaConfPath)
		return false
	}
	reloadWPA(iface)
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		if wpaAssociatedTo(iface, ssid) {
			return true
		}
	}
	return false
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
	if b, err := os.ReadFile(wpaConfPath + ".bak"); err == nil {
		if werr := os.WriteFile(wpaConfPath, b, 0o600); werr != nil {
			s.logger.Warn("WLAN: rollback write failed", "err", werr)
		}
		_ = os.Remove(wpaConfPath + ".bak")
	}
	reloadWPA(iface)
}

// rebootBox triggers a detached reboot so BCO boxes apply the persisted creds
// through the boot-time provisioning path. sync flushes the NAND creds first.
func rebootBox() {
	_ = exec.Command("sh", "-c", "(sleep 1; sync; /sbin/reboot) </dev/null >/dev/null 2>&1 &").Start()
}

// buildWPAConfig generates a minimal wpa_supplicant.conf. With an empty
// password key_mgmt=NONE is set (open WLAN).
func buildWPAConfig(ssid, psk string) string {
	var b strings.Builder
	b.WriteString("ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=root\n")
	b.WriteString("update_config=1\n")
	b.WriteString("network={\n")
	b.WriteString("    ssid=\"" + escapeWPAValue(ssid) + "\"\n")
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

	resp, err := http.Get(fmt.Sprintf("http://%s:8090/now_playing", s.boxHost))
	if err != nil {
		// Fall back to the last cached body on a transient box error so a
		// brief BoseApp hiccup does not blank the now-playing display.
		s.statusMu.Lock()
		body, code, have := s.statusBody, s.statusCode, s.statusBody != nil
		s.statusMu.Unlock()
		if have {
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

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, indexHTML)
}
