package webui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The two write-capable debug endpoints must be invisible unless explicitly
// switched on. One removes and rewrites preset slots, the other redirects the
// speaker's cloud traffic to another machine; both sit on port 8888, reachable
// from anywhere on the owner's LAN.
func TestWriteCapableDebugEndpointsAreOffByDefault(t *testing.T) {
	t.Setenv("STR_DEV_TOOLS", "")
	if devToolsEnabled() {
		t.Skip("a devtools marker exists on this machine; the default cannot be observed here")
	}

	s := New(":0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	cases := []struct {
		path string
		body string
		fn   func(http.ResponseWriter, *http.Request)
	}{
		{"/api/debug/native-preset-probe", `{"slot":6}`, s.handleNativeProbe},
		{"/api/debug/marge-lab", `{"target":"192.168.0.2:9080"}`, s.handleMargeLab},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			c.fn(w, httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.body)))
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s answered %d, want 404 so it is not even discoverable", c.path, w.Code)
			}
		})
	}

	// The read-only state endpoint must stay available: diagnostic bundles are
	// built from it, and gating it would blind every support case.
	w := httptest.NewRecorder()
	s.handleDebugState(w, httptest.NewRequest(http.MethodGet, "/api/debug/state", nil))
	if w.Code == http.StatusNotFound {
		t.Fatal("/api/debug/state must remain reachable; bundles depend on it")
	}
}

// The developer endpoints take an optional body: a bare POST means the
// defaults (clear the relay, probe slot 6). A body that is there and broken is
// refused before anything happens.
func TestDevToolsEndpointsTakeAnEmptyBodyButNotABrokenOne(t *testing.T) {
	t.Setenv("STR_DEV_TOOLS", "1")
	s := New(":0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	var targets []string
	s.margeForward = func(target string) error {
		targets = append(targets, target)
		return nil
	}
	post := func(path string, fn func(http.ResponseWriter, *http.Request), body string) int {
		w := httptest.NewRecorder()
		fn(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return w.Code
	}

	if code := post("/api/debug/marge-lab", s.handleMargeLab, ""); code != http.StatusOK {
		t.Fatalf("bare POST to marge-lab: %d, want 200 (clears the relay)", code)
	}
	if code := post("/api/debug/marge-lab", s.handleMargeLab, `{"target":`); code != http.StatusBadRequest {
		t.Fatalf("broken marge-lab body: %d, want 400", code)
	}
	if len(targets) != 1 || targets[0] != "" {
		t.Fatalf("forward calls = %q, want one empty clear from the bare POST only", targets)
	}

	// No presets store in this server: a bare body gets as far as that check
	// (503), a broken one is refused before it (400).
	if code := post("/api/debug/native-preset-probe", s.handleNativeProbe, ""); code != http.StatusServiceUnavailable {
		t.Fatalf("bare POST to native-preset-probe: %d, want 503", code)
	}
	if code := post("/api/debug/native-preset-probe", s.handleNativeProbe, `{"slot":`); code != http.StatusBadRequest {
		t.Fatalf("broken native-preset-probe body: %d, want 400", code)
	}
}

func TestDevToolsOptIn(t *testing.T) {
	t.Setenv("STR_DEV_TOOLS", "1")
	if !devToolsEnabled() {
		t.Fatal("STR_DEV_TOOLS=1 must enable the development endpoints")
	}
	t.Setenv("STR_DEV_TOOLS", "0")
	if devToolsEnabled() {
		t.Fatal("STR_DEV_TOOLS=0 must not enable them")
	}
}
