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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
		// Host-level SSRF protection runs at dial time (dialGuardSSRF) on
		// the resolved IP, which also defeats a hostname that resolves to a
		// blocked address. Here we only gate the scheme.
		return nil
	default:
		return fmt.Errorf("disallowed url scheme %q (only http/https accepted)", u.Scheme)
	}
}

// dialGuardSSRF runs as the net.Dialer Control hook, i.e. AFTER DNS
// resolution and BEFORE the TCP connect, on the concrete resolved address.
// It refuses connections to addresses that are never a legitimate public
// radio stream but that an attacker-controlled radio-browser stream URL
// could abuse to make the agent fetch its own privileged loopback services
// (the Bose firmware / STR webui on the box) or a cloud metadata endpoint
// (169.254.169.254). Because it inspects the resolved IP, a hostname that
// resolves to a blocked address (DNS-rebinding) is caught too.
//
// Private LAN ranges (192.168/10/172.16) are deliberately NOT blocked so a
// user's own local Icecast/DLNA stream on their network keeps working; the
// dangerous self-targeting case is loopback, which is blocked.
func dialGuardSSRF(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil // not an IP literal (should not happen post-resolution)
	}
	if ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("stream target %s blocked (loopback/link-local/metadata)", ip)
	}
	return nil
}

// isHLSorDASHURL reports whether a URL points at an HLS (.m3u8) or DASH (.mpd)
// playlist rather than a continuous raw stream. These are segment playlists,
// not endless byte streams, and Bose's player cannot consume them; the proxy
// would otherwise fetch the short playlist, hit EOF, and reconnect-loop forever
// (BBC Radio 4 and the other BBC HLS-only stations). Until the
// agent grows a real HLS/DASH remuxer, we detect these and report the stream as
// not playable instead of looping. The query string is ignored so a tokenised
// ".m3u8?..." still matches.
func isHLSorDASHURL(raw string) bool {
	return isHLSURL(raw) || isDASHURL(raw)
}

// isHLSURL reports whether a URL points at an HLS (.m3u8) playlist. STR follows
// these and demuxes their segments (see serveHLS), so they are now playable.
func isHLSURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Path), ".m3u8")
}

// isDASHURL reports whether a URL points at a DASH (.mpd) manifest. DASH is not
// supported yet and is still refused.
func isDASHURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Path), ".mpd")
}

// isHLSorDASHContentType catches HLS/DASH responses whose URL does not carry a
// telltale suffix, by their MIME type (application/vnd.apple.mpegurl,
// application/x-mpegURL, audio/mpegurl, application/dash+xml).
func isHLSorDASHContentType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "mpegurl") || strings.Contains(ct, "dash+xml")
}

// hlsNotPlayableMsg is the user-facing reason returned to the box for an HLS/
// DASH stream. Kept short; the box shows a not-playable state rather than a
// silent reconnect storm.
const hlsNotPlayableMsg = "HLS/DASH streams are not supported yet"

type Server struct {
	store  *presets.Store
	logger *slog.Logger
	client *http.Client

	failMu   sync.Mutex
	lastFail map[string]time.Time

	// errMu guards lastErr, the most recent terminal upstream failure for the
	// stream the box is (or just was) pulling. The desktop app polls
	// /api/stream-status right after starting a station so it can show a clear
	// reason ("this stream is blocked / unavailable") and automatically try
	// another radio-browser entry of the same station. Radio failures are
	// asynchronous: the box accepts the UPnP URL instantly, then the 403/503
	// only surfaces here when it pulls the bytes, so a pollable record is the
	// only way the app learns why nothing plays.
	errMu   sync.Mutex
	lastErr streamFailure

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

	// titleMu guards the live ICY StreamTitle of the stream being proxied.
	// We always request ICY metadata from the upstream, de-interleave it out
	// of the byte stream the box receives (so the box gets clean audio), and
	// surface the parsed StreamTitle here. curTitleURL pins the title to its
	// stream so a station switch clears a stale title instead of showing the
	// previous station's track.
	titleMu     sync.Mutex
	curTitle    string
	curTitleURL string
	// onTitle, if set, is called whenever the live StreamTitle changes to a
	// non-empty value. Used to push the radio track text to the box display.
	onTitle func(title string)
}

