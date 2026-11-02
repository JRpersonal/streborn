package spotify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePlayModeEngine answers /status with a position and records every POST
// with its body.
type fakePlayModeEngine struct {
	mu    sync.Mutex
	posMs int64
	noPos bool
	posts []string
}

func (f *fakePlayModeEngine) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet && r.URL.Path == "/status" {
		st := map[string]any{}
		if !f.noPos {
			st["track"] = map[string]any{"uri": "spotify:track:x", "position": f.posMs}
		}
		_ = json.NewEncoder(w).Encode(st)
		return
	}
	b, _ := io.ReadAll(r.Body)
	f.posts = append(f.posts, r.URL.Path+" "+strings.TrimSpace(string(b)))
}

func (f *fakePlayModeEngine) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.posts...)
}

func newFakePlayModeManager(t *testing.T, f *fakePlayModeEngine) *Manager {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(ts.Close)
	m := newTestManagerAt(t, ts.URL)
	m.client = ts.Client()
	return m
}

func TestRemotePrevGoesBackOnWhatTheRoomHears(t *testing.T) {
	for _, c := range []struct {
		pos, lag time.Duration
		back     bool
	}{
		// The engine is ten seconds of lead ahead: 15 s produced is 5 s heard.
		{15 * time.Second, 10 * time.Second, true},
		{2 * time.Second, 10 * time.Second, true},
		{19900 * time.Millisecond, 10 * time.Second, true},
		{20 * time.Second, 10 * time.Second, false},
		{45 * time.Second, 10 * time.Second, false},
		// A box that plays nearly live: the window is the song itself.
		{9 * time.Second, 0, true},
		{11 * time.Second, 0, false},
	} {
		if got := remotePrevGoesBack(c.pos, c.lag); got != c.back {
			t.Errorf("pos %v lag %v: back = %v, want %v", c.pos, c.lag, got, c.back)
		}
	}
}

func TestHeardLagFallsBackToTheLeadCap(t *testing.T) {
	m := newTestManagerAt(t, "http://127.0.0.1:1")
	if lag, measured := m.heardLag(); measured || lag != remotePrevDefaultLag {
		t.Fatalf("no reading: lag %v measured %v, want the lead cap", lag, measured)
	}
	m.mu.Lock()
	m.lastLag = boxLagMeasurement{At: time.Now(), DiffSec: 7.5}
	m.mu.Unlock()
	if lag, measured := m.heardLag(); !measured || lag != 7500*time.Millisecond {
		t.Fatalf("a reading of 7.5 s: lag %v measured %v", lag, measured)
	}
	m.mu.Lock()
	m.lastLag = boxLagMeasurement{At: time.Now(), DiffSec: 400}
	m.mu.Unlock()
	if lag, measured := m.heardLag(); measured || lag != remotePrevDefaultLag {
		t.Fatalf("an unbelievable reading must not be used: lag %v measured %v", lag, measured)
	}
}

func TestRemotePrevEarlyGoesToTheSongBefore(t *testing.T) {
	f := &fakePlayModeEngine{posMs: 14000} // 4 s heard behind the 10 s lead
	m := newFakePlayModeManager(t, f)
	back, err := m.RemotePrev(context.Background())
	if err != nil || !back {
		t.Fatalf("back=%v err=%v, want the song before", back, err)
	}
	got := f.calls()
	if len(got) != 2 || got[0] != `/player/seek {"position":0}` || !strings.HasPrefix(got[1], "/player/prev") {
		t.Fatalf("engine calls = %q, want a seek to 0 then prev", got)
	}
}

func TestRemotePrevLateRestartsTheSong(t *testing.T) {
	f := &fakePlayModeEngine{posMs: 95000}
	m := newFakePlayModeManager(t, f)
	back, err := m.RemotePrev(context.Background())
	if err != nil || back {
		t.Fatalf("back=%v err=%v, want a restart", back, err)
	}
	if got := f.calls(); len(got) != 1 || !strings.HasPrefix(got[0], "/player/prev") {
		t.Fatalf("engine calls = %q, want one plain prev", got)
	}
}

func TestRemotePrevWithoutPositionLeavesItToTheEngine(t *testing.T) {
	f := &fakePlayModeEngine{noPos: true}
	m := newFakePlayModeManager(t, f)
	if back, _ := m.RemotePrev(context.Background()); back {
		t.Fatal("an unknown position must not seek")
	}
	if got := f.calls(); len(got) != 1 || !strings.HasPrefix(got[0], "/player/prev") {
		t.Fatalf("engine calls = %q, want one plain prev", got)
	}
}
