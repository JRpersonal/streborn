// Package streamproxy wraps third-party radio streams in a stable URL that
// Bose's UPnP player can no longer let go of.
//
// Background: many modern radio stations (1LIVE, SWR3, Rock Antenne via
// streamonkey) answer with an HTTP 302 redirect to a CDN URL carrying a
// signed token. Bose's UPnP player does follow the redirect, but holds on
// to the per-token URL. When the token expires after a few hours, the CDN
// kills the connection — Bose registers that as "stream dead" and goes into
// INVALID_SOURCE. The user's impression: "the station stops playing after a
// while".
//
// With this proxy Bose always sees the same URL
// `http://127.0.0.1:8888/stream/<slot>`. The stick agent internally resolves
// the redirect to the CDN and streams the bytes through. When the CDN kills
// the connection (token expiry), the proxy reconnects IMMEDIATELY — Bose's
// TCP connection stays open, so Bose never notices a drop.
package streamproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/netutil"
	"github.com/JRpersonal/streborn/internal/presets"
)

// failLogSuppressWindow is the minimum spacing between identical
// "upstream fail" warnings for the same URL. Bose's UPnP player
// re-hits the proxy when a station is unreachable, so without this
// the agent log fills with the same NXDOMAIN line several times a
// minute for a single dead preset.
const failLogSuppressWindow = 30 * time.Second

// safeHTTPURL and the dial guard live in internal/netutil so the upnp playlist
// fetcher shares the exact same SSRF policy. streamOne is reachable via handle()
// with a URL straight from the preset store (CodeQL flagged that outbound Do()),
// so the scheme gate stays mandatory here too.
func safeHTTPURL(raw string) error { return netutil.SafeHTTPURL(raw) }

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

// unwrapSelfProxy unwinds a stored stream URL that points back at the agent's own
// stream proxy, e.g. "http://127.0.0.1:8888/stream/raw?u=<base64 real URL>". This
// happens when a preset is saved from the box's now-playing location while a
// radio station is playing THROUGH the proxy: the saved URL is the proxy wrapper,
// not the real upstream (regression since v0.7.16, where ad-hoc radio routes via
// /stream/raw). Recalling such a preset makes the proxy fetch its own loopback
// URL, which the SSRF dial guard blocks, so the box gets nothing (INVALID_SOURCE /
// AUDIO_ERROR_BAD_URL). Decoding the inner u recovers the real URL and heals the
// preset in place, with no re-save needed. Loops in case of multiple wraps;
// returns raw unchanged when it is not a self-proxy URL.
func unwrapSelfProxy(raw string) string {
	for i := 0; i < 5; i++ {
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Path, "/stream/raw") {
			return raw
		}
		enc := u.Query().Get("u")
		if enc == "" {
			return raw
		}
		dec, err := base64.RawURLEncoding.DecodeString(enc)
		if err != nil {
			if d2, e2 := base64.StdEncoding.DecodeString(enc); e2 == nil {
				dec = d2
			} else {
				return raw
			}
		}
		inner := string(dec)
		if !strings.HasPrefix(inner, "http://") && !strings.HasPrefix(inner, "https://") {
			return raw
		}
		raw = inner
	}
	return raw
}

// selfProxySlotRe matches the agent's own per-slot proxy path (/stream/1..6).
var selfProxySlotRe = regexp.MustCompile(`^/stream/([1-6])$`)

// selfProxySlotRef reports whether raw points at this agent's own
// /stream/<slot> proxy (the box-visible preset location, never a valid
// station origin) and which slot it references. Mirrors webui's save gate.
func selfProxySlotRef(raw string) (int, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, false
	}
	m := selfProxySlotRe.FindStringSubmatch(u.Path)
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

