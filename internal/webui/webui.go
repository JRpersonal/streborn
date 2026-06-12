// Package webui stellt das Config Web Interface auf Port 8888 bereit.
// Enthaelt die HTML UI plus eine REST API die spaeter auch von der Wails
// Desktop App genutzt wird.
package webui

import (
	"context"
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
	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/netutil"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/streamproxy"
	"github.com/JRpersonal/streborn/internal/upnp"
	"github.com/JRpersonal/streborn/internal/webhooks"
	"github.com/JRpersonal/streborn/internal/zones"
)

// Server kapselt den Webui HTTP Server.
type Server struct {
	addr        string
	boxHost     string
	logger      *slog.Logger
	presets     *presets.Store
	// zones persists this box's multiroom membership so a zone auto-reforms
	// after reboot/standby (#70). nil when not wired; zone write endpoints
	// then still drive the box but do not persist.
	zones *zones.Store
	renderer    *upnp.Renderer
	autoPair    *autopair.Manager
	regionMu    sync.RWMutex
	region      string // ISO 3166-1 alpha-2 vom Setup Wizard, leer wenn unbekannt
	regionFile  string // Pfad fuer persistente Speicherung
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
}

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
// the user plays something new. With the exponential backoff the attempts span
// ~30s rather than the dozens-per-second runaway it replaces.
const maxRePushes = 5

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

// Option ist ein functional option fuer New.
type Option func(*Server)

// WithPresets verbindet den Store fuer Preset CRUD.
func WithPresets(p *presets.Store) Option {
	return func(s *Server) { s.presets = p }
}

// WithZones wires the multiroom zone persistence store so formed zones survive
// a reboot/standby and auto-reform (#70).
func WithZones(z *zones.Store) Option {
	return func(s *Server) { s.zones = z }
}

// WithBoxHost setzt die Bose Box IP/Hostname fuer UPnP Calls.
func WithBoxHost(host string) Option {
	return func(s *Server) {
		s.boxHost = host
		s.renderer = upnp.NewBoseRenderer(host)
	}
}

// WithAutoPair gibt dem Server Zugriff auf den AutoPair Manager damit
// Play Calls auch nach Standby Aufwecken die Box wieder pairen koennen.
func WithAutoPair(m *autopair.Manager) Option {
	return func(s *Server) { s.autoPair = m }
}

// WithRegion uebergibt den vom Setup Wizard gewaehlten Country Code.
// Wird ueber /api/region exposed damit die Desktop App ihre Defaults
// fuer Radio Suche und Sprache daraus ableiten kann.
func WithRegion(cc string) Option {
	return func(s *Server) { s.region = strings.ToUpper(cc) }
}

// WithRegionFile setzt den persistenten Pfad fuer Aenderungen von
// /api/region (PUT). Ohne diesen Pfad sind Aenderungen nur in memory.
func WithRegionFile(path string) Option {
	return func(s *Server) { s.regionFile = path }
}

// WithStreamProxy haengt den Stream Proxy ein. Wenn gesetzt wird der
// /stream/ Endpoint registriert. Bose ContentItems werden dann mit
// http://127.0.0.1:8888/stream/<slot> verlinkt statt mit der echten
// CDN URL — Streams ueberleben Token Expiry.
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

// WithSpotifySetRecalling registers the hook that marks an in-flight recall so
// ServeOgg drives the new track from its start.
func WithSpotifySetRecalling(setRecalling func()) Option {
	return func(s *Server) { s.spotifySetRecalling = setRecalling }
}

// ensureBoxReady weckt die Box aus dem Standby (mit retry+poll bis
// wirklich wach) und stellt sicher dass der Marge Account aktiv ist.
// Wird vor jedem Play Call aufgerufen.
func (s *Server) ensureBoxReady(ctx context.Context) {
	if s.boxHost != "" {
		wakeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		if err := boxcli.WakeAndWait(wakeCtx, s.boxHost, 6*time.Second, s.logger); err != nil {
			s.logger.Warn("Box konnte nicht aus STANDBY geholt werden", "err", err)
		}
		cancel()
	}
	if s.autoPair != nil {
		pairCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		s.autoPair.TriggerNow(pairCtx)
		cancel()
	}
}

