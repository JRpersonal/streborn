// icy.go: ICY (Icecast/SHOUTcast) protocol support — the legacy "ICY 200 OK"
// status-line rewrite, metadata de-interleaving, StreamTitle parsing, and the
// live-title accessors and endpoint.

package streamproxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
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

// writeJSONString emits {"<key>":"<value>"} with value JSON-escaped, so a
// StreamTitle containing quotes or backslashes cannot break the response.
func writeJSONString(w io.Writer, key, value string) {
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	fmt.Fprintf(w, `{%q:"%s"}`, key, esc.Replace(value))
}