// resolvePresetURL heals a stored preset URL before serving. Two poisoned
// forms exist in the field (#252): the /stream/raw?u=<base64> self-wrap
// (decoded by unwrapSelfProxy) and the bare /stream/<n> slot form an older
// same-slot save left behind. For the slot form, n != slot dereferences the
// referenced slot's stored origin (loop-guarded); n == slot is unrecoverable -
// the origin URL is gone - and logs a distinct WARN naming the remedy instead
// of the generic SSRF dial error the box otherwise trips.
func (s *Server) resolvePresetURL(slot int, raw string) string {
	raw = unwrapSelfProxy(raw)
	seen := map[int]bool{slot: true}
	for i := 0; i < 6; i++ {
		ref, self := selfProxySlotRef(raw)
		if !self {
			return raw
		}
		if seen[ref] {
			s.logger.Warn("stream proxy: preset stores its own proxy URL, origin lost - re-save the station in the app (#252)",
				"slot", slot, "ref", ref)
			return raw
		}
		seen[ref] = true
		p, ok := s.store.Get(ref)
		if !ok || p.StreamURL == "" {
			s.logger.Warn("stream proxy: preset references another slot's proxy URL but that slot is empty - re-save the station in the app (#252)",
				"slot", slot, "ref", ref)
			return raw
		}
		raw = unwrapSelfProxy(p.StreamURL)
	}
	return raw
}

// isHLSorDASHContentType catches HLS/DASH responses whose URL does not carry a
// telltale suffix, by their MIME type (application/vnd.apple.mpegurl,
// application/x-mpegURL, audio/mpegurl, application/dash+xml).
func isHLSorDASHContentType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "mpegurl") || strings.Contains(ct, "dash+xml")
}

// errPlaylistIsHLS signals that a response STR started to stream as a raw byte
// stream turned out to be an HLS playlist (recognised by its body, not a .m3u8
// URL suffix). The caller re-runs the request through serveHLS, which demuxes
// the segments into one continuous stream. Returned only before any audio (or
// response headers) have been written, so switching paths is safe.
var errPlaylistIsHLS = errors.New("streamproxy: response is an HLS playlist")

