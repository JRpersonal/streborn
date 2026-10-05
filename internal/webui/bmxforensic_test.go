package webui

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func bmxTestServer(buf *bytes.Buffer) *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(buf, nil))}
}

// resetBMXLimiters gives each test fresh limiters so a path logged by an
// earlier test does not suppress the line this one asserts on.

func resetBMXLimiters(t *testing.T) {
	t.Helper()
	oldB, oldS := bmxLogLimiter, strayLogLimiter
	bmxLogLimiter = newPathLogLimiter(time.Minute, 256)
	strayLogLimiter = newPathLogLimiter(time.Minute, 256)
	t.Cleanup(func() { bmxLogLimiter, strayLogLimiter = oldB, oldS })
}

func assertJSON(t *testing.T, rr *httptest.ResponseRecorder, wantStatus int) map[string]any {
	t.Helper()
	if rr.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body %q)", rr.Code, wantStatus, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON", ct)
	}
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rr.Body.String())
	}
	return m
}

func TestBMXUnknownPathAnswersJSON404(t *testing.T) {
	resetBMXLimiters(t)
	var logs bytes.Buffer
	s := bmxTestServer(&logs)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/bmx/somethingelse/v1/x?y=1", nil)
	req.Header.Set("User-Agent", "Bose/27.0.6")
	s.handleBMX(rr, req)
	m := assertJSON(t, rr, http.StatusNotFound)
	if m["error"] != "not implemented" {
		t.Fatalf("error = %v", m["error"])
	}
	if strings.Contains(rr.Body.String(), "<") {
		t.Fatalf("answered HTML: %q", rr.Body.String())
	}
	out := logs.String()
	for _, want := range []string{"bmx: request from the box on the webui port", "path=/bmx/somethingelse/v1/x", `query="y=1"`, "Bose/27.0.6", "status=404", "not implemented"} {
		if !strings.Contains(out, want) {
			t.Errorf("log misses %q: %s", want, out)
		}
	}
}

func TestBMXLogRateLimitedPerPath(t *testing.T) {
	resetBMXLimiters(t)
	now := time.Unix(1_000_000, 0)
	oldNow := bmxNow
	bmxNow = func() time.Time { return now }
	t.Cleanup(func() { bmxNow = oldNow })

	var logs bytes.Buffer
	s := bmxTestServer(&logs)
	hit := func(p string) {
		s.handleBMX(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
	}
	count := func() int { return strings.Count(logs.String(), "bmx: request from the box") }

	hit("/bmx/a")
	hit("/bmx/a")
	hit("/bmx/a")
	if c := count(); c != 1 {
		t.Fatalf("after 3 hits on one path: %d lines, want 1", c)
	}
	hit("/bmx/b")
	if c := count(); c != 2 {
		t.Fatalf("a new path must log on first sight: %d lines, want 2", c)
	}
	now = now.Add(61 * time.Second)
	hit("/bmx/a")
	if c := count(); c != 3 {
		t.Fatalf("after the window: %d lines, want 3", c)
	}
}

func TestPathLogLimiterBounded(t *testing.T) {
	l := newPathLogLimiter(time.Minute, 3)
	now := time.Unix(0, 0)
	for _, p := range []string{"a", "b", "c", "d", "e"} {
		if !l.allow(p, now) {
			t.Fatalf("first sight of %s refused", p)
		}
	}
	if len(l.last) > 3 {
		t.Fatalf("map grew to %d, cap 3", len(l.last))
	}
}

func TestStrayBoxRequestLogged(t *testing.T) {
	resetBMXLimiters(t)
	oldAddrs := ownAddrsFn
	ownAddrsFn = func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP("192.0.2.1"), Mask: net.CIDRMask(24, 32)}}, nil
	}
	t.Cleanup(func() { ownAddrsFn = oldAddrs })

	var logs bytes.Buffer
	s := bmxTestServer(&logs)
	note := func(path, remote string) {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr = remote
		s.noteStrayBoxRequest(r)
	}
	count := func() int { return strings.Count(logs.String(), "the box requested a path") }

	note("/", "127.0.0.1:1234")
	note("/favicon.ico", "192.0.2.50:1234") // a phone on the LAN
	if c := count(); c != 0 {
		t.Fatalf("logged %d lines for the index or a phone", c)
	}
	note("/v1/whatever", "127.0.0.1:1234")
	note("/v1/whatever", "127.0.0.1:1234")
	note("/other", "192.0.2.1:5555") // the box's own LAN address
	if c := count(); c != 2 {
		t.Fatalf("logged %d lines, want 2 (one per path from the box)", c)
	}
}
