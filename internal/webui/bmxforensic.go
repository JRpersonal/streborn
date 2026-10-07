package webui

// Forensics for the BMX adapter paths the speaker requests on this port.
//
// The BMX service registry STR serves (internal/marge/bmxservices.go) points
// every radio service at this agent's webui port on loopback: the orion
// adapter under /core02/..., TuneIn under /bmx/tunein. Before this handler
// existed nothing on the mux answered /bmx/, so a TuneIn request fell through
// to the catchall and the speaker got the phone remote's HTML page with a 200.
// The firmware then reported 4502 BMX_JSON_PARSE_ERROR ("value, object or
// array expected" at the first byte of "<!DOCTYPE") and the select failed with
// INVALID_SOURCE (discussion #500), and the agent log said nothing at all
// because nothing on this port logged the request.
//
// So every /bmx/ request is logged here, with what the speaker sent and what
// it got back, and an unknown path answers a JSON 404 the firmware can parse
// instead of HTML. The log is rate-limited per path: the first request to a
// path is always logged (that is the evidence), repeats at most once a minute.

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	bmxPrefix = "/bmx/"
	// bmxBodyPeek bounds how much of a request body is read for the log line.
	bmxBodyPeek = 4096
	// bmxRespPeek is how much of the response body is quoted in the log line.
	bmxRespPeek = 200
)

// pathLogLimiter decides whether a log line for a path is due: the first
// request per path always, then at most one per window. The map is bounded so
// a speaker (or anything else) walking many distinct paths cannot grow it
// without limit; when full it starts over, which at worst logs a path again.
type pathLogLimiter struct {
	mu     sync.Mutex
	last   map[string]time.Time
	window time.Duration
	max    int
}

func newPathLogLimiter(window time.Duration, max int) *pathLogLimiter {
	return &pathLogLimiter{last: map[string]time.Time{}, window: window, max: max}
}

func (l *pathLogLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t, ok := l.last[key]; ok && now.Sub(t) < l.window {
		return false
	}
	if _, ok := l.last[key]; !ok && len(l.last) >= l.max {
		l.last = map[string]time.Time{}
	}
	l.last[key] = now
	return true
}

var (
	bmxLogLimiter   = newPathLogLimiter(time.Minute, 256)
	strayLogLimiter = newPathLogLimiter(time.Minute, 256)
	// bmxNow is the clock behind the limiters, a seam for tests.
	bmxNow = time.Now
)

// handleBMX serves everything under /bmx/: the TuneIn adapter routes, and a
// JSON 404 for anything else. Every request is logged (rate-limited).
//
// The route handlers only return a status and a JSON body; this is the one
// place that writes a BMX response. That keeps the content type fixed to JSON
// (with nosniff) for every answer, so nothing derived from the request can
// ever be served as something a browser would render.
func (s *Server) handleBMX(w http.ResponseWriter, r *http.Request) {
	bodyLen := 0
	if r.Body != nil {
		n, _ := io.Copy(io.Discard, io.LimitReader(r.Body, bmxBodyPeek))
		bodyLen = int(n)
	}
	status, body := s.routeBMX(r)
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	if !bmxLogLimiter.allow(r.URL.Path, bmxNow()) {
		return
	}
	head := body
	if len(head) > bmxRespPeek {
		head = head[:bmxRespPeek]
	}
	s.logger.Info("bmx: request from the box on the webui port",
		"method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery,
		"userAgent", r.UserAgent(), "accept", r.Header.Get("Accept"),
		"bodyBytes", bodyLen, "remote", r.RemoteAddr,
		"status", status, "response", string(head))
}

func (s *Server) routeBMX(r *http.Request) (int, []byte) {
	p := r.URL.Path
	switch {
	case p == tuneInTokenPath:
		return s.handleTuneInToken(r)
	case strings.HasPrefix(p, tuneInStationPrefix):
		return s.handleTuneInStation(r)
	case isNowPlayingPath(p):
		return s.handleTuneInNowPlaying(r)
	default:
		return bmxError(http.StatusNotFound, "not implemented")
	}
}

// bmxError builds a JSON error answer, never HTML: the firmware parses
// whatever comes back from a BMX adapter as JSON, and an HTML page turns a
// clean failure into a parse error that hides the real cause.
func bmxError(status int, msg string) (int, []byte) {
	return bmxJSON(status, map[string]string{"error": msg})
}

// bmxJSON marshals v as a BMX answer body.
func bmxJSON(status int, v any) (int, []byte) {
	b, err := json.Marshal(v)
	if err != nil {
		return http.StatusInternalServerError, []byte(`{"error":"encoding failed"}`)
	}
	return status, append(b, '\n')
}

// noteStrayBoxRequest logs a request the speaker itself sent to a path the
// remote does not serve, which otherwise lands silently on the index page.
// Phones asking for /favicon.ico and the like are not logged: only loopback or
// one of this machine's own addresses counts as "the box".
func (s *Server) noteStrayBoxRequest(r *http.Request) {
	if r.URL.Path == "/" || !requestFromBox(r.RemoteAddr) {
		return
	}
	if !strayLogLimiter.allow(r.URL.Path, bmxNow()) {
		return
	}
	s.logger.Info("webui: the box requested a path the remote does not serve, answering the index page",
		"method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery,
		"userAgent", r.UserAgent(), "accept", r.Header.Get("Accept"), "remote", r.RemoteAddr)
}

// ownAddrsFn lists this machine's interface addresses, a seam for tests.
var ownAddrsFn = net.InterfaceAddrs

// requestFromBox reports whether a request came from the speaker the agent
// runs on: loopback, or one of the machine's own interface addresses.
func requestFromBox(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	addrs, err := ownAddrsFn()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}
