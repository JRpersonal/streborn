// Package streamproxy verpackt fremde Radio Streams in eine stabile
// URL die Bose's UPnP Player nicht mehr loslassen kann.
//
// Hintergrund: viele moderne Radiosender (1LIVE, SWR3, Rock Antenne via
// streamonkey) antworten mit HTTP 302 Redirect auf eine CDN URL mit
// signed Token. Bose's UPnP Player folgt dem Redirect zwar, behaelt aber
// die per-Token-URL. Wenn der Token nach einigen Stunden ablaeuft, killt
// die CDN die Verbindung — Bose merkt das als "Stream tot" und geht in
// INVALID_SOURCE. User Eindruck: "Sender hoert nach einer Weile auf zu
// spielen".
//
// Mit diesem Proxy sieht Bose immer dieselbe URL
// `http://127.0.0.1:8888/stream/<slot>`. Der Stick Agent loest intern
// den Redirect zur CDN auf und streamt die Bytes durch. Wenn die CDN
// die Verbindung killt (Token expiry), connectet der Proxy SOFORT
// neu — Bose's TCP Verbindung bleibt offen, Bose merkt keinen Drop.
package streamproxy

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// failLogSuppressWindow is the minimum spacing between identical
// "upstream fail" warnings for the same URL. Bose's UPnP player
// re-hits the proxy when a station is unreachable, so without this
// the agent log fills with the same NXDOMAIN line several times a
// minute for a single dead preset.
const failLogSuppressWindow = 30 * time.Second

