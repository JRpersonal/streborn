// icy.go: ICY (Icecast/SHOUTcast) protocol support — the legacy "ICY 200 OK"
// status-line rewrite, metadata de-interleaving, StreamTitle parsing, and the
// live-title accessors and endpoint.

package streamproxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

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
	title = titleToUTF8(title)
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

// titleToUTF8 normalises an ICY StreamTitle to valid UTF-8. Shoutcast/Icecast
// stations often send the title in Latin-1 (ISO-8859-1), not UTF-8, so an umlaut
// in a track or artist ("Fürstenfeld", "Böhse Onkelz") arrives as a lone high
// byte. Left as-is it is invalid UTF-8, and json.Marshal to the app then replaces
// it with U+FFFD, so the song shows up garbled as "F�rstenfeld". This is the same
// fix the box names get in boxapi.ensureUTF8: a title that is already valid UTF-8
// is returned unchanged, otherwise the bytes are read as Latin-1 (a 1:1 map to the
// first 256 code points, so ASCII is untouched and only high bytes are widened)
// and re-encoded as UTF-8.
func titleToUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	b := []byte(s)
	out := make([]byte, 0, len(b)+8)
	for _, c := range b {
		out = utf8.AppendRune(out, rune(c))
	}
	return string(out)
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

// titleEndGrace is how long after a handler ends the title survives while a
// successor handler of the SAME stream may still take over. An immediate wipe
// fed a self-sustaining stutter loop with the on-display track push enabled
// (v0.9.53, #119): the push re-issues the URI, the box drops and re-fetches
// the same stream, the OLD handler's wipe then emptied the title, and the
// next (unchanged) metadata block counted as a change and re-fired the push,
// re-buffering the box once per throttle window. A takeover bumps the url's
// generation and cancels the pending wipe; a stream that truly ended has no
// successor and goes blank after the grace, so #274 stays fixed.
const titleEndGrace = 5 * time.Second

// noteStreamStart marks a handler taking (over) url; called at every proxy
// handler start so a pending end-of-stream title wipe knows it is stale.
func (s *Server) noteStreamStart(url string) {
	s.titleMu.Lock()
	if s.titleGens == nil {
		s.titleGens = make(map[string]uint64)
	}
	s.titleGens[url]++
	s.titleMu.Unlock()
}

// clearTitleOnEnd drops the title when the proxy stops carrying url. Without
// it CurrentTitle kept reporting the LAST stream's song forever, and a client
// that cannot tell radio-via-proxy from other playback showed it under
// whatever came next: the phone remote's source gate cured the line inputs,
// but a NAS track plays over the same UPnP source as proxied radio, so the
// old radio song sat under it (SA-5, #274, still with v0.9.52). Guarded by
// URL so a handler that outlived a station switch cannot wipe the successor's
// title, and DELAYED by titleEndGrace so a box re-fetch of the same stream
// (display push, brief flap) keeps the title instead of re-firing it.
func (s *Server) clearTitleOnEnd(url string) {
	s.titleMu.Lock()
	gen := s.titleGens[url]
	s.titleMu.Unlock()
	time.AfterFunc(titleEndGrace, func() { s.wipeTitleIfUnclaimed(url, gen) })
}

// wipeTitleIfUnclaimed is clearTitleOnEnd's delayed half: it wipes only when
// no successor handler for url has started since the snapshot was taken.
func (s *Server) wipeTitleIfUnclaimed(url string, gen uint64) {
	s.titleMu.Lock()
	defer s.titleMu.Unlock()
	if s.titleGens[url] != gen {
		return
	}
	delete(s.titleGens, url)
	if url == s.curTitleURL {
		s.curTitle = ""
	}
}

// icyConn wraps a net.Conn so a legacy SHOUTcast "ICY 200 OK" response line is
// rewritten to "HTTP/1.0 200 OK" on the first read, letting Go's net/http parse
// the response instead of rejecting it. All bytes after the status line (headers,
// the ICY-interleaved audio) pass through unchanged. Only the very first line is
// inspected; a normal "HTTP/1.x ..." response is left exactly as received.
type icyConn struct {
	net.Conn
	br        *bufio.Reader
	inspected bool
	prefix    []byte // rewritten status-line bytes not yet handed to the caller
}

func (c *icyConn) Read(p []byte) (int, error) {
	if !c.inspected {
		c.inspected = true
		// Peek only as far as the protocol token; blocks until the response
		// arrives, which is exactly when http.Transport issues its first read.
		if head, err := c.br.Peek(4); err == nil && string(head[:3]) == "ICY" && (head[3] == ' ' || head[3] == '\t') {
			if line, err := c.br.ReadString('\n'); err == nil {
				// "ICY 200 OK\r\n" -> "HTTP/1.0 200 OK\r\n" (keep the rest verbatim).
				c.prefix = append([]byte("HTTP/1.0"), line[3:]...)
			} else {
				// No full line yet: hand back what we consumed unchanged so we
				// never lose bytes; the transport will keep reading.
				c.prefix = []byte(line)
			}
		}
	}
	if len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.br.Read(p)
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

// handleTitle returns the live ICY StreamTitle of the stream currently
// proxied, or "" when the station sends no metadata. Cheap: a single guarded
// string read. The desktop app polls this on a slow cadence to show the live
// radio track next to the station name, the same way it shows the bitrate.
func (s *Server) handleTitle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	writeJSONString(w, "title", s.CurrentTitle())
}

// writeJSONString emits {"<key>":"<value>"} with value JSON-escaped via
// encoding/json, so a StreamTitle containing quotes, backslashes, or raw
// control characters (a garbled or hostile ICY metadata block) cannot break
// the response. The hand-rolled replacer used before covered only \ " \n \r
// \t and leaked the remaining U+0000..U+001F bytes into the output as-is,
// which is invalid JSON and blanked the live title in the app.
func writeJSONString(w io.Writer, key, value string) {
	// json.Marshal of a string cannot fail; invalid UTF-8 is replaced with
	// U+FFFD, the same policy the rest of the app applies.
	k, _ := json.Marshal(key)
	v, _ := json.Marshal(value)
	fmt.Fprintf(w, "{%s:%s}", k, v)
}
