// Package webui stellt das Config Web Interface auf Port 8888 bereit.
// Enthaelt die HTML UI plus eine REST API die spaeter auch von der Wails
// Desktop App genutzt wird.
package webui

import (
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
	"github.com/JRpersonal/streborn/internal/netutil"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/radiobrowser"
	"github.com/JRpersonal/streborn/internal/streamproxy"
	"github.com/JRpersonal/streborn/internal/upnp"
)

// Server kapselt den Webui HTTP Server.
type Server struct {
	addr        string
	boxHost     string
	logger      *slog.Logger
	presets     *presets.Store
	renderer    *upnp.Renderer
	autoPair    *autopair.Manager
	regionMu    sync.RWMutex
	region      string // ISO 3166-1 alpha-2 vom Setup Wizard, leer wenn unbekannt
	regionFile  string // Pfad fuer persistente Speicherung
	streamProxy *streamproxy.Server
	// spotifyStream serves the live WAV from the go-librespot manager to
	// the box over HTTP (registered at /spotify/stream). nil when Spotify
	// is not configured. Injected as a handler so webui need not import
	// the spotify package.
	spotifyStream http.HandlerFunc
	// spotifyPlay tells go-librespot to play a Spotify URI (the control
	// side of a Spotify preset recall). nil when Spotify is not
	// configured. Injected as a func for the same decoupling reason.
	spotifyPlay func(context.Context, string) error
	langCacheMu sync.Mutex
	langCache   map[string]langCacheEntry
	// wifiSignalFn returns the latest Wi-Fi signal class observed on the
	// gabbo WebSocket (set from cmd/agent's boxws client). Used to fill
	// the signal for BCO boxes, whose /networkInfo reports none.
	wifiSignalFn func() string

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
}

// statusCacheTTL bounds the staleness of a cached now_playing response and
// thus the maximum /now_playing hit rate against the Bose app to about
// 1/TTL per second regardless of client poll frequency.
const statusCacheTTL = 2 * time.Second

// SetWifiSignalFn wires a provider for the latest Wi-Fi signal class
// (from the boxws gabbo stream). cmd/agent calls this after creating the
// WebSocket client.
func (s *Server) SetWifiSignalFn(fn func() string) { s.wifiSignalFn = fn }

type langCacheEntry struct {
	Langs []radiobrowser.Language
	At    time.Time
}

// Option ist ein functional option fuer New.
type Option func(*Server)

// WithPresets verbindet den Store fuer Preset CRUD.
func WithPresets(p *presets.Store) Option {
	return func(s *Server) { s.presets = p }
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

// WithSpotifyStream registers the handler that serves go-librespot's
// live WAV to the box at /spotify/stream (the Spotify-preset audio
// plane).
func WithSpotifyStream(h http.HandlerFunc) Option {
	return func(s *Server) { s.spotifyStream = h }
}

// WithSpotifyControl registers the function that starts playback of a
// Spotify URI in go-librespot (the Spotify-preset control plane).
func WithSpotifyControl(play func(context.Context, string) error) Option {
	return func(s *Server) { s.spotifyPlay = play }
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
	mux.HandleFunc("/api/radio/search", s.handleRadioSearch)
	mux.HandleFunc("/api/radio/top", s.handleRadioTop)
	mux.HandleFunc("/api/radio/tags", s.handleRadioTags)
	mux.HandleFunc("/api/radio/languages", s.handleRadioLanguages)
	mux.HandleFunc("/api/radio/vote/", s.handleRadioVote)
	mux.HandleFunc("/api/radio/click/", s.handleRadioClick)
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
	mux.HandleFunc("/api/stick/status", s.handleStickStatus)
	mux.HandleFunc("/api/debug/state", s.handleDebugState)
	mux.HandleFunc("/api/debug/probe", s.handleDebugProbe)

	// Stream Proxy: stabile URLs fuer Radio Streams mit Token Expiry.
	// Siehe internal/streamproxy fuer Details.
	if s.streamProxy != nil {
		s.streamProxy.Register(mux)
	}
	if s.spotifyStream != nil {
		mux.HandleFunc("/spotify/stream", s.spotifyStream)
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
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		p.Slot = slot
		if p.Type == "" {
			p.Type = "radio"
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
			proxyURL := fmt.Sprintf("http://127.0.0.1:8888/stream/%d", slot)
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

func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	if s.renderer == nil {
		http.Error(w, "renderer not configured (set --box-host)", http.StatusServiceUnavailable)
		return
	}
	var req playRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.URL == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}
	s.ensureBoxReady(r.Context())
	// Stream durch unseren Proxy schicken — damit klappen auch
	// HTTPS Quellen (Bose UPnP kann kein TLS) und Token Expiry wird
	// transparent abgefangen. Bose sieht eine stabile loopback URL.
	playURL := "http://127.0.0.1:8888/stream/raw?u=" + base64.RawURLEncoding.EncodeToString([]byte(req.URL))
	if err := s.renderer.PlayURL(r.Context(), playURL, req.Title, req.Icon); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "Station could not be played",
			"detail": guessErrorReason(err),
			"url":    req.URL,
		})
		return
	}
	// Click Tracking: best effort, im Hintergrund.
	if req.UUID != "" {
		go func(uuid string) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			_ = radiobrowser.New().Click(ctx, uuid)
		}(req.UUID)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "playing", "url": req.URL})
}

