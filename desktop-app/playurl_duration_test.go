package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// A single library track started from the desktop app drew no progress bar, in
// the app AND on the phone page, while the same track inside a folder drew one.
// Neither UI was at fault: the firmware answers `<time total="0">` unless the
// DIDL it was handed carries a duration, and both UIs only read back what the
// box reports. The folder path sent duration_sec; App.PlayURL never did (#845).
//
// The test asserts the field reaches the agent, because that is the whole bug:
// the value was in hand on the desktop side the entire time.

type playBox struct {
	mu     sync.Mutex
	srv    *httptest.Server
	bodies []map[string]any
}

func newPlayBox(t *testing.T) *playBox {
	t.Helper()
	b := &playBox{}
	b.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// playPost probes the agent first and refuses with box_not_ready
		// without this, so the mock has to look like a live agent.
		if r.URL.Path == "/api/agent/version" {
			_, _ = w.Write([]byte(`{"version":"v0.9.84"}`))
			return
		}
		if r.URL.Path == "/api/play" {
			raw, _ := io.ReadAll(r.Body)
			var got map[string]any
			_ = json.Unmarshal(raw, &got)
			b.mu.Lock()
			b.bodies = append(b.bodies, got)
			b.mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	b.srv.Listener.Close()
	b.srv.Listener = l
	b.srv.Start()
	t.Cleanup(b.srv.Close)
	return b
}

func (b *playBox) last(t *testing.T) map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.bodies) == 0 {
		t.Fatal("the app never posted to /api/play")
	}
	return b.bodies[len(b.bodies)-1]
}

func TestPlayURLCarriesTheTrackLength(t *testing.T) {
	box := newPlayBox(t)
	a := copyApp(t)
	err := a.PlayURL("127.0.0.1", listenPort(t, box.srv),
		"http://nas.example/Song.flac", "Song", "", "", "audio/flac", "", "", 247)
	if err != nil {
		t.Fatalf("PlayURL: %v", err)
	}
	got := box.last(t)
	d, ok := got["duration_sec"]
	if !ok {
		t.Fatal("duration_sec missing: the box then answers total=0 and neither UI can draw a bar or see the track end")
	}
	if n, _ := d.(float64); int(n) != 247 {
		t.Errorf("duration_sec = %v, want 247", d)
	}
}

// Radio has no length, and that is the honest value rather than a gap in the
// wiring: the agent treats 0 as "unknown" and the bar correctly stays away.
func TestPlayURLSendsZeroForSomethingWithNoLength(t *testing.T) {
	box := newPlayBox(t)
	a := copyApp(t)
	err := a.PlayURL("127.0.0.1", listenPort(t, box.srv),
		"http://stream.example/rp", "Radio Paradise", "", "uuid-1", "", "https://example/rp", "AAC+", 0)
	if err != nil {
		t.Fatalf("PlayURL: %v", err)
	}
	got := box.last(t)
	if d, ok := got["duration_sec"]; !ok || int(d.(float64)) != 0 {
		t.Errorf("duration_sec = %v (present=%v), want 0", d, ok)
	}
	// The fields that were already right must not have moved when the body
	// changed from map[string]string to map[string]any.
	for k, want := range map[string]string{
		"url":      "http://stream.example/rp",
		"title":    "Radio Paradise",
		"uuid":     "uuid-1",
		"homepage": "https://example/rp",
		"codec":    "AAC+",
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %q", k, got[k], want)
		}
	}
}