// SetOnTitle registers a callback invoked when the live ICY StreamTitle of
// the proxied stream changes to a non-empty value. Set once at wiring time.
func (s *Server) SetOnTitle(fn func(title string)) { s.onTitle = fn }

// CurrentTitle returns the live ICY StreamTitle of the stream being proxied
// right now, or "" if the station sends no metadata or none has arrived yet.
func (s *Server) CurrentTitle() string {
	s.titleMu.Lock()
	defer s.titleMu.Unlock()
	return s.curTitle
}

// setTitle records a freshly parsed StreamTitle for url and fires onTitle when
// it changed to a new non-empty value. Empty titles (StreamTitle='') clear the
// current title but never fire the push, so a station that briefly sends an
// empty title does not blank the box display with a spurious update.
func (s *Server) setTitle(url, title string) {
	title = strings.TrimRight(title, "\x00")
	title = strings.TrimSpace(title)
	s.titleMu.Lock()
	changed := title != s.curTitle || url != s.curTitleURL
	s.curTitle = title
	s.curTitleURL = url
	fire := changed && title != "" && s.onTitle != nil
	cb := s.onTitle
	s.titleMu.Unlock()
	if changed {
		s.logger.Info("stream proxy ICY title", "title", title)
	}
	if fire {
		cb(title)
	}
}

// clearTitleForNewURL drops a stale title when the proxied stream changes, so
// the brief window before the new station's first metadata block does not show
// the old station's track. A reconnect to the same url keeps the title.
func (s *Server) clearTitleForNewURL(url string) {
	s.titleMu.Lock()
	if url != s.curTitleURL {
		s.curTitle = ""
		s.curTitleURL = url
	}
	s.titleMu.Unlock()
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
		// Der DialContext-Guard blockt SSRF-Ziele (loopback/link-local/
		// metadata) nach der DNS-Aufloesung — eine boesartige
		// radio-browser-URL kann die Box so nicht auf ihre eigenen
		// Loopback-Dienste oder Cloud-Metadata zeigen lassen.
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Control: dialGuardSSRF}).DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
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

// icyMetaint returns the byte spacing between interleaved ICY metadata blocks
// from the upstream icy-metaint response header, or 0 if the station sends no
// metadata. With a non-zero value the stream is: metaint audio bytes, then one
// length byte L, then L*16 bytes of metadata, repeating.
func icyMetaint(h http.Header) int {
	v := h.Get("icy-metaint")
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		return n
	}
	return 0
}

// parseStreamTitle pulls the track text out of an ICY metadata block, which
// looks like `StreamTitle='Artist - Song';StreamUrl='...';` padded to a 16-byte
// boundary with NULs. Returns ok=false when there is no StreamTitle field.
func parseStreamTitle(meta string) (string, bool) {
	const key = "StreamTitle='"
	i := strings.Index(meta, key)
	if i < 0 {
		return "", false
	}
	rest := meta[i+len(key):]
	// Closing delimiter is `';`; fall back to a lone quote if the station omits
	// the semicolon, and to the whole remainder (NUL-trimmed) as a last resort.
	if j := strings.Index(rest, "';"); j >= 0 {
		return rest[:j], true
	}
	if j := strings.IndexByte(rest, '\''); j >= 0 {
		return rest[:j], true
	}
	return strings.TrimRight(rest, "\x00"), true
}

// icyReader wraps an upstream stream that carries interleaved ICY metadata and
// presents only the audio bytes to the caller. Each metadata block is handed
// to onMeta as it is read, so the proxy can extract StreamTitle without ever
// forwarding the metadata (or the icy-metaint contract) to the box.
type icyReader struct {
	src     io.Reader
	metaint int
	remain  int // audio bytes left before the next metadata block
	onMeta  func(meta string)
}

func newICYReader(src io.Reader, metaint int, onMeta func(meta string)) *icyReader {
	return &icyReader{src: src, metaint: metaint, remain: metaint, onMeta: onMeta}
}

