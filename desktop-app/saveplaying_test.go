package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A long-press save of what plays goes through the agent's POST
// /api/box/save-playing (#1467). The desktop app updates independently of the
// agent, so the call must also be answered sensibly by an agent that predates
// it: such an agent has no route for the path, and its catch-all serves the
// phone page with 200 text/html for any path and any method.

func savePlayingBox(t *testing.T, h http.HandlerFunc) int {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return listenPort(t, srv)
}

func TestSavePlayingPresetReadsTheAgentsAnswer(t *testing.T) {
	port := savePlayingBox(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/box/save-playing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"slot":2,"kind":"copy","name":"Jazz Lounge","unchanged":false}`))
	})
	got, err := moveApp(t).SavePlayingPreset("127.0.0.1", port, 2)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if got.Name != "Jazz Lounge" || got.Kind != "copy" {
		t.Errorf("saved = %+v, want a copy named Jazz Lounge", got)
	}
}

func TestSavePlayingPresetOnAnAgentWithoutTheCallFallsThrough(t *testing.T) {
	port := savePlayingBox(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>phone page</body></html>"))
	})
	_, err := moveApp(t).SavePlayingPreset("127.0.0.1", port, 1)
	if err == nil {
		t.Fatal("an HTML answer was taken as a save")
	}
	// The frontend falls back to its own save on exactly this status.
	if !strings.HasPrefix(err.Error(), "status 404:") {
		t.Errorf("err = %q, want a status 404 the frontend falls back on", err)
	}
}

func TestSavePlayingPresetOnAnUnreadableJSONAnswerFallsThrough(t *testing.T) {
	port := savePlayingBox(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("<not json"))
	})
	_, err := moveApp(t).SavePlayingPreset("127.0.0.1", port, 1)
	if err == nil || !strings.HasPrefix(err.Error(), "status 404:") {
		t.Errorf("err = %v, want a status 404 the frontend falls back on", err)
	}
}

func TestSavePlayingPresetPassesARefusalOn(t *testing.T) {
	port := savePlayingBox(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"already-on-slot","slot":5,"name":"Jazz Lounge"}`))
	})
	_, err := moveApp(t).SavePlayingPreset("127.0.0.1", port, 1)
	if err == nil || !strings.HasPrefix(err.Error(), "status 409:") || !strings.Contains(err.Error(), "already-on-slot") {
		t.Errorf("err = %v, want the 409 with its body for the move offer", err)
	}
}