// New erstellt einen neuen Webui Server.
func New(addr string, logger *slog.Logger, opts ...Option) *Server {
	s := &Server{addr: addr, logger: logger}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Run startet den Server und blockiert bis ctx abgebrochen wird.
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
	mux.HandleFunc("/api/box/sync-presets", s.handleBoxSyncPresets)
	mux.HandleFunc("/api/box/zone", s.handleBoxZone)
	mux.HandleFunc("/api/box/group", s.handleBoxGroup)
	mux.HandleFunc("/api/webhooks", s.handleWebhooks)
	mux.HandleFunc("/api/webhooks/test", s.handleWebhooksTest)
	mux.HandleFunc("/api/stick/status", s.handleStickStatus)
	mux.HandleFunc("/api/debug/state", s.handleDebugState)
	mux.HandleFunc("/api/debug/probe", s.handleDebugProbe)

	// Stream Proxy: stabile URLs fuer Radio Streams mit Token Expiry.
	// Siehe internal/streamproxy fuer Details.
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

// corsMiddleware erlaubt Cross-Origin Calls von der Desktop App.
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
		// Sync zur Box damit Hardware Tasten den richtigen Slot kennen.
		// Bose bekommt die Stream Proxy URL, nicht den echten CDN.
		// So ueberlebt der Stream Token Expiry.
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

// ---- Play / Pause / Stop ----

type playRequest struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Icon  string `json:"icon"` // albumArtURI fuer Box Display
	UUID  string `json:"uuid"` // optional, fuer Click Tracking
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
	if a.URL == "" {
		a = s.webhooks.Get().Thumb
	}
	if a.URL == "" {
		http.Error(w, "no URL to test", http.StatusBadRequest)
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
	// Stream durch unseren Proxy schicken — damit klappen auch
	// HTTPS Quellen (Bose UPnP kann kein TLS) und Token Expiry wird
	// transparent abgefangen. Bose sieht eine stabile loopback URL.
	playURL := boxurl.RawStream(req.URL)
	if err := s.renderer.PlayURL(r.Context(), playURL, req.Title, req.Icon); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "Station could not be played",
			"detail": guessErrorReason(err),
			"url":    req.URL,
		})
		return
	}
	s.setLastPlay(playURL, req.Title, req.Icon, "")
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
			s.verifyRecall(func(ctx context.Context) {
				if s.spotifyPlay(ctx, uri, account) == nil {
					_ = s.renderer.PlayURLMime(ctx, slotURL, name, art, "audio/ogg")
				}
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
	// Stream Proxy URL nutzen damit auch nach Token Expiry weitergespielt
	// wird (Bose sieht die stabile loopback URL).
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
	name, art := p.Name, p.Art
	go s.verifyRecall(func(ctx context.Context) {
		_ = s.renderer.PlayURL(ctx, playURL, name, art)
	}, nil)
	writeJSON(w, http.StatusOK, map[string]any{"status": "playing", "slot": slot, "name": p.Name})
}

// verifyRecall confirms the box reached a playing state shortly after a recall
// and re-issues the play a few times if not. Fixes the "first press after a
// reboot does nothing, second press works" race (box/go-librespot not ready
// yet) without any latency on the happy path (the initial play already ran).
func (s *Server) verifyRecall(retry func(context.Context), working func() bool) {
	for attempt := 1; attempt <= 3; attempt++ {
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
		retry(ctx)
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

// ResumeLastPlay re-pushes the last stream STR played. It is the power-button
// wake-from-standby resume: the box gives no powerStateUpdated, it just tries
// (and declines, DO_NOT_RESUME) to restore its last UPNP selection, which boxws
// surfaces as OnWakeResume. Power-on is an explicit "play it again", so this
// overrides the user-stop the power-off STOP_STATE set and clears the
// failed/attempt state so even a previously dead stream gets one fresh try.
func (s *Server) ResumeLastPlay() {
	if s.renderer == nil {
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
		// unambiguous before we decide. The DO_NOT_RESUME that triggers this
		// fires on BOTH a power-on wake and a deliberate power-off (the box
		// tears down its UPNP selection either way), so the event alone cannot
		// tell them apart: the box state can.
		time.Sleep(2 * time.Second)

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
	if !icyDisplayPushEnabled() || s.renderer == nil || title == "" {
		return
	}
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	if lp == nil {
		s.lastPlayMu.Unlock()
		return
	}
	boxURL, art, mime := lp.boxURL, lp.art, lp.mime
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
		err = s.renderer.SetURIMime(ctx, boxURL, title, art, mime)
	} else {
		err = s.renderer.SetURI(ctx, boxURL, title, art)
	}
	if err != nil {
		s.logger.Warn("icy display push failed", "err", err, "title", title)
		return
	}
	s.logger.Info("icy display push", "title", title)
}

// boxPlayState reads now_playing once and reports whether the box is in standby
// and whether it is busy (playing, buffering or paused). Best-effort: on error
// it reports neither, so the caller does not re-push blindly.
func (s *Server) boxPlayState() (standby, busy bool) {
	if s.boxHost == "" {
		return false, false
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get("http://" + s.boxHost + ":8090/now_playing")
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	body := string(b)
	standby = strings.Contains(body, "STANDBY")
	busy = strings.Contains(body, "PLAY_STATE") || strings.Contains(body, "BUFFERING_STATE") || strings.Contains(body, "PAUSE_STATE")
	return standby, busy
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

// handleAgentVersion liefert die laufende Stick Agent Version. Wird von
// der Desktop App genutzt um zu erkennen ob ein Update faellig ist.
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

// handleAgentUpdate nimmt ein neues Stick Agent Binary entgegen, schreibt
// es atomar nach /mnt/nv/streborn/bin/streborn-armv7l und
// startet den Agent neu. Body muss das rohe ARM Binary sein.
//
// Nach Erfolg gibt der Stick noch 200 OK zurueck und beendet sich. Der
// rc.local Bootstrap startet den neuen Agent.
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

// isLocalLAN true wenn der Request von einer privaten LAN IP kommt
// (RFC1918) oder localhost. Update aus dem Internet wird blockiert.
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

// guessErrorReason konvertiert den technischen UPnP / Netzwerk Fehler
// in einen menschen-lesbaren Hinweis. Die SOAP Antworten der Box sind
// stark in XML eingewickelt und nicht direkt verstaendlich.
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

// handleBoxSettings liefert info + volume + bass + network + sources
// kombiniert als JSON.
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

// handleBoxName PUT setzt den Box Namen. Body {"name":"..."}.
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
		http.Error(w, "name leer", http.StatusBadRequest)
		return
	}
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	if err := c.SetName(ctx, req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// Bose setzt bei /name POST nebenher die margeURL auf default zurueck —
	// AutoPair triggern damit der Pair State unmittelbar wieder hergestellt
	// wird.
	if s.autoPair != nil {
		go s.autoPair.TriggerNow(context.Background())
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "name": req.Name})
}

// handleBoxVolume PUT setzt Lautstaerke. Body {"value":N}.
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

// handleBoxSource PUT schaltet die Box auf eine andere Quelle: AUX,
// BLUETOOTH oder STANDBY. Body {"source":"AUX"}.
//
// Bose /select erwartet ein ContentItem XML. Wir bauen das je nach
// Source. STANDBY hat ein eigenes ContentItem ohne sourceAccount.
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

	// Sonderfall STANDBY: kein ContentItem Source bei Bose. /key POWER
	// triggert nur LED Animation, /standby ist der echte Endpoint —
	// und Bose erwartet **GET**, kein POST (POST liefert 400).
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

// handleBoxBass PUT setzt Bass Wert. Body {"value":N}.
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

	// Persist first so a transient drive error still leaves the group on record
	// for the reconcile loop to retry. Only the master persists.
	z := zones.Zone{Master: master.DeviceID, MasterIP: master.IP, Stereo: req.Stereo, Mode: mode, Name: req.Name}
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
	c := boxapi.New(s.boxHost)
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
	s.logger.Info("zone: formed", "mode", "native", "liveMaster", z2.Master, "liveMembers", len(z2.Members))
	writeJSON(w, http.StatusOK, z2)
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

// PeriodicZoneReconcile re-asserts a persisted group so it survives
// reboot/standby/Wi-Fi outage (#70 beta). No-op when standalone. Started by
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
	// Prefer the persisted membership; fall back to the live zone so a dissolve
	// still works after an agent restart.
	if s.zones != nil {
		if z, ok := s.zones.Get(); ok {
			master = boxapi.ZoneMember{DeviceID: z.Master, IP: z.MasterIP}
			for _, m := range z.Slaves {
				slaves = append(slaves, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
			}
		}
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
		if err := c.RemoveZoneSlave(ctx, master, slaves); err != nil {
			// Log but still clear the store so we stop re-forming a broken zone.
			s.logger.Warn("zone: removeZoneSlave failed", "err", err)
		}
	}
	if s.zones != nil {
		if err := s.zones.Clear(); err != nil {
			s.logger.Warn("zone: clear store failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleBoxGroup liest die aktuelle Stereo-Pair-Group der Box.
// Read-only. Antwort ist {"id":"...","name":"...","members":[...]}.
// Bei einer Box ohne Pair ist id leer und members leer.
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

// handleStickStatus liefert ob der USB Stick aktuell in der Box steckt
// (= /media/sda1 ist gemountet). Optional die Stick Version aus
// version.txt wenn lesbar. Sehr leichtgewichtig — eine os.Stat.
func (s *Server) handleStickStatus(w http.ResponseWriter, _ *http.Request) {
	info, err := os.Stat("/media/sda1")
	mounted := err == nil && info != nil && info.IsDir()
	out := map[string]any{"mounted": mounted}
	if mounted {
		if b, rerr := os.ReadFile("/media/sda1/version.txt"); rerr == nil {
			out["version"] = strings.TrimSpace(string(b))
		}
	}
	// SSH Status — schauen ob Port 22 aktuell listened. Wenn ja
	// kann jemand im LAN auf die Box zugreifen, App zeigt Warn Banner.
	// Wir probieren einen TCP Connect auf localhost mit 200 ms timeout.
	if conn, dialErr := net.DialTimeout("tcp", "127.0.0.1:22", 200*time.Millisecond); dialErr == nil {
		_ = conn.Close()
		out["sshOpen"] = true
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDebugState liefert wichtige Box State Dateien als JSON damit
// wir ohne SSH von aussen debuggen koennen wenn der Stick eingebaut ist.
//
// Wird nur fuer interaktive Diagnose genutzt — die App selbst ruft das
// nicht regelmaessig. Limit pro Datei: 8 KB damit die Antwort kompakt
// bleibt.
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

// handleBoxSyncPresets ueberschreibt die Box eigene Preset Liste mit
// allen aktuellen Stick Presets via Bose CLI. Damit funktionieren die
// Hardware Tasten 1-6 wieder wenn der initial Sync beim Boot aus
// irgendwelchen Gruenden nicht durchgelaufen ist (z.B. Box war noch
// nicht erreichbar).
func (s *Server) handleBoxSyncPresets(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.presets == nil || s.boxHost == "" {
		http.Error(w, "presets store oder box host nicht konfiguriert", http.StatusServiceUnavailable)
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

// handleBoxReboot startet die Box neu via shell `reboot`. Wird genutzt
// damit conf Files vom Stick (wlan / region / name) beim run.sh Boot
// Pfad applied werden — wir vermeiden so ein dauerhaft laufendes USB
// Watcher Polling.
func (s *Server) handleBoxReboot(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "reboot only allowed from LAN", http.StatusForbidden)
		return
	}
	s.logger.Info("Box Reboot vom User angefordert")
	writeJSON(w, http.StatusOK, map[string]string{"status": "rebooting"})
	// 1s spaeter ausfuehren damit unsere HTTP Response noch raus geht.
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

// handleBoxWLAN setzt die WLAN Konfiguration der Box zur Laufzeit.
// Body: {"ssid":"...", "password":"..."}
//
// Vorgehen: wir schreiben /mnt/nv/wpa_supplicant.conf neu und schicken
// SIGHUP an wpa_supplicant. Das ist exakt der Weg den run.sh beim
// initialen WLAN Provisioning ueber USB nutzt — funktioniert also
// auch zur Laufzeit. Bei falschem Passwort verliert die Box die
// Netzverbindung; User muss dann manuell Werks Reset oder neuen Stick.
//
// Nur fuer LAN Clients erlaubt damit nicht zufaellig Internet Calls
// das WLAN umstellen.
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
		http.Error(w, "ssid darf nicht leer sein", http.StatusBadRequest)
		return
	}
	// PSK darf laut WPA Standard mindestens 8 Zeichen sein. Wenn der User
	// ein offenes WLAN konfiguriert, lassen wir das Passwort weg.
	if req.Password != "" && len(req.Password) < 8 {
		http.Error(w, "passwort zu kurz (mindestens 8 Zeichen)", http.StatusBadRequest)
		return
	}

	conf := buildWPAConfig(req.SSID, req.Password)
	const wpaPath = "/mnt/nv/wpa_supplicant.conf"
	tmp := wpaPath + ".new"
	if err := os.WriteFile(tmp, []byte(conf), 0o600); err != nil {
		http.Error(w, "write conf: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmp, wpaPath); err != nil {
		http.Error(w, "rename conf: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// SIGHUP an wpa_supplicant: laedt die conf neu und switcht
	// asynchron auf das neue Netz. Wenn der Daemon nicht laeuft,
	// loggen wir nur — beim naechsten Box Boot wird die Conf eh
	// gelesen.
	if err := sighupWPA(); err != nil {
		s.logger.Warn("SIGHUP to wpa_supplicant failed", "err", err)
	}
	s.logger.Info("WLAN umgeschaltet", "ssid", req.SSID)
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"ssid":   req.SSID,
	})
}

// buildWPAConfig erzeugt eine minimale wpa_supplicant.conf. Bei leerem
// Passwort wird key_mgmt=NONE gesetzt (offenes WLAN).
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
	// Backslash und Doublequote escapen
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return r.Replace(s)
}

// sighupWPA findet die wpa_supplicant PID und schickt SIGHUP. Ueber
// killall waere kuerzer, aber auf der busybox Box ist killall ohne
// Signal Argument ein Plain Term. Wir gehen den expliziten Weg.
func sighupWPA() error {
	b, err := os.ReadFile("/var/run/wpa_supplicant.pid")
	if err == nil {
		pidStr := strings.TrimSpace(string(b))
		if pid, perr := strconv.Atoi(pidStr); perr == nil && pid > 0 {
			return syscall.Kill(pid, syscall.SIGHUP)
		}
	}
	// Fallback: alle wpa_supplicant Prozesse
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, perr := strconv.Atoi(e.Name())
		if perr != nil {
			continue
		}
		comm, _ := os.ReadFile("/proc/" + e.Name() + "/comm")
		if strings.TrimSpace(string(comm)) == "wpa_supplicant" {
			_ = syscall.Kill(pid, syscall.SIGHUP)
		}
	}
	return nil
}

// countryToLanguage liefert den Default Sprach Code fuer das radio-browser
// "language" Filter Feld aus einem ISO 3166-1 Country Code. Fallback ist
// "english" wenn unbekannt — die Welt versteht das.
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

// handleRegion liefert die vom Setup Wizard gespeicherte Region samt
// abgeleiteter Default Sprache, oder setzt sie neu via PUT.
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
			http.Error(w, "country muss ISO 3166-1 alpha-2 sein", http.StatusBadRequest)
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

// agentVersion und agentBuild werden via Setter aus main.go versorgt.
var (
	agentVersion = func() string { return "1.0.0" }
	agentBuild   = func() string { return "dev" }
)

// SetAgentVersion erlaubt main.go beim Start die Semver Version zu setzen.
func SetAgentVersion(v string) { agentVersion = func() string { return v } }

// SetAgentBuild setzt den Build Stamp (Datum/Commit) als zusaetzliche Info.
func SetAgentBuild(b string) { agentBuild = func() string { return b } }

// handleStatus proxied das now_playing XML der Box, mit einem kurzen
// Micro-Cache (statusCacheTTL) davor. Mehrere oder zu schnell pollende
// Clients teilen sich so denselben Box-Roundtrip, statt die fragile
// BoseApp (:8090) auf jeder Abfrage neu zu treffen.
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