func (r *icyReader) Read(p []byte) (int, error) {
	// At a metadata boundary: read the length byte and, if non-zero, the block.
	if r.remain == 0 {
		var lb [1]byte
		if _, err := io.ReadFull(r.src, lb[:]); err != nil {
			return 0, err
		}
		if mlen := int(lb[0]) * 16; mlen > 0 {
			meta := make([]byte, mlen)
			if _, err := io.ReadFull(r.src, meta); err != nil {
				return 0, err
			}
			if r.onMeta != nil {
				r.onMeta(string(meta))
			}
		}
		r.remain = r.metaint
	}
	// Read at most up to the next metadata boundary so the length byte is never
	// mistaken for audio.
	n := len(p)
	if n > r.remain {
		n = r.remain
	}
	read, err := r.src.Read(p[:n])
	r.remain -= read
	return read, err
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

// upstreamStatusError carries the upstream HTTP status of a non-200 response so
// the reconnect loops can tell a permanent client-side rejection (403 geo-block,
// 404/410 gone) from a transient one (5xx) via errors.As, instead of parsing a
// formatted string.
type upstreamStatusError struct {
	Code   int
	Status string
}

func (e *upstreamStatusError) Error() string {
	if e.Status != "" {
		return "upstream status " + e.Status
	}
	return fmt.Sprintf("upstream status %d", e.Code)
}

// isPermanentUpstream reports whether err is an upstream failure that retrying
// the SAME URL cannot fix: a client-side HTTP rejection (forbidden, not found,
// gone, unavailable-for-legal-reasons) or an HLS/DASH playlist we cannot play.
// For these the reconnect loop gives up immediately so the desktop app can fall
// back to another radio-browser entry of the station within a second instead of
// after a 30s retry storm against a URL that will never serve audio.
func isPermanentUpstream(err error) bool {
	var se *upstreamStatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
			http.StatusNotFound, http.StatusGone, http.StatusUnavailableForLegalReasons:
			return true
		}
	}
	return false
}

// streamFailure is the most recent terminal upstream error, surfaced via
// /api/stream-status. url is the upstream stream URL (not the proxy wrapper) so
// the app can confirm the error belongs to the station it just started.
type streamFailure struct {
	when   time.Time
	code   int    // upstream HTTP status, 0 for a network-level failure
	reason string // coarse class the UI maps to a message: blocked|gone|unavailable|unreachable|hls
	url    string
}

// classifyFailure maps an upstream error to a coarse reason the desktop app
// turns into a localized, human message. Network errors (DNS, refused, reset)
// land in "unreachable"; HTTP statuses split into blocked/gone/unavailable.
func classifyFailure(err error) (code int, reason string) {
	var se *upstreamStatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusUnavailableForLegalReasons:
			return se.Code, "blocked"
		case http.StatusNotFound, http.StatusGone:
			return se.Code, "gone"
		default:
			// Everything else (5xx, and any other non-200) is a transient
			// "currently unavailable" from the station's side.
			return se.Code, "unavailable"
		}
	}
	if strings.Contains(strings.ToLower(errStr(err)), "hls") || strings.Contains(strings.ToLower(errStr(err)), "dash") {
		return 0, "hls"
	}
	return 0, "unreachable"
}

// recordFailure stores the latest terminal upstream failure for url so the
// desktop app can poll it and react (message + alternative-source fallback).
func (s *Server) recordFailure(url string, err error) {
	code, reason := classifyFailure(err)
	s.errMu.Lock()
	s.lastErr = streamFailure{when: time.Now(), code: code, reason: reason, url: url}
	s.errMu.Unlock()
}

// clearFailure drops any recorded failure for url once it streams successfully,
// so a recovered station does not keep reporting a stale error to the app.
func (s *Server) clearFailure(url string) {
	s.errMu.Lock()
	if s.lastErr.url == url {
		s.lastErr = streamFailure{}
	}
	s.errMu.Unlock()
}

// Handler registriert /stream/<slot> sowie /stream/raw fuer ad-hoc URLs
// (z.B. aus der Radio Suche) auf den uebergebenen Mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/stream/raw", s.handleRaw)
	mux.HandleFunc("/stream/", s.handle)
	mux.HandleFunc("/api/stream/bitrate", s.handleBitrate)
	mux.HandleFunc("/api/stream/title", s.handleTitle)
	mux.HandleFunc("/api/stream-status", s.handleStreamStatus)
}

