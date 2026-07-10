package webui

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pollStatus runs one GET /api/status against the handler.
func pollStatus(s *Server) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.handleStatus(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	return w
}

// Once the box stops answering, /api/status keeps serving the cached
// now_playing body (clients regex-parse it, so it must stay intact), but past
// statusStaleAfter it must SAY so: the stale/age headers let clients render
// the real "unreachable" state instead of hours-old "playing" data, and one
// WARN (not one per poll) marks the transition in the log.
func TestStatusStaleFallbackSignalsAge(t *testing.T) {
	var logBuf bytes.Buffer
	cached := `<nowPlaying source="UPNP"><playStatus>PLAY_STATE</playStatus></nowPlaying>`
	s := &Server{
		logger: slog.New(slog.NewTextHandler(&logBuf, nil)),
		// A host:port in boxHost yields an invalid URL (double port), so the
		// box GET fails instantly without touching the network - the same
		// fallback path a dead BoseApp triggers.
		boxHost:    "127.0.0.1:1",
		statusBody: []byte(cached),
		statusCode: http.StatusOK,
		statusAt:   time.Now().Add(-45 * time.Second),
	}

	w := pollStatus(s)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the cached body keeps being served)", w.Code)
	}
	if w.Body.String() != cached {
		t.Fatalf("body changed: %q, clients regex-parse the box XML and must get it verbatim", w.Body.String())
	}
	if w.Header().Get("X-STR-Status-Stale") != "1" {
		t.Error("45s-old fallback served without X-STR-Status-Stale: 1")
	}
	if age, err := strconv.Atoi(w.Header().Get("X-STR-Status-Age")); err != nil || age < 44 {
		t.Errorf("X-STR-Status-Age = %q, want >= 44 seconds", w.Header().Get("X-STR-Status-Age"))
	}

	// A second poll in the same outage serves the same signal but logs no
	// second WARN.
	_ = pollStatus(s)
	if n := strings.Count(logBuf.String(), "marked stale"); n != 1 {
		t.Errorf("stale WARN logged %d times over two polls, want exactly once per outage", n)
	}
}

// A young fallback (a brief BoseApp hiccup) is NOT stale: the body is served
// as before, with the age reported but no stale marker and no WARN.
func TestStatusYoungFallbackIsNotStale(t *testing.T) {
	var logBuf bytes.Buffer
	s := &Server{
		logger:     slog.New(slog.NewTextHandler(&logBuf, nil)),
		boxHost:    "127.0.0.1:1",
		statusBody: []byte(`<nowPlaying source="UPNP"/>`),
		statusCode: http.StatusOK,
		statusAt:   time.Now().Add(-5 * time.Second),
	}

	w := pollStatus(s)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Header().Get("X-STR-Status-Stale") != "" {
		t.Error("5s-old fallback flagged stale; brief hiccups must stay unflagged")
	}
	if w.Header().Get("X-STR-Status-Age") == "" {
		t.Error("fallback served without the X-STR-Status-Age header")
	}
	if strings.Contains(logBuf.String(), "marked stale") {
		t.Error("young fallback logged the stale WARN")
	}
}
