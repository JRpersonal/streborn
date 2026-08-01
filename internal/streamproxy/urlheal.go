// urlheal.go: stream-URL classification and healing — HLS/DASH/playlist
// detection, self-proxy unwrap, and preset URL resolution.

package streamproxy

import (
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/JRpersonal/streborn/internal/netutil"
)

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