// safeHTTPURL rejects everything that is not http or https. Used at
// every outbound HTTP call site that takes a URL from a
// not-strictly-trusted source (preset store, base64-decoded query
// param). Belt-and-braces: handleRaw already pre-checks the
// HasPrefix path, but streamOne is also reachable via handle()
// where the URL comes straight from the preset store and could in
// principle contain anything. CodeQL flagged streamOne's outbound
// Do() exactly for this reason. Centralising the check keeps the
// policy obvious.
func safeHTTPURL(raw string) error {
	if raw == "" {
		return errors.New("url is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("disallowed url scheme %q (only http/https accepted)", u.Scheme)
	}
}

type Server struct {
	store  *presets.Store
	logger *slog.Logger
	client *http.Client

	failMu   sync.Mutex
	lastFail map[string]time.Time

	// brMu guards the detected bitrate of the stream currently being
	// proxied. We learn it from the upstream Icecast/Shoutcast "icy-br"
	// header (exact, instant) or, when that is absent, by measuring
	// steady-state throughput. radio-browser's catalogue bitrate is often
	// missing or wrong, so this real value is what the UI shows for
	// now-playing. measuredBr locks in the value per stream URL so an
	// internal reconnect (token expiry) reuses it instead of re-measuring
	// and producing a different number on every UI poll.
	brMu       sync.Mutex
	curBitrate int
	curURL     string
	measuredBr map[string]int

	// onDisconnect, if set, is called when the Bose renderer closes a stream.
	// The argument is the last upstream error (nil = upstream was healthy, so
	// the box dropped the stream itself). Used by the auto-re-push.
	onDisconnect func(upstreamErr error)
}

// SetOnDisconnect registers a callback invoked whenever the box closes a
// proxied stream (raw or slot). Set once at wiring time.
func (s *Server) SetOnDisconnect(fn func(upstreamErr error)) { s.onDisconnect = fn }

func New(store *presets.Store, logger *slog.Logger) *Server {
	return &Server{
		store:  store,
		logger: logger,
		// Eigener Client damit wir Redirect Verhalten kontrollieren.
		// Default ist Follow bis 10 — passt fuer Streamonkey & Co.
		// Kein Timeout: Streams sind endlos, wir lesen bis EOF.
		client:     &http.Client{},
		lastFail:   make(map[string]time.Time),
		measuredBr: make(map[string]int),
	}
}

// CurrentBitrate returns the detected bitrate (kbps) of the stream being
// proxied right now, or 0 if unknown.
func (s *Server) CurrentBitrate() int {
	s.brMu.Lock()
	defer s.brMu.Unlock()
	return s.curBitrate
}

// beginStream marks url as the stream now playing and seeds curBitrate:
// the icy-br value if the station sent one, else a value already measured
// for this URL in a previous play (so a reconnect or a re-play does not
// briefly show "-"), else 0 (unknown, to be measured). Crucially it always
// updates curURL so switching from a station that had a bitrate to one that
// does not clears the stale number instead of leaving the old one showing.
func (s *Server) beginStream(url string, icyBr int) (known bool) {
	s.brMu.Lock()
	defer s.brMu.Unlock()
	s.curURL = url
	if icyBr > 0 {
		s.curBitrate = icyBr
		s.measuredBr[url] = icyBr
		return true
	}
	if br, ok := s.measuredBr[url]; ok {
		s.curBitrate = br
		return true
	}
	s.curBitrate = 0
	return false
}

// rememberBitrate stores a throughput-measured bitrate for url and makes it
// the current value, so internal reconnects reuse it rather than measuring
// a fresh (and slightly different) number on every UI poll. The map is
// capped so the ad-hoc search-play path cannot grow it without bound.
func (s *Server) rememberBitrate(url string, br int) {
	if br <= 0 {
		return
	}
	s.brMu.Lock()
	defer s.brMu.Unlock()
	if len(s.measuredBr) > 64 {
		s.measuredBr = make(map[string]int)
	}
	s.measuredBr[url] = br
	s.curURL = url
	s.curBitrate = br
}

// icyBitrate pulls the real stream bitrate (kbps) from the Icecast/
// Shoutcast response headers, 0 if none present. icy-br is sometimes a
// comma list ("128,128"); the first value is used.
func icyBitrate(h http.Header) int {
	for _, k := range []string{"icy-br", "ice-bitrate", "x-audiocast-bitrate"} {
		v := h.Get(k)
		if v == "" {
			continue
		}
		if i := strings.IndexByte(v, ','); i >= 0 {
			v = v[:i]
		}
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// standardBitrates are the common MP3/AAC stream rates a throughput
// measurement is snapped to, so the displayed value is stable and
// realistic instead of a jittery raw number.
var standardBitrates = []int{32, 48, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 448, 512}

// roundStandardBitrate snaps a throughput-derived kbps to the nearest
// common audio stream rate. Steady-state throughput of a playing stream
// sits very close to its real bitrate, so the nearest standard rate is the
// honest value. Anything above the highest audio rate is still buffer-fill
// burst, not a real bitrate, and returns 0 (shown as "-" rather than a
// misleading number like 1310).
func roundStandardBitrate(kbps int) int {
	if kbps <= 0 || kbps > 600 {
		return 0
	}
	best, bestDelta := 0, 1<<30
	for _, std := range standardBitrates {
		d := kbps - std
		if d < 0 {
			d = -d
		}
		if d < bestDelta {
			bestDelta, best = d, std
		}
	}
	return best
}

// shouldLogFail returns true if a fresh WARN line should be emitted
// for this URL. Within failLogSuppressWindow of the previous emit it
// returns false so the agent log does not repeat the same
// "upstream fail" line every time Bose's UPnP player retries an
// unreachable station.
func (s *Server) shouldLogFail(url string) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	now := time.Now()
	if last, ok := s.lastFail[url]; ok && now.Sub(last) < failLogSuppressWindow {
		return false
	}
	s.lastFail[url] = now
	return true
}

// Handler registriert /stream/<slot> sowie /stream/raw fuer ad-hoc URLs
// (z.B. aus der Radio Suche) auf den uebergebenen Mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/stream/raw", s.handleRaw)
	mux.HandleFunc("/stream/", s.handle)
	mux.HandleFunc("/api/stream/bitrate", s.handleBitrate)
}

// handleBitrate returns the real bitrate (kbps) of the stream currently
// proxied, detected from the upstream icy-br header or a throughput
// sample. 0 means unknown. Cheap: a single guarded int read. The desktop
// app fetches this once per station change (not on a timer) to keep box
// load minimal.
func (s *Server) handleBitrate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"bitrate":%d}`, s.CurrentBitrate())
}

// handleRaw streamt eine beliebige URL durch — vom Radio Suche
// Play Pfad genutzt damit Bose's UPnP auch HTTPS Streams via uns
// bekommen kann. URL kommt als ?u=<base64url> Parameter.
func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	enc := r.URL.Query().Get("u")
	if enc == "" {
		http.Error(w, "u missing", http.StatusBadRequest)
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		// Fallback: vielleicht plain URL-encoded
		decoded = []byte(enc)
	}
	url := string(decoded)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		http.Error(w, "invalid url scheme", http.StatusBadRequest)
		return
	}
	s.logger.Info("stream proxy raw start", "url", url)
	start := time.Now()
	headersSent := false
	var lastErr error
	for attempt := 0; attempt < 60; attempt++ {
		if r.Context().Err() != nil {
			s.logger.Info("stream proxy end: client gone", "kind", "raw", "elapsed", time.Since(start).Round(time.Second).String())
			return
		}
		if attempt > 0 {
			s.logger.Info("stream proxy reconnect", "kind", "raw", "attempt", attempt, "lastErr", errStr(lastErr))
			select {
			case <-time.After(500 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		var boseAlive bool
		boseAlive, lastErr = s.streamOne(r.Context(), w, r, url, !headersSent)
		if !boseAlive {
			// Bose closed the connection (station switch, standby) — a
			// normal end, distinct from the give-up case below.
			s.logger.Info("stream proxy end: bose disconnected", "kind", "raw", "elapsed", time.Since(start).Round(time.Second).String(), "lastErr", errStr(lastErr))
			if s.onDisconnect != nil {
				s.onDisconnect(lastErr)
			}
			return
		}
		headersSent = true
	}
	// 60 reconnects exhausted: the box still wanted bytes but upstream
	// kept failing. A network error in lastErr points at the box's
	// outbound path (e.g. a flaky wired link) rather than the box itself.
	s.logger.Warn("stream proxy gave up reconnecting", "kind", "raw", "attempts", 60, "elapsed", time.Since(start).Round(time.Second).String(), "lastErr", errStr(lastErr))
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	slotStr := strings.TrimPrefix(r.URL.Path, "/stream/")
	slot, err := strconv.Atoi(slotStr)
	if err != nil || slot < 1 || slot > 6 {
		http.Error(w, "invalid slot", http.StatusBadRequest)
		return
	}
	p, ok := s.store.Get(slot)
	if !ok || p.StreamURL == "" {
		http.Error(w, "no preset", http.StatusNotFound)
		return
	}
	s.logger.Info("stream proxy start", "slot", slot, "name", p.Name)

	// Wir machen genau einen GET zum CDN, kopieren bytes auf Bose.
	// Wenn CDN EOF liefert (Token expiry), reconnecten wir intern und
	// streamen weiter — Bose's Verbindung bleibt offen. Wir haben einen
	// generoesen Retry Budget aber bei Client Disconnect (context cancel)
	// hoeren wir sofort auf — sonst rauschen wir in einer Endlosschleife
	// gegen den CDN.
	start := time.Now()
	headersSent := false
	var lastErr error
	for attempt := 0; attempt < 60; attempt++ {
		// Wenn Bose die Verbindung beendet hat, sofort raus.
		if r.Context().Err() != nil {
			s.logger.Info("stream proxy end: client gone", "slot", slot, "elapsed", time.Since(start).Round(time.Second).String())
			return
		}
		if attempt > 0 {
			s.logger.Info("stream proxy reconnect", "slot", slot, "attempt", attempt, "lastErr", errStr(lastErr))
			// Kurz warten damit wir CDN nicht mit Reconnects ueberlasten
			select {
			case <-time.After(500 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		// Aktuelle URL holen — User koennte das Preset zwischenzeitlich
		// geaendert haben.
		cur, ok := s.store.Get(slot)
		if !ok || cur.StreamURL == "" {
			return
		}
		boseAlive, err := s.streamOne(r.Context(), w, r, cur.StreamURL, !headersSent)
		lastErr = err
		if !boseAlive {
			// Bose hat die Verbindung beendet (Standby, Sender Wechsel).
			// Normales Ende, klar getrennt vom Give-up-Fall unten, damit
			// im Log Box-Stop vs Outbound-Problem unterscheidbar ist.
			s.logger.Info("stream proxy end: bose disconnected", "slot", slot, "elapsed", time.Since(start).Round(time.Second).String(), "lastErr", errStr(err))
			if s.onDisconnect != nil {
				s.onDisconnect(err)
			}
			return
		}
		headersSent = true
	}
	// 60 Reconnects erschoepft: die Box wollte weiter Bytes, aber der
	// Upstream scheiterte wiederholt. Ein Netzwerkfehler in lastErr deutet
	// auf den Outbound-Pfad der Box (z.B. flakiges Kabel) statt auf die Box.
	s.logger.Warn("stream proxy gave up reconnecting", "slot", slot, "attempts", 60, "elapsed", time.Since(start).Round(time.Second).String(), "lastErr", errStr(lastErr))
}

// streamOne macht einen Roundtrip zum upstream + kopiert Body zu w.
// Returnt boseAlive=true wenn die Verbindung zu Bose noch offen ist
// (Reconnect sinnvoll), false wenn Bose disconnected hat. Der zweite
// Rueckgabewert ist der letzte Upstream-Fehler dieses Versuchs (nil bei
// sauberem EOF oder normalem Bose-Disconnect); der Aufrufer loggt ihn am
// Stream-Ende, damit Box-Stop von Outbound-Problemen unterscheidbar ist.
func (s *Server) streamOne(ctx context.Context, w http.ResponseWriter, r *http.Request, url string, sendHeaders bool) (bool, error) {
	if err := safeHTTPURL(url); err != nil {
		s.logger.Warn("stream proxy refusing url", "url", url, "err", err)
		if sendHeaders {
			http.Error(w, "invalid stream url", http.StatusBadRequest)
		}
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		s.logger.Warn("stream proxy NewRequest fail", "err", err)
		return false, err
	}
	// Icecast Header durchreichen damit Box Metadaten bekommt
	if md := r.Header.Get("Icy-MetaData"); md != "" {
		req.Header.Set("Icy-MetaData", md)
	}
	req.Header.Set("User-Agent", "STR-Proxy/1.0")

	resp, err := s.client.Do(req)
	if err != nil {
		// Wenn Bose die Verbindung beendet hat, kein Retry sinnvoll.
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return false, nil
		}
		// Dedupe identical failures: Bose's UPnP player re-hits the
		// proxy when a station is unreachable, so the same NXDOMAIN
		// would otherwise spam the agent log.
		if s.shouldLogFail(url) {
			s.logger.Warn("stream proxy upstream fail", "url", url, "err", err)
		} else {
			s.logger.Debug("stream proxy upstream fail (dedup)", "url", url, "err", err)
		}
		if sendHeaders {
			http.Error(w, "upstream unreachable", http.StatusBadGateway)
			return false, err
		}
		// Headers schon gesendet — Reconnect probieren
		return true, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		statusErr := fmt.Errorf("upstream status %d", resp.StatusCode)
		if s.shouldLogFail(url) {
			s.logger.Warn("stream proxy upstream status", "status", resp.StatusCode, "url", url)
		} else {
			s.logger.Debug("stream proxy upstream status (dedup)", "status", resp.StatusCode, "url", url)
		}
		if sendHeaders {
			http.Error(w, "upstream status: "+resp.Status, http.StatusBadGateway)
			return false, statusErr
		}
		return true, statusErr
	}

	// Successful reach — clear any dedup entry so a future failure
	// for this URL produces a fresh WARN immediately.
	s.failMu.Lock()
	delete(s.lastFail, url)
	s.failMu.Unlock()

	// Capture the real stream bitrate from the icy-br header, if the
	// station sends one (most Icecast/Shoutcast do). A single header read
	// outside the copy loop. beginStream also reuses a value already
	// measured for this URL, so stations without the header only fall back
	// to the throughput measurement below on the very first play.
	icyBr := icyBitrate(resp.Header)
	knownBitrate := s.beginStream(url, icyBr)

	if sendHeaders {
		for k, vv := range resp.Header {
			// Hop by hop Headers nicht weitergeben
			switch strings.ToLower(k) {
			case "connection", "transfer-encoding":
				continue
			}
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
	}

	// Flush kontinuierlich damit Bose's Player nicht auf Buffer wartet
	flusher, _ := w.(http.Flusher)
	// Throughput fallback for stations that send no icy-br and have not
	// been measured before. The box fills its decode buffer fast at the
	// start, so a measurement taken immediately reads far above the real
	// bitrate (e.g. 1300 for a 192k stream). We therefore skip the first
	// brSettle of buffer-fill, then average bytes/elapsed over brWindow of
	// steady-state playback (when bytes arrive at real-time = the true
	// bitrate), snap to the nearest standard rate, store it once, and stop.
	// Bounded to this single active stream: a few counters and one division.
	const (
		brSettle = 4 * time.Second
		brWindow = 6 * time.Second
	)
	streamStart := time.Now()
	var winBytes int64
	var winStart time.Time
	measured := knownBitrate
	buf := make([]byte, 16*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				// Bose hat Verbindung geschlossen
				return false, nil
			}
			if flusher != nil {
				flusher.Flush()
			}
			if !measured && time.Since(streamStart) >= brSettle {
				if winStart.IsZero() {
					// First read past the settle point: start the window
					// here, do not count this partial chunk.
					winStart = time.Now()
				} else {
					winBytes += int64(n)
					if el := time.Since(winStart); el >= brWindow {
						if secs := el.Seconds(); secs > 0 {
							raw := int(float64(winBytes) * 8 / 1000 / secs)
							if br := roundStandardBitrate(raw); br > 0 {
								s.rememberBitrate(url, br)
							}
						}
						measured = true
					}
				}
			}
		}
		if readErr != nil {
			// Bose hat zu — KEIN Retry, sonst Endlos Schleife
			if errors.Is(readErr, context.Canceled) || ctx.Err() != nil {
				return false, nil
			}
			if readErr == io.EOF {
				// CDN hat sauber EOF — wahrscheinlich Token expired
				return true, nil
			}
			// Network Fehler — wir versuchen reconnect
			s.logger.Info("stream proxy upstream read fail, reconnect", "err", readErr)
			return true, readErr
		}
	}
}

// errStr renders an error for a structured log field, empty when nil so
// a clean stream end does not log a misleading "lastErr=<nil>".
func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
