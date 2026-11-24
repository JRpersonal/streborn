package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newPlayModeTestApp() *App {
	a := newTestApp()
	a.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return a
}

// TestSpotifyPlayModePostsTheAction pins the player's Spotify shuffle and
// repeat buttons (#2058) to the agent endpoint: a POST carrying the action,
// and the state after the step handed back as the agent sent it.
func TestSpotifyPlayModePostsTheAction(t *testing.T) {
	for _, action := range []string{"shuffle", "repeat"} {
		var gotMethod, gotPath string
		var got map[string]string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod, gotPath = r.Method, r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(`{"ok":true,"shuffle":true,"repeat":"context"}`))
		}))
		out, err := newPlayModeTestApp().SpotifyPlayMode("127.0.0.1", listenPort(t, srv), action)
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if gotMethod != http.MethodPost || gotPath != "/api/spotify/playmode" || got["action"] != action {
			t.Errorf("%s: request = %s %s %v", action, gotMethod, gotPath, got)
		}
		if out["shuffle"] != true || out["repeat"] != "context" {
			t.Errorf("%s: answer = %v", action, out)
		}
	}
}

// An agent older than the endpoint answers with its phone page (200, HTML):
// that must read as "not available", never as a press that worked.
func TestSpotifyPlayModeOnAnOlderAgent(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"index page": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<!doctype html><html></html>"))
		},
		"404": func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) },
	} {
		srv := httptest.NewServer(h)
		_, err := newPlayModeTestApp().SpotifyPlayMode("127.0.0.1", listenPort(t, srv), "shuffle")
		srv.Close()
		if err == nil || err.Error() != spotifyPlayModeUnsupported {
			t.Errorf("%s: err = %v, want %s", name, err, spotifyPlayModeUnsupported)
		}
	}
}

func TestSpotifyPlayModeSurfacesTheRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"Spotify on STR is not what the speaker plays","reason":"not-playing"}`))
	}))
	defer srv.Close()
	_, err := newPlayModeTestApp().SpotifyPlayMode("127.0.0.1", listenPort(t, srv), "repeat")
	if err == nil || !strings.Contains(err.Error(), "not what the speaker plays") {
		t.Fatalf("err = %v, want the agent's sentence", err)
	}
}

func TestSpotifyPlayModeRejectsAnUnknownAction(t *testing.T) {
	if _, err := newPlayModeTestApp().SpotifyPlayMode("127.0.0.1", 1, "sleep"); err == nil {
		t.Fatal("an unknown action reached the speaker")
	}
}

// SpotifyNowPlaying carries the engine's play modes from /spotify/info, and
// none from an agent that does not send them.
func TestSpotifyNowPlayingCarriesThePlayModes(t *testing.T) {
	for body, want := range map[string]*SpotifyPlayModes{
		`{"track":"A","playModes":{"shuffle":true,"repeat":"track"}}`: {Shuffle: true, Repeat: "track"},
		`{"track":"A"}`: nil,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		np := newPlayModeTestApp().SpotifyNowPlaying("127.0.0.1", listenPort(t, srv))
		srv.Close()
		if (np.PlayModes == nil) != (want == nil) || (want != nil && *np.PlayModes != *want) {
			t.Errorf("%s: playModes = %+v, want %+v", body, np.PlayModes, want)
		}
	}
}
