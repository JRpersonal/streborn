package webui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// While an agent update is installing, the update itself may reclaim the
// engine's ~16 MB to make room for the new binary. An engine delivered into
// that window is deleted moments later and the speaker restarts without one.
// A SoundTouch 30 lost its engine twice in one day this way, both times
// because a client pushed the engine while the update it had just started was
// still running.
func TestSidecarIsRefusedWhileAnUpdateIsInstalling(t *testing.T) {
	updateInFlight.Store(true)
	t.Cleanup(func() { updateInFlight.Store(false) })

	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec := httptest.NewRecorder()
	s.handleAgentSidecar(rec, httptest.NewRequest(http.MethodPost, "/api/agent/sidecar", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: the speaker must say no rather than accept an engine it is about to delete",
			rec.Code, http.StatusConflict)
	}
	if body := rec.Body.String(); !strings.Contains(body, "update-in-flight") {
		t.Errorf("body = %q, want a machine-readable code the app can act on", body)
	}
}

// And it must not refuse the rest of the time, or every speaker loses its
// engine delivery for good.
func TestSidecarIsAcceptedWhenNoUpdateIsRunning(t *testing.T) {
	updateInFlight.Store(false)

	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec := httptest.NewRecorder()
	s.handleAgentSidecar(rec, httptest.NewRequest(http.MethodPost, "/api/agent/sidecar", nil))

	if rec.Code == http.StatusConflict {
		t.Fatal("a speaker with no update running must not refuse the engine")
	}
}