// handleStreamStatus reports the most recent terminal upstream failure as JSON:
//
//	{"error":true,"status":403,"reason":"blocked","url":"http://...","ageMs":1200}
//
// or {"error":false} when the last start streamed fine or is too old to matter.
// The desktop app polls this for a few seconds after starting a station: a fresh
// error tells it which message to show and that it should try another
// radio-browser entry of the same station. Only failures younger than
// streamStatusTTL are reported so a long-past error never blocks a fresh play.
func (s *Server) handleStreamStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	s.errMu.Lock()
	f := s.lastErr
	s.errMu.Unlock()
	if f.when.IsZero() || time.Since(f.when) > streamStatusTTL {
		fmt.Fprint(w, `{"error":false}`)
		return
	}
	body, err := json.Marshal(struct {
		Error  bool   `json:"error"`
		Status int    `json:"status"`
		Reason string `json:"reason"`
		URL    string `json:"url"`
		AgeMs  int64  `json:"ageMs"`
	}{
		Error:  true,
		Status: f.code,
		Reason: f.reason,
		URL:    f.url,
		AgeMs:  time.Since(f.when).Milliseconds(),
	})
	if err != nil {
		fmt.Fprint(w, `{"error":false}`)
		return
	}
	w.Write(body)
}

// streamStatusTTL bounds how long a recorded upstream failure is reported. Long
// enough for the app's post-play poll window, short enough that a stale error
// from minutes ago never suppresses a fresh, healthy station.
const streamStatusTTL = 20 * time.Second

// handleTitle returns the live ICY StreamTitle of the stream currently
// proxied, or "" when the station sends no metadata. Cheap: a single guarded
// string read. The desktop app polls this on a slow cadence to show the live
// radio track next to the station name, the same way it shows the bitrate.
func (s *Server) handleTitle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	writeJSONString(w, "title", s.CurrentTitle())
}

