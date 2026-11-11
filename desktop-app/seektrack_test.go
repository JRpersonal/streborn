package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Discussion #1207: the app's side of a click on the progress bar. The agent
// decides what can be positioned; the app has to hand it the second, bring a
// refusal back as an error, and pass on the agent's "seekable" untouched.

func TestSeekTrackPostsThePosition(t *testing.T) {
	var method, path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		method, path, body = r.Method, r.URL.Path, string(raw)
		_, _ = w.Write([]byte(`{"ok":true,"positionSec":90,"durationSec":240}`))
	}))
	defer srv.Close()

	if err := newTestApp().SeekTrack("127.0.0.1", listenPort(t, srv), 90); err != nil {
		t.Fatalf("SeekTrack: %v", err)
	}
	if method != http.MethodPost || path != "/api/seek" || body != `{"positionSec":90}` {
		t.Fatalf("the agent got %s %s %s, want POST /api/seek {\"positionSec\":90}", method, path, body)
	}
}

func TestSeekTrackReturnsTheAgentsRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"what is playing is not a music-library file","reason":"not-seekable"}`))
	}))
	defer srv.Close()

	err := newTestApp().SeekTrack("127.0.0.1", listenPort(t, srv), 30)
	if err == nil || !strings.Contains(err.Error(), "not-seekable") {
		t.Fatalf("SeekTrack = %v, want the agent's refusal", err)
	}
}

func TestSeekTrackRefusesANegativePositionWithoutCalling(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()

	if err := newTestApp().SeekTrack("127.0.0.1", listenPort(t, srv), -1); err == nil {
		t.Fatal("a negative position was accepted")
	}
	if called {
		t.Fatal("the speaker was asked to jump to a negative position")
	}
}

func TestTrackPositionCarriesSeekable(t *testing.T) {
	answer := `{"ok":true,"positionSec":12,"durationSec":240,"seekable":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	a := newTestApp()
	port := listenPort(t, srv)

	if got := a.TrackPosition("127.0.0.1", port); got != (TrackPos{PositionSec: 12, DurationSec: 240, Seekable: true}) {
		t.Fatalf("TrackPosition = %+v, want seekable", got)
	}
	// An agent that predates the field: never seekable, so the bar stays inert.
	answer = `{"ok":true,"positionSec":12,"durationSec":240}`
	if got := a.TrackPosition("127.0.0.1", port); got.Seekable {
		t.Fatalf("TrackPosition = %+v from an agent without the field, want not seekable", got)
	}
}
