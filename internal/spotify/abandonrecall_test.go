package spotify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// A recall the speaker never took is abandoned with a pause. The recall's own
// resume check must not undo that pause: live Portable 2026-10-22, the give-up
// pause was followed 30 ms later by "resuming again", and the engine played
// the playlist to nobody while the same account's phone lost its playback.
func TestAbandonRecallStopsTheResumeCheck(t *testing.T) {
	fastConfirm(t)
	stuck := &stuckEngine{}
	var mu sync.Mutex
	pauses := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/player/pause" {
			mu.Lock()
			pauses++
			mu.Unlock()
		}
		stuck.handler(w, r)
	}))
	defer ts.Close()
	m := newTestManagerAt(t, ts.URL)
	stuck.m = m

	if err := m.Play(context.Background(), "spotify:playlist:abc", PlayOptions{}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	stuck.mu.Lock()
	before := stuck.resumes
	stuck.mu.Unlock()

	if err := m.AbandonRecall(context.Background()); err != nil {
		t.Fatalf("AbandonRecall: %v", err)
	}
	time.Sleep(recallConfirmWindow + 500*time.Millisecond)

	stuck.mu.Lock()
	after := stuck.resumes
	stuck.mu.Unlock()
	if after != before {
		t.Fatalf("the recall's resume check resumed the engine %d more time(s) after the recall was abandoned", after-before)
	}
	mu.Lock()
	defer mu.Unlock()
	if pauses == 0 {
		t.Fatal("abandoning the recall did not pause the engine")
	}
	if m.engineHot() {
		t.Fatal("an abandoned recall still keeps the engine hot for a speaker that is not listening")
	}
}

// Nil-safe like the rest of the manager's entry points.
func TestAbandonRecallNilManager(t *testing.T) {
	var m *Manager
	if err := m.AbandonRecall(context.Background()); err != nil {
		t.Fatalf("nil manager: %v", err)
	}
}
