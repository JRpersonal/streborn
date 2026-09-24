package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
)

// The PWA manifest is the one place that can override the device's own
// rotation, and it did: "orientation":"portrait" turned the remote upright on a
// tablet mounted sideways on a wall, where the screen cannot follow (reported by
// mail, 2026-09-20). Nothing covered the manifest at all, so the lock was
// invisible to the suite. It binds only the INSTALLED home-screen app, which is
// exactly the case that broke, and a browser tab was the workaround.
func TestManifestDoesNotLockTheScreenRotation(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w := httptest.NewRecorder()
	s.handleManifest(w, httptest.NewRequest("GET", "/manifest.webmanifest", nil))

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Errorf("Content-Type = %q, want application/manifest+json", ct)
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest is not JSON: %v (%s)", err, w.Body.String())
	}
	if got := m["orientation"]; got != "any" {
		t.Errorf("orientation = %v, want \"any\" so the device decides", got)
	}
	// The rest of what makes it installable, pinned in the same pass because
	// a manifest that stops being installable fails silently: the phone just
	// offers a bookmark instead of an app and nobody finds out why.
	if got := m["display"]; got != "standalone" {
		t.Errorf("display = %v, want standalone", got)
	}
	if got := m["start_url"]; got != "/" {
		t.Errorf("start_url = %v, want /", got)
	}
	icons, _ := m["icons"].([]any)
	large := false
	for _, ic := range icons {
		if e, ok := ic.(map[string]any); ok && e["sizes"] == "1024x1024" {
			large = true
		}
	}
	if !large {
		t.Error("no icon >= 512px: Android then offers a bookmark, not an installable app")
	}
}