// writeJSONString emits {"<key>":"<value>"} with value JSON-escaped, so a
// StreamTitle containing quotes or backslashes cannot break the response.
func writeJSONString(w io.Writer, key, value string) {
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	fmt.Fprintf(w, `{%q:"%s"}`, key, esc.Replace(value))
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
	if isDASHURL(url) {
		s.logger.Warn("stream proxy: DASH not supported yet, refusing instead of reconnect-looping", "url", url)
		s.recordFailure(url, fmt.Errorf("dash not supported"))
		http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
		return
	}
	if isHLSURL(url) {
		// HLS: follow the playlist and demux its segments into a continuous stream
		// for the box (#124). serveHLS only returns an error before it has written
		// any audio, so http.Error here is always valid (never mid-stream).
		if err := s.serveHLS(r.Context(), w, r, url); err != nil {
			s.logger.Warn("stream proxy: HLS playback failed", "url", url, "err", err)
			s.recordFailure(url, err)
			http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
		}
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
		if isPermanentUpstream(lastErr) {
			// A client-side rejection (403 geo-block, 404/410 gone) will not
			// change on retry. Stop now so the desktop app can fall back to
			// another radio-browser entry of the station instead of waiting out
			// a 30s retry storm against a URL that will never serve audio.
			s.logger.Info("stream proxy end: permanent upstream rejection, not retrying", "kind", "raw", "lastErr", errStr(lastErr))
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

	if isDASHURL(p.StreamURL) {
		s.logger.Warn("stream proxy: DASH preset not supported yet, refusing", "slot", slot, "url", p.StreamURL)
		s.recordFailure(p.StreamURL, fmt.Errorf("dash not supported"))
		http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
		return
	}
	if isHLSURL(p.StreamURL) {
		// HLS preset: follow the playlist and demux to the box (#124).
		if err := s.serveHLS(r.Context(), w, r, p.StreamURL); err != nil {
			s.logger.Warn("stream proxy: HLS preset playback failed", "slot", slot, "url", p.StreamURL, "err", err)
			s.recordFailure(p.StreamURL, err)
			http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
		}
		return
	}

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
		if isPermanentUpstream(lastErr) {
			s.logger.Info("stream proxy end: permanent upstream rejection, not retrying", "slot", slot, "lastErr", errStr(lastErr))
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
	// Always request ICY metadata from the upstream, regardless of what the
	// box asked for. STR owns the metadata: it de-interleaves it out of the
	// stream (so the box gets clean audio) and reads StreamTitle to drive the
	// now-playing text. The box never sees the icy-metaint contract.
	req.Header.Set("Icy-MetaData", "1")
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
		s.recordFailure(url, err)
		if sendHeaders {
			http.Error(w, "upstream unreachable", http.StatusBadGateway)
			return false, err
		}
		// Headers schon gesendet — Reconnect probieren
		return true, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		statusErr := &upstreamStatusError{Code: resp.StatusCode, Status: resp.Status}
		if s.shouldLogFail(url) {
			s.logger.Warn("stream proxy upstream status", "status", resp.StatusCode, "url", url)
		} else {
			s.logger.Debug("stream proxy upstream status (dedup)", "status", resp.StatusCode, "url", url)
		}
		s.recordFailure(url, statusErr)
		if sendHeaders {
			http.Error(w, "upstream status: "+resp.Status, http.StatusBadGateway)
			return false, statusErr
		}
		return true, statusErr
	}

	// HLS/DASH detected by MIME type (a URL without the telltale suffix). These
	// are segment playlists, not raw streams, so reading the body as audio yields
	// instant EOF and a reconnect storm. Report not-playable and stop instead.
	if ct := resp.Header.Get("Content-Type"); isHLSorDASHContentType(ct) {
		if s.shouldLogFail(url) {
			s.logger.Warn("stream proxy: HLS/DASH content-type not supported yet", "url", url, "contentType", ct)
		}
		hlsErr := fmt.Errorf("hls/dash not supported (content-type %q)", ct)
		s.recordFailure(url, hlsErr)
		if sendHeaders {
			http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
		}
		return false, hlsErr
	}

	// Successful reach — clear any dedup entry so a future failure
	// for this URL produces a fresh WARN immediately, and drop any recorded
	// stream-status failure so the app stops reporting a now-recovered station.
	s.failMu.Lock()
	delete(s.lastFail, url)
	s.failMu.Unlock()
	s.clearFailure(url)

	// Capture the real stream bitrate from the icy-br header, if the
	// station sends one (most Icecast/Shoutcast do). A single header read
	// outside the copy loop. beginStream also reuses a value already
	// measured for this URL, so stations without the header only fall back
	// to the throughput measurement below on the very first play.
	icyBr := icyBitrate(resp.Header)
	knownBitrate := s.beginStream(url, icyBr)

	// ICY metadata spacing. When set, the upstream interleaves StreamTitle
	// blocks every metaint bytes.
	metaint := icyMetaint(resp.Header)
	s.clearTitleForNewURL(url)

	// Did the box itself ask for ICY metadata? If so it can de-interleave and
	// display StreamTitle natively, with no stream re-fetch (the gap-free path,
	// unlike re-issuing SetAVTransportURI which makes the box drop+reconnect).
	// In that case pass the interleaved bytes AND the icy-metaint header
	// through unchanged, and tee a parse so STR's /api/stream/title still
	// updates too. If the box did NOT ask, strip the metadata so it gets clean
	// audio (it would otherwise mistake metadata bytes for audio).
	boxICY := r.Header.Get("Icy-MetaData")
	boxWantsICY := boxICY != "" && boxICY != "0"
	s.logger.Info("stream proxy ICY negotiation", "boxWantsICY", boxWantsICY, "boxIcyMetaData", boxICY, "upstreamMetaint", metaint)

	if sendHeaders {
		for k, vv := range resp.Header {
			// Hop by hop Headers nicht weitergeben
			switch strings.ToLower(k) {
			case "connection", "transfer-encoding":
				continue
			// icy-metaint only reaches the box when the box asked for ICY and
			// will de-interleave it. When we strip (box did not ask), the
			// header must not leak or the box would treat metadata bytes as
			// audio and get corrupted sound.
			case "icy-metaint":
				if !boxWantsICY {
					continue
				}
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
	// De-interleave ICY metadata ONLY when the box did not ask for it: src
	// then yields clean audio and each StreamTitle block updates STR's live
	// title. When the box DID ask (boxWantsICY), pass the interleaved stream
	// through untouched so the box de-interleaves and displays the track
	// itself, gap-free. Without metaint the body passes through unchanged.
	var src io.Reader = resp.Body
	if metaint > 0 && !boxWantsICY {
		src = newICYReader(resp.Body, metaint, func(meta string) {
			if title, ok := parseStreamTitle(meta); ok {
				s.setTitle(url, title)
			}
		})
	}
	buf := make([]byte, 16*1024)
	for {
		n, readErr := src.Read(buf)
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