// hasHTTPScheme reports whether s begins with an http(s) scheme.
func hasHTTPScheme(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// firstMediaURLFromPlaylist extracts the first playable stream URL from a plain
// M3U or PLS pointer playlist (a short text file that just lists the real stream
// URL, as Icecast/Shoutcast directory stations and the Absolut* stations serve
// under an audio/x-mpegurl MIME even when the URL has no .m3u suffix, #252).
// M3U: the first non-comment line that is a URL. PLS: the first FileN=URL entry.
// A relative M3U entry is resolved against baseURL. Returns "" when the body
// carries no stream URL (e.g. it is actually an HLS segment playlist, which the
// caller detects separately via the #EXT-X- markers).
func firstMediaURLFromPlaylist(body, baseURL string) string {
	base, _ := url.Parse(baseURL)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			// blank line or M3U comment/directive (#EXTM3U, #EXTINF, #EXT-X-*).
			continue
		}
		lower := strings.ToLower(line)
		// PLS entry: FileN=URL (key is case-insensitive, N is a number).
		if eq := strings.IndexByte(line, '='); eq > 0 && strings.HasPrefix(lower, "file") {
			if cand := strings.TrimSpace(line[eq+1:]); hasHTTPScheme(cand) {
				return cand
			}
			continue
		}
		// Absolute M3U entry.
		if hasHTTPScheme(line) {
			return line
		}
		// PLS metadata / section headers ([playlist], NumberOfEntries=1,
		// Title1=..., Version=2) are not stream URLs.
		if strings.ContainsAny(line, "=[]") {
			continue
		}
		// A relative URI in an M3U — resolve it against the playlist URL.
		if base != nil {
			if ref, err := url.Parse(line); err == nil {
				return base.ResolveReference(ref).String()
			}
		}
	}
	return ""
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

	// fetchMu guards lastFetch, the last time the BOX opened any proxied
	// stream (slot or raw). The wedge detector uses it to tell "the box never
	// even pulled the URL it accepted" (control stack wedged, needs a
	// power-cycle) apart from "the box pulled it and the station failed".
	// slotFetch records the same moment PER preset slot, stamped only after
	// the slot validated and the store served a preset. The hardware recall
	// verify keys its success signal off the slot it pushed: the global stamp
	// let ANY proxied fetch (another slot's reconnect, a zone follower, even a
	// 404) certify a failed recall as healthy at the first tick, so no retry
	// ran and wedge strikes were falsely cleared (#252: station on the
	// display, no audio, clean log).
	fetchMu   sync.Mutex
	lastFetch time.Time
	slotFetch [7]time.Time // index 1..6: when the box last OPENED the slot
	// slotFetchEnd / slotOpen make the per-slot signal liveness-aware: a
	// 36ms-2.4s fetch that dies in the box's re-login source bounce used to
	// satisfy "opened since the press" and certified a dead recall as healthy
	// (field bundles 2026-07-22). A recall counts as pulled only while a
	// connection is OPEN or after it served a sustained stretch.
	slotFetchEnd [7]time.Time
	slotOpen     [7]int

	// boxStateFn reports a speaker-side condition that makes every station
	// fail ("wedged", "login-error"; "" = fine). Wired to webui.BoxStateHint;
	// surfaced in /api/stream-status so the app can distinguish a box problem
	// from a station problem. nil-safe.
	boxStateFn func() string

	// netMu guards a briefly-cached verdict on whether the SPEAKER itself can
	// reach the public internet. It lets /api/stream-status tell "this one
	// station is unreachable" apart from "the speaker has no internet at all"
	// (a box that landed on a dead Wi-Fi fails EVERY station this way, #375).
	netMu      sync.Mutex
	netCheckAt time.Time
	netOnline  bool

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

	// gapMu guards lastByteToBox, the wall-clock time the proxy last handed
	// audio bytes to the box on the current stream. The reconnect loops read it
	// at the top of each retry to log how long the box went without audio: a gap
	// over ~1s is the audible dropout users report (#185).
	gapMu         sync.Mutex
	lastByteToBox time.Time
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
// it changed to a new non-empty value. Empty titles (StreamTitle=”) clear the
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
	// The SSRF guard (loopback/link-local/metadata blocked after DNS
	// resolution) is applied to every dial, plain or TLS, so a malicious
	// radio-browser URL cannot point the box at its own loopback services or
	// cloud metadata.
	baseDialer := &net.Dialer{Control: netutil.DialGuardSSRF}
	// DialTLSContext handles HTTPS itself so the clock-tolerant verification has
	// the real dial host (including a bare-IP host, for which the client sends
	// no SNI and tls.ConnectionState.ServerName would be empty). It reuses the
	// SSRF-guarded dialer, then does the handshake with a per-connection config
	// carrying that host. TLSClientConfig/TLSHandshakeTimeout are ignored once
	// DialTLSContext is set, so the handshake deadline is applied here.
	dialTLS := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		raw, err := baseDialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		conn := tls.Client(raw, clockTolerantTLSConfig(host))
		if err := conn.HandshakeContext(hctx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		return conn, nil
	}
	return &Server{
		store:  store,
		logger: logger,
		// Our own client so we control redirect behaviour. The default is
		// follow up to 10 — fine for Streamonkey & co. No timeout: streams
		// are endless, we read until EOF.
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           baseDialer.DialContext,
				DialTLSContext:        dialTLS,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
		lastFail:   make(map[string]time.Time),
		measuredBr: make(map[string]int),
	}
}

// minPlausibleClock is a lower bound on a trustworthy wall clock. STR shipped in
// 2026, so a box reporting a time before this has an unset clock. SoundTouch
// speakers have no battery-backed RTC: after a cold boot, before NTP has synced
// (or when NTP is blocked), the clock lands in the firmware's build epoch, often
// mid-2015. See #296 and docs/FIRMWARE-NOTES.md.
var minPlausibleClock = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// clockUntrustworthy reports whether the local wall clock is implausibly old,
// which would make certificate time-validity checks fail spuriously. It reads
// time.Now() live, so strict verification resumes automatically the moment NTP
// corrects the clock.
func clockUntrustworthy() bool { return time.Now().Before(minPlausibleClock) }

