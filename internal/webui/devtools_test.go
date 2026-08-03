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
