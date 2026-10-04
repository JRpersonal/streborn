// stopintent_test.go: a Stop pressed inside a Spotify recall window must hold
// (fleet roll 2026-10-04: three speakers came back by themselves a few seconds
// after Stop, at the next track boundary).

package spotify

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newStopTestManager returns a Manager whose engine API is a local fake that
// counts /player/pause posts and answers them with pauseStatus.
func newStopTestManager(t *testing.T, pauseStatus int) (*Manager, *int32) {
	t.Helper()
	var pauses int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/player/pause" {
			atomic.AddInt32(&pauses, 1)
			w.WriteHeader(pauseStatus)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(api.Close)
	bin := filepath.Join(t.TempDir(), "engine")
	if err := os.WriteFile(bin, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(bin, filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.apiAddr = strings.TrimPrefix(api.URL, "http://")
	return m, &pauses
}

// waitPauses waits briefly for the asynchronous re-pause.
func waitPauses(p *int32, want int32) int32 {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(p) >= want {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return atomic.LoadInt32(p)
}

func TestUserStoppedEndsRecallWindowAndPausesEngine(t *testing.T) {
	m, pauses := newStopTestManager(t, http.StatusOK)
	m.SetRecalling()
	if !m.engineHot() {
		t.Fatal("precondition: a recall keeps the engine hot")
	}
	m.UserStopped(context.Background(), "stop")
	if m.engineHot() {
		t.Error("the recall window still keeps the engine running after the user's stop")
	}
	if m.recalling() {
		t.Error("the recall marker survived the user's stop")
	}
	if got := atomic.LoadInt32(pauses); got != 1 {
		t.Errorf("engine pauses = %d, want 1", got)
	}
	// The engine is paused, so a later "playing" is somebody starting it again.
	if !m.engineStartAllowed("playing") {
		t.Error("a play after a successful pause was held back")
	}
}

func TestUserStoppedLeavesAnIdleEngineAlone(t *testing.T) {
	m, pauses := newStopTestManager(t, http.StatusOK)
	m.UserStopped(context.Background(), "stop")
	if got := atomic.LoadInt32(pauses); got != 0 {
		t.Errorf("an idle engine was paused %d times", got)
	}
	// Spotify started from the app later must still take the speaker.
	if !m.engineStartAllowed("playing") {
		t.Error("an app play after a stop with an idle engine was held back")
	}
}

func TestEngineCarryingOnThroughTheStopIsHeld(t *testing.T) {
	// The pause does not land, exactly the case the latch is for.
	m, pauses := newStopTestManager(t, http.StatusInternalServerError)
	var activations int32
	m.SetOnActivate(func(context.Context) { atomic.AddInt32(&activations, 1) })
	m.SetRecalling()
	m.UserStopped(context.Background(), "stop")

	// Track boundary: the engine reports "playing" for the next song.
	if m.engineStartAllowed("playing") {
		t.Fatal("the engine's own next track was allowed to bring the speaker back")
	}
	// It tries to pause the engine once more, and only once per stop.
	if got := waitPauses(pauses, 2); got != 2 {
		t.Errorf("pauses after the held boundary = %d, want 2", got)
	}
	if m.engineStartAllowed("playing") {
		t.Fatal("a second boundary was allowed through")
	}
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(pauses); got != 2 {
		t.Errorf("the re-pause repeated: %d pauses", got)
	}
	if got := atomic.LoadInt32(&activations); got != 0 {
		t.Errorf("activations = %d, want 0", got)
	}
}

func TestRealPlayIntentsLiftTheLatch(t *testing.T) {
	cases := []struct {
		name string
		do   func(m *Manager) bool
	}{
		{"the Spotify app selects the speaker", func(m *Manager) bool {
			return m.engineStartAllowed("active")
		}},
		{"play after the engine reported a pause", func(m *Manager) bool {
			m.noteEngineEnded()
			return m.engineStartAllowed("playing")
		}},
		{"a recall", func(m *Manager) bool {
			m.SetRecalling()
			return m.engineStartAllowed("playing")
		}},
		{"the speaker fetches the stream again", func(m *Manager) bool {
			m.mu.Lock()
			m.noteSinkAttachedLocked(time.Now().Add(stopAttachGrace + time.Second))
			m.mu.Unlock()
			return m.engineStartAllowed("playing")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newStopTestManager(t, http.StatusInternalServerError)
			m.SetRecalling()
			m.UserStopped(context.Background(), "stop")
			if !tc.do(m) {
				t.Fatal("a real play intent was held back")
			}
			m.mu.Lock()
			armed := !m.userStopAt.IsZero()
			m.mu.Unlock()
			if armed {
				t.Error("the latch is still armed after a real play intent")
			}
		})
	}
}

func TestAFetchAlreadyUnderWayAtTheStopDoesNotLiftTheLatch(t *testing.T) {
	m, _ := newStopTestManager(t, http.StatusInternalServerError)
	m.SetRecalling()
	m.UserStopped(context.Background(), "stop")
	m.mu.Lock()
	m.noteSinkAttachedLocked(time.Now())
	m.mu.Unlock()
	if m.engineStartAllowed("playing") {
		t.Error("an attach right at the stop lifted the latch")
	}
}