// clockTolerantTLSConfig builds the per-connection TLS config the proxy uses for
// an upstream HTTPS radio stream dialed at host (a hostname, or a bare-IP
// literal). It always verifies the certificate chain to the system roots and the
// host, but tolerates a wrong box clock: when the local clock is implausibly
// old, a chain that is valid except for the certificate's time window is still
// accepted. Without this, a speaker whose clock reset to 2015 rejected every
// HTTPS station as "certificate is not yet valid" (#296: Virgin Radio and other
// HTTPS streams would not play, while plain-HTTP BBC streams still did).
//
// Only the expiry/not-yet-valid window is relaxed, and only while the clock is
// untrustworthy; chain-to-root and host are always enforced, so this does not
// weaken MITM protection. host is passed explicitly (not read from
// ConnectionState.ServerName) so a bare-IP upstream, which sends no SNI, is
// still verified against the certificate's IP SANs.
func clockTolerantTLSConfig(host string) *tls.Config {
	return &tls.Config{
		ServerName: host, // SNI for a hostname; ignored on the wire for an IP literal
		// h2 stays available (DialTLSContext otherwise defaults to HTTP/1.1 only).
		NextProtos: []string{"h2", "http/1.1"},
		// The default verifier is disabled so the time window can be relaxed in
		// VerifyConnection; chain and host are still checked there manually.
		InsecureSkipVerify: true, //nolint:gosec // VerifyConnection re-implements chain+host verification below.
		VerifyConnection: func(cs tls.ConnectionState) error {
			roots, _ := x509.SystemCertPool()
			return verifyChainClockTolerant(cs.PeerCertificates, host, roots, time.Now(), clockUntrustworthy())
		},
	}
}

