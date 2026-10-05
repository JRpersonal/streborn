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
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
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

// peekRecorder passes a response through while keeping its status and the
// first bytes of its body for the log line.
type peekRecorder struct {
	http.ResponseWriter
	status int
	head   bytes.Buffer
}

func (p *peekRecorder) WriteHeader(code int) {
	if p.status == 0 {
		p.status = code
	}
	p.ResponseWriter.WriteHeader(code)
}

func (p *peekRecorder) Write(b []byte) (int, error) {
	if p.status == 0 {
		p.status = http.StatusOK
	}
	if room := bmxRespPeek - p.head.Len(); room > 0 {
		if len(b) < room {
			room = len(b)
		}
		p.head.Write(b[:room])
	}
	return p.ResponseWriter.Write(b)
}

// handleBMX serves everything under /bmx/: the TuneIn adapter routes, and a
// JSON 404 for anything else. Every request is logged (rate-limited).
func (s *Server) handleBMX(w http.ResponseWriter, r *http.Request) {
	bodyLen := 0
	if r.Body != nil {
		n, _ := io.Copy(io.Discard, io.LimitReader(r.Body, bmxBodyPeek))
		bodyLen = int(n)
	}
	rec := &peekRecorder{ResponseWriter: w}
	s.routeBMX(rec, r)
	if !bmxLogLimiter.allow(r.URL.Path, bmxNow()) {
		return
	}
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	s.logger.Info("bmx: request from the box on the webui port",
		"method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery,
		"userAgent", r.UserAgent(), "accept", r.Header.Get("Accept"),
		"bodyBytes", bodyLen, "remote", r.RemoteAddr,
		"status", status, "response", rec.head.String())
}

func (s *Server) routeBMX(w http.ResponseWriter, r *http.Request) {
	_ = r
	writeBMXError(w, http.StatusNotFound, "not implemented")
}

// writeBMXError answers in JSON, never HTML: the firmware parses whatever
// comes back from a BMX adapter as JSON, and an HTML page turns a clean
// failure into a parse error that hides the real cause.
func writeBMXError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
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