func (s *Server) handlePlaySlot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	// Spotify presets have no playable HTTP StreamURL: tell go-librespot
	// to play the saved URI, then point the box at our live /spotify/stream.
	if p.Type == "spotify" && p.URI != "" {
		if s.spotifyPlay == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": "Spotify not configured", "slot": slot, "name": p.Name,
			})
			return
		}
		if err := s.spotifyPlay(r.Context(), p.URI); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": "Spotify could not be played", "detail": err.Error(),
				"slot": slot, "name": p.Name,
			})
			return
		}
		if err := s.renderer.PlayURL(r.Context(), "http://127.0.0.1:8888/spotify/stream", p.Name, p.Art); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": "Spotify stream could not be played", "detail": guessErrorReason(err),
				"slot": slot, "name": p.Name,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "playing", "slot": slot, "name": p.Name, "type": "spotify"})
		return
	}
	// Stream Proxy URL nutzen damit auch nach Token Expiry weitergespielt
	// wird (Bose sieht die stabile loopback URL).
	playURL := fmt.Sprintf("http://127.0.0.1:8888/stream/%d", slot)
	if err := s.renderer.PlayURL(r.Context(), playURL, p.Name, p.Art); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":  "Station could not be played",
			"detail": guessErrorReason(err),
			"slot":   slot,
			"name":   p.Name,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "playing", "slot": slot, "name": p.Name})
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
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

// handleRadioSearch proxied einen Radio-Browser.info Search Query.
// Query Parameter:
//
//	q       Name Suchbegriff
//	tag     Genre Tag
//	cc      Country Code (ISO 2)
//	order   "votes" | "clickcount" | "clicktrend" | "name" (default votes)
//	limit   max Ergebnisse (default 30)
//	onlyok  "1" um nur funktionierende Sender zu liefern
func (s *Server) handleRadioSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rb := radiobrowser.New()
	ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
	defer cancel()
	limit := 30
	if v := q.Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		fmt.Sscanf(v, "%d", &offset)
	}
	stations, err := rb.SearchSmart(ctx, radiobrowser.SearchOpts{
		Name:     q.Get("q"),
		Tag:      q.Get("tag"),
		Country:  q.Get("cc"),
		Language: q.Get("lang"),
		Order:    q.Get("order"),
		Limit:    limit,
		Offset:   offset,
		OnlyOK:   q.Get("onlyok") == "1",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, stations)
}

// handleRadioTop liefert die meistgevoteten Sender. Country Filter
// kommt strikt vom Client. Frueher defaultete der Server still auf
// "DE" wenn cc fehlte — das hat den Frontend-Filter "alle Laender"
// (der cc=leer schickt) sabotiert: User bekam trotzdem nur DE Sender.
// Jetzt: cc leer = keine Country Filter, Top global. Wenn der User
// DE will, schickt das Frontend cc=DE explizit, was es per Default
// auch tut (state.searchCountry: 'DE' bis der User aktiv umschaltet).
// Respektiert ALLE Filter (tag, lang, order, onlyok, offset). Default
// sort ist votes desc; mit q.Get("order") explizit ueberschreibbar.
func (s *Server) handleRadioTop(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cc := q.Get("cc")
	limit := 30
	if v := q.Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		fmt.Sscanf(v, "%d", &offset)
	}
	order := q.Get("order")
	if order == "" {
		order = "votes"
	}
	rb := radiobrowser.New()
	ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
	defer cancel()
	stations, err := rb.Search(ctx, radiobrowser.SearchOpts{
		Country:  cc,
		Tag:      q.Get("tag"),
		Language: q.Get("lang"),
		Order:    order,
		Limit:    limit,
		Offset:   offset,
		OnlyOK:   q.Get("onlyok") == "1",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, stations)
}

// handleRadioTags liefert eine Liste der populaerstn Genre Tags fuer die
// Chip Filter UI. Default 60.
func (s *Server) handleRadioTags(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 60
	if v := q.Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	rb := radiobrowser.New()
	ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
	defer cancel()
	tags, err := rb.TopTags(ctx, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, tags)
}