// verifyChainClockTolerant verifies leaf (certs[0]) against roots and host at
// time now. host is matched against the certificate's DNS names, or, for an IP
// literal, its IP SANs. If verification fails solely because the certificate is
// outside its time window and clockBad is true, it retries with the time check
// pinned to the leaf's NotBefore (so the window always passes) while still
// requiring a valid chain and host. It is split out from clockTolerantTLSConfig
// so the policy can be unit-tested without a live TLS handshake.
func verifyChainClockTolerant(certs []*x509.Certificate, host string, roots *x509.CertPool, now time.Time, clockBad bool) error {
	if len(certs) == 0 {
		return errors.New("tls: no peer certificates")
	}
	// Require a host: x509.Verify silently skips host checking when DNSName is
	// empty, so an empty host here would drop the host guard entirely. The
	// caller derives it from the dial address, so an empty value means something
	// is wrong and we must reject rather than relax.
	if host == "" {
		return errors.New("tls: empty host")
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	leaf := certs[0]
	opts := x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: inter, CurrentTime: now}
	if _, err := leaf.Verify(opts); err == nil {
		return nil
	} else {
		// Relax the time window only when the clock is untrustworthy and the
		// failure was a time-validity problem (x509.Expired covers both
		// not-yet-valid and expired). Any other failure (unknown authority,
		// hostname mismatch) still rejects the connection.
		var invalid x509.CertificateInvalidError
		if clockBad && errors.As(err, &invalid) && invalid.Reason == x509.Expired {
			opts.CurrentTime = leaf.NotBefore
			if _, err2 := leaf.Verify(opts); err2 == nil {
				return nil
			}
		}
		return err
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

// markByteDelivery stamps the moment audio last reached the box. Called from the
// streamOne copy loop on every successful write; cheap (one guarded time.Now),
// never logged per call.
func (s *Server) markByteDelivery() {
	s.gapMu.Lock()
	s.lastByteToBox = time.Now()
	s.gapMu.Unlock()
}

// resetAudioGap clears the last-byte stamp at the start of a fresh stream so a
// stale gap from a previous station is not reported on the first reconnect.
func (s *Server) resetAudioGap() {
	s.gapMu.Lock()
	s.lastByteToBox = time.Time{}
	s.gapMu.Unlock()
}

// audioGap returns how long it has been since audio last reached the box, or 0
// if no byte has been delivered yet (so the very first connect attempt is not
// reported as a gap).
func (s *Server) audioGap() time.Duration {
	s.gapMu.Lock()
	defer s.gapMu.Unlock()
	if s.lastByteToBox.IsZero() {
		return 0
	}
	return time.Since(s.lastByteToBox)
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

// boxHasOutbound reports whether the speaker itself can reach the public
// internet, cached for a few seconds. It lets handleStreamStatus tell a single
// unreachable station apart from the speaker being offline entirely (#375): a
// box on a dead Wi-Fi fails EVERY station with a network-level error, which
// otherwise reads as each station being individually broken. Dials a couple of
// well-known hosts by NAME on :443 (so DNS, which radio also needs, is part of
// the test) plus a raw resolver IP; any success means online.
func (s *Server) boxHasOutbound() bool {
	s.netMu.Lock()
	if !s.netCheckAt.IsZero() && time.Since(s.netCheckAt) < 8*time.Second {
		v := s.netOnline
		s.netMu.Unlock()
		return v
	}
	s.netMu.Unlock()
	online := false
	for _, h := range []string{"www.google.com:443", "www.cloudflare.com:443", "1.1.1.1:53"} {
		c, err := net.DialTimeout("tcp", h, 2*time.Second)
		if err == nil {
			_ = c.Close()
			online = true
			break
		}
	}
	s.netMu.Lock()
	s.netOnline = online
	s.netCheckAt = time.Now()
	s.netMu.Unlock()
	return online
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

// Register registers /stream/<slot> as well as /stream/raw for ad-hoc URLs
// (e.g. from the radio search) on the supplied mux.
// noteFetch records that the box opened a proxied stream just now.
func (s *Server) noteFetch() {
	s.fetchMu.Lock()
	s.lastFetch = time.Now()
	s.fetchMu.Unlock()
}

// noteSlotFetch records that the box opened THIS slot's proxied stream. Called
// only after the slot validated and the store had a playable preset, so a 404
// or a foreign fetch can never stamp it. Paired with noteSlotFetchDone.
func (s *Server) noteSlotFetch(slot int) {
	if slot < 1 || slot > 6 {
		return
	}
	s.fetchMu.Lock()
	s.slotFetch[slot] = time.Now()
	s.slotOpen[slot]++
	s.fetchMu.Unlock()
}

// noteSlotFetchDone records that a slot connection closed, for the liveness
// half of SlotPulledSince.
func (s *Server) noteSlotFetchDone(slot int) {
	if slot < 1 || slot > 6 {
		return
	}
	s.fetchMu.Lock()
	if s.slotOpen[slot] > 0 {
		s.slotOpen[slot]--
	}
	s.slotFetchEnd[slot] = time.Now()
	s.fetchMu.Unlock()
}

// LastFetchForSlot reports when the box last opened the given slot's proxied
// stream (zero time = never, or slot out of range). The global LastActivity
// stamp stays for the wedge detector, which deliberately counts any fetch.
func (s *Server) LastFetchForSlot(slot int) time.Time {
	if slot < 1 || slot > 6 {
		return time.Time{}
	}
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()
	return s.slotFetch[slot]
}

// minSustainedFetch is how long a now-closed slot fetch must have served to
// still count as "the box played this recall". The box's re-login source
// bounce opens the stream for 36ms-2.4s and drops it; a genuine playback
// session either stays open or lasted well past this.
const minSustainedFetch = 3 * time.Second

// SlotPulledSince reports whether the box is credibly playing this slot's
// proxied stream for a recall anchored at t: a connection opened after t that
// is still OPEN, or one that served at least minSustainedFetch before closing.
// This is the hardware recall verify's success signal; "opened once since t"
// alone certified dead recalls as healthy (#252 field bundles).
func (s *Server) SlotPulledSince(slot int, t time.Time) bool {
	if slot < 1 || slot > 6 {
		return false
	}
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()
	start := s.slotFetch[slot]
	if start.IsZero() || !start.After(t) {
		return false
	}
	if s.slotOpen[slot] > 0 {
		return true
	}
	return s.slotFetchEnd[slot].Sub(start) >= minSustainedFetch
}

// SetBoxStateFn wires the speaker-side condition reporter for
// /api/stream-status (see boxStateFn).
func (s *Server) SetBoxStateFn(fn func() string) {
	s.boxStateFn = fn
}

// LastActivity reports when the box last opened any proxied stream and when
// the last terminal upstream failure happened (zero times = never). Consumed
// by the webui's wedge detector.
func (s *Server) LastActivity() (lastFetch, lastFailure time.Time) {
	s.fetchMu.Lock()
	lastFetch = s.lastFetch
	s.fetchMu.Unlock()
	s.errMu.Lock()
	lastFailure = s.lastErr.when
	s.errMu.Unlock()
	return lastFetch, lastFailure
}

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
	// boxState surfaces a SPEAKER-side condition ("wedged", "login-error")
	// that makes every station fail, so the app can say what is actually
	// wrong instead of blaming the station and cycling radio-browser
	// alternates ("Sender spielt nicht ... suche andere Quelle" while the box
	// was rejecting sources as not-logged-in). Additive: absent when the box
	// is fine, ignored by older frontends.
	boxState := ""
	if s.boxStateFn != nil {
		boxState = s.boxStateFn()
	}
	s.errMu.Lock()
	f := s.lastErr
	s.errMu.Unlock()
	if f.when.IsZero() || time.Since(f.when) > streamStatusTTL {
		if boxState != "" {
			fmt.Fprintf(w, `{"error":false,"boxState":%q}`, boxState)
			return
		}
		fmt.Fprint(w, `{"error":false}`)
		return
	}
	// A network-level "unreachable" is ambiguous: the one station may be down,
	// or the speaker has no internet at all. When outbound is also down, report a
	// distinct "offline" reason so the app can say "the speaker has no internet
	// connection" and offer to re-run Wi-Fi setup, instead of blaming every
	// station in turn (#375). Only checked on the "unreachable" catch-all, so a
	// clear 403/blocked/gone is never masked and the probe stays off the hot path.
	if f.reason == "unreachable" && !s.boxHasOutbound() {
		f.reason = "offline"
	}
	body, err := json.Marshal(struct {
		Error    bool   `json:"error"`
		Status   int    `json:"status"`
		Reason   string `json:"reason"`
		URL      string `json:"url"`
		AgeMs    int64  `json:"ageMs"`
		BoxState string `json:"boxState,omitempty"`
	}{
		Error:    true,
		Status:   f.code,
		Reason:   f.reason,
		URL:      f.url,
		AgeMs:    time.Since(f.when).Milliseconds(),
		BoxState: boxState,
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

// handleRaw streams an arbitrary URL through — used by the radio search
// play path so Bose's UPnP can receive HTTPS streams via us as well. The
// URL arrives as a ?u=<base64url> parameter.
func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	s.noteFetch()
	enc := r.URL.Query().Get("u")
	if enc == "" {
		http.Error(w, "u missing", http.StatusBadRequest)
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		// Fallback: maybe plain URL-encoded
		decoded = []byte(enc)
	}
	url := unwrapSelfProxy(string(decoded))
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
	s.resetAudioGap()
	headersSent := false
	var lastErr error
	for attempt := 0; attempt < 60; attempt++ {
		if r.Context().Err() != nil {
			s.logger.Info("stream proxy end: client gone", "kind", "raw", "elapsed", time.Since(start).Round(time.Second).String())
			return
		}
		if attempt > 0 {
			if gap := s.audioGap(); gap > 1*time.Second {
				s.logger.Warn("stream proxy audio gap before reconnect", "kind", "raw",
					"attempt", attempt, "gapMs", gap.Milliseconds(), "lastErr", errStr(lastErr))
			}
			s.logger.Info("stream proxy reconnect", "kind", "raw", "attempt", attempt, "lastErr", errStr(lastErr))
			select {
			case <-time.After(500 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		var boseAlive bool
		boseAlive, lastErr = s.streamOne(r.Context(), w, r, url, !headersSent)
		if errors.Is(lastErr, errPlaylistIsHLS) && !headersSent {
			// The URL had no .m3u8 suffix but its body is an HLS playlist; demux
			// it (#252). serveHLS only errors before writing audio, so http.Error
			// stays valid here.
			if err := s.serveHLS(r.Context(), w, r, url); err != nil {
				s.logger.Warn("stream proxy: HLS (via content-type) playback failed", "kind", "raw", "url", url, "err", err)
				s.recordFailure(url, err)
				http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
			}
			return
		}
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
	s.noteFetch()
	slotStr := strings.TrimPrefix(r.URL.Path, "/stream/")
	slot, err := strconv.Atoi(slotStr)
	if err != nil || slot < 1 || slot > 6 {
		http.Error(w, "invalid slot", http.StatusBadRequest)
		return
	}
	p, ok := s.store.Get(slot)
	if !ok || p.StreamURL == "" {
		// Log before 404ing: the box fetching a slot the store cannot serve is
		// exactly the "preset button does nothing" symptom, and this used to be
		// the only branch in the recall chain with no trace at all (#252).
		s.logger.Warn("stream proxy: box fetched a slot with no playable preset",
			"slot", slot, "found", ok)
		http.Error(w, "no preset", http.StatusNotFound)
		return
	}
	p.StreamURL = s.resolvePresetURL(slot, p.StreamURL)
	s.noteSlotFetch(slot)
	defer s.noteSlotFetchDone(slot)
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

	// We do exactly one GET to the CDN and copy bytes to Bose. When the CDN
	// returns EOF (token expiry), we reconnect internally and keep streaming —
	// Bose's connection stays open. We have a generous retry budget, but on a
	// client disconnect (context cancel) we stop immediately — otherwise we
	// would charge into an endless loop against the CDN.
	start := time.Now()
	s.resetAudioGap()
	headersSent := false
	var lastErr error
	for attempt := 0; attempt < 60; attempt++ {
		// If Bose has closed the connection, bail out immediately.
		if r.Context().Err() != nil {
			s.logger.Info("stream proxy end: client gone", "slot", slot, "elapsed", time.Since(start).Round(time.Second).String())
			return
		}
		if attempt > 0 {
			if gap := s.audioGap(); gap > 1*time.Second {
				s.logger.Warn("stream proxy audio gap before reconnect", "slot", slot,
					"attempt", attempt, "gapMs", gap.Milliseconds(), "lastErr", errStr(lastErr))
			}
			s.logger.Info("stream proxy reconnect", "slot", slot, "attempt", attempt, "lastErr", errStr(lastErr))
			// Wait briefly so we do not overload the CDN with reconnects
			select {
			case <-time.After(500 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		// Fetch the current URL — the user might have changed the preset in
		// the meantime.
		cur, ok := s.store.Get(slot)
		if !ok || cur.StreamURL == "" {
			return
		}
		curURL := s.resolvePresetURL(slot, cur.StreamURL)
		boseAlive, err := s.streamOne(r.Context(), w, r, curURL, !headersSent)
		lastErr = err
		if errors.Is(err, errPlaylistIsHLS) && !headersSent {
			// A preset whose URL had no .m3u8 suffix but serves an HLS playlist
			// body — demux it (#252). serveHLS only errors before writing audio.
			if herr := s.serveHLS(r.Context(), w, r, curURL); herr != nil {
				s.logger.Warn("stream proxy: HLS (via content-type) preset playback failed", "slot", slot, "url", curURL, "err", herr)
				s.recordFailure(curURL, herr)
				http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
			}
			return
		}
		if !boseAlive {
			// Bose closed the connection (standby, station switch). A normal
			// end, kept clearly distinct from the give-up case below, so the
			// log can tell a box stop from an outbound problem.
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
	// 60 reconnects exhausted: the box still wanted bytes, but upstream
	// kept failing. A network error in lastErr points at the box's outbound
	// path (e.g. a flaky cable) rather than the box itself.
	s.logger.Warn("stream proxy gave up reconnecting", "slot", slot, "attempts", 60, "elapsed", time.Since(start).Round(time.Second).String(), "lastErr", errStr(lastErr))
}

// streamOne does one round trip to the upstream and copies the body to w.
// It returns boseAlive=true when the connection to Bose is still open (a
// reconnect makes sense), false when Bose has disconnected. The second
// return value is the last upstream error of this attempt (nil on a clean
// EOF or a normal Bose disconnect); the caller logs it at stream end so a
// box stop can be told apart from outbound problems.
func (s *Server) streamOne(ctx context.Context, w http.ResponseWriter, r *http.Request, url string, sendHeaders bool) (bool, error) {
	return s.streamOneDepth(ctx, w, r, url, sendHeaders, 0)
}

// streamOneDepth is streamOne with a playlist-resolution recursion guard: an
// audio/x-mpegurl response that turns out to be a plain M3U/PLS pointer file is
// re-fetched at its first real stream URL (depth+1), capped so a playlist that
// points at itself or at another playlist cannot loop forever.
func (s *Server) streamOneDepth(ctx context.Context, w http.ResponseWriter, r *http.Request, url string, sendHeaders bool, depth int) (bool, error) {
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
		// If Bose has closed the connection, a retry makes no sense.
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
		// Headers already sent — try a reconnect
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

	// HLS/DASH/playlist detected by MIME type (a URL without the telltale
	// suffix). Reading a segment playlist or a pointer file as audio yields
	// instant EOF and a reconnect storm, so classify it here, before any bytes
	// are written, and switch paths:
	//   - DASH (dash+xml): still unsupported -> report not-playable and stop.
	//   - HLS body (#EXT-X- markers): re-run through serveHLS, which demuxes it.
	//   - plain M3U/PLS pointer: resolve to its first real stream URL and play
	//     that (#252: Absolut Relax et al. serve audio/x-mpegurl on a URL with
	//     no .m3u suffix, so it never reached the .m3u8 HLS branch upstream).
	if ct := resp.Header.Get("Content-Type"); isHLSorDASHContentType(ct) {
		if strings.Contains(strings.ToLower(ct), "dash") {
			if s.shouldLogFail(url) {
				s.logger.Warn("stream proxy: DASH content-type not supported yet", "url", url, "contentType", ct)
			}
			dashErr := fmt.Errorf("dash not supported (content-type %q)", ct)
			s.recordFailure(url, dashErr)
			if sendHeaders {
				http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
			}
			return false, dashErr
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		text := string(body)
		if strings.Contains(text, "#EXT-X-") {
			// A real HLS playlist reached the raw path (URL had no .m3u8 suffix).
			// Let the caller re-serve it through serveHLS.
			return false, errPlaylistIsHLS
		}
		if media := firstMediaURLFromPlaylist(text, url); media != "" && media != url && depth < 2 {
			s.logger.Info("stream proxy: resolved playlist pointer to stream URL",
				"playlist", url, "stream", media, "depth", depth+1)
			resp.Body.Close()
			return s.streamOneDepth(ctx, w, r, media, sendHeaders, depth+1)
		}
		if s.shouldLogFail(url) {
			s.logger.Warn("stream proxy: playlist content-type not resolvable", "url", url, "contentType", ct)
		}
		plErr := fmt.Errorf("playlist not resolvable (content-type %q)", ct)
		s.recordFailure(url, plErr)
		if sendHeaders {
			http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
		}
		return false, plErr
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
			// Do not pass hop-by-hop headers through
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

	// Flush continuously so Bose's player does not wait on a buffer
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
	// Connection-scoped telemetry so a diagnostic bundle can explain a dropout
	// without a live capture: how long this upstream connection lasted and how
	// many bytes it delivered to the box before it ended (#185).
	connStart := time.Now()
	var connBytes int64
	gotData := false
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				// Bose closed the connection
				return false, nil
			}
			if flusher != nil {
				flusher.Flush()
			}
			connBytes += int64(n)
			gotData = true
			s.markByteDelivery() // records "last byte to box" wall clock across reconnects
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
			// Bose has closed — NO retry, otherwise an endless loop
			if errors.Is(readErr, context.Canceled) || ctx.Err() != nil {
				return false, nil
			}
			if readErr == io.EOF {
				// Clean EOF (typically CDN token expiry). Expected; the reconnect
				// is gap-free if it lands fast. INFO, with timing so a bundle shows
				// how often a station forces a reconnect.
				s.logger.Info("stream proxy upstream EOF, will reconnect", "url", url,
					"connectedSec", int(time.Since(connStart).Seconds()), "bytes", connBytes, "delivered", gotData)
				return true, nil
			}
			// Network-level read error mid-stream: this is the dropout cause for
			// the #185 class. WARN, with how long the connection survived and how
			// much it delivered, so the bundle pins the drop without a capture.
			s.logger.Warn("stream proxy upstream read fail, will reconnect", "url", url, "err", readErr,
				"connectedSec", int(time.Since(connStart).Seconds()), "bytes", connBytes, "delivered", gotData)
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