// handleRadioLanguages liefert die Sprach Liste — global, oder mit
// ?country=XX gefiltert auf alle Stationen die in diesem Land sind
// (radio-browser hat dafuer keinen direkten Endpoint, wir aggregieren
// ueber Stations Search). Mit Cache 10 min pro Country damit nicht
// jeder Filter Click eine fette Search Query macht.
func (s *Server) handleRadioLanguages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 40
	if v := q.Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	country := strings.ToUpper(strings.TrimSpace(q.Get("country")))

	rb := radiobrowser.New()
	ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
	defer cancel()

	if country != "" {
		// Cache lookup
		s.langCacheMu.Lock()
		entry, ok := s.langCache[country]
		s.langCacheMu.Unlock()
		if ok && time.Since(entry.At) < 10*time.Minute {
			writeJSON(w, http.StatusOK, entry.Langs)
			return
		}
		langs, err := rb.LanguagesByCountry(ctx, country)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// Limit anwenden falls grosse Liste
		if limit > 0 && len(langs) > limit {
			langs = langs[:limit]
		}
		s.langCacheMu.Lock()
		if s.langCache == nil {
			s.langCache = make(map[string]langCacheEntry)
		}
		s.langCache[country] = langCacheEntry{Langs: langs, At: time.Now()}
		s.langCacheMu.Unlock()
		writeJSON(w, http.StatusOK, langs)
		return
	}

	langs, err := rb.Languages(ctx, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, langs)
}

// handleRadioVote leitet einen Vote (Daumen hoch) an radio-browser durch.
// Path Format: /api/radio/vote/<uuid>
func (s *Server) handleRadioVote(w http.ResponseWriter, r *http.Request) {
	uuid := strings.TrimPrefix(r.URL.Path, "/api/radio/vote/")
	if uuid == "" {
		http.Error(w, "uuid fehlt", http.StatusBadRequest)
		return
	}
	rb := radiobrowser.New()
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	if err := rb.Vote(ctx, uuid); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "voted", "uuid": uuid})
}

// handleRadioClick erhoeht den Click Counter fuer einen Sender.
// Path Format: /api/radio/click/<uuid>
func (s *Server) handleRadioClick(w http.ResponseWriter, r *http.Request) {
	uuid := strings.TrimPrefix(r.URL.Path, "/api/radio/click/")
	if uuid == "" {
		http.Error(w, "uuid fehlt", http.StatusBadRequest)
		return
	}
	rb := radiobrowser.New()
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	if err := rb.Click(ctx, uuid); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "clicked", "uuid": uuid})
}

// handleAgentVersion liefert die laufende Stick Agent Version. Wird von
// der Desktop App genutzt um zu erkennen ob ein Update faellig ist.
func (s *Server) handleAgentVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version": agentVersion(),
		"build":   agentBuild(),
	})
}

// handleAgentUpdate nimmt ein neues Stick Agent Binary entgegen, schreibt
// es atomar nach /mnt/nv/streborn/bin/streborn-armv7l und
// startet den Agent neu. Body muss das rohe ARM Binary sein.
//
// Nach Erfolg gibt der Stick noch 200 OK zurueck und beendet sich. Der
// rc.local Bootstrap startet den neuen Agent.
func (s *Server) handleAgentUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
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
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	var req struct {
		Value int `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
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
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Source string `json:"source"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
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
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Value int `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
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

// handleBoxZone liest die aktuelle SoundTouch Multiroom Zone der Box.
// Read-only. Antwort ist {"master":"...","senderIP":"...","members":[...]}.
// Bei einer Box ohne Zone ist master leer und members leer.
func (s *Server) handleBoxZone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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

// handleBoxGroup liest die aktuelle Stereo-Pair-Group der Box.
// Read-only. Antwort ist {"id":"...","name":"...","members":[...]}.
// Bei einer Box ohne Pair ist id leer und members leer.
func (s *Server) handleBoxGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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

// handleBoxSyncPresets ueberschreibt die Box eigene Preset Liste mit
// allen aktuellen Stick Presets via Bose CLI. Damit funktionieren die
// Hardware Tasten 1-6 wieder wenn der initial Sync beim Boot aus
// irgendwelchen Gruenden nicht durchgelaufen ist (z.B. Box war noch
// nicht erreichbar).
func (s *Server) handleBoxSyncPresets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.presets == nil || s.boxHost == "" {
		http.Error(w, "presets store oder box host nicht konfiguriert", http.StatusServiceUnavailable)
		return
	}
	var specs []boxcli.PresetSpec
	for _, p := range s.presets.All() {
		specs = append(specs, boxcli.PresetSpec{
			Slot:      p.Slot,
			Name:      p.Name,
			StreamURL: p.StreamURL,
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
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
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
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
