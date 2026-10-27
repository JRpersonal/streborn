package spotify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
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
	if err := m.AbandonRecall(context.Background()); err != nil {
		t.Fatalf("AbandonRecall: %v", err)
	}
	// Counted from the abandon on, not from Play's return: a resume the check
	// sent while the recall still stood is its job, and a test goroutine held
	// up for the 200 ms settle between Play and the abandon (a loaded machine)
	// used to count that one against the abandon.
	stuck.mu.Lock()
	before := stuck.resumes
	stuck.mu.Unlock()
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

// An abandon that lands while the resume check is already on its way to an
// extra resume must still leave the engine paused. The check's last look and
// its resume used to be two steps: an abandon in between had its pause
// overtaken by the resume, and the engine played to nobody. The seam opens
// exactly that gap and runs the abandon inside it.
func TestAnAbandonDuringTheExtraResumeStillEndsPaused(t *testing.T) {
	fastConfirm(t)
	stuck := &stuckEngine{}
	var mu sync.Mutex
	var sent []string
	var hookRan atomic.Bool
	pauseSeen := make(chan struct{}, 8)
	extraResumed := make(chan struct{}, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/player/pause", "/player/resume":
			mu.Lock()
			sent = append(sent, r.URL.Path)
			mu.Unlock()
			if r.URL.Path == "/player/pause" {
				pauseSeen <- struct{}{}
			} else if hookRan.Load() {
				select {
				case extraResumed <- struct{}{}:
				default:
				}
			}
		}
		stuck.handler(w, r)
	}))
	defer ts.Close()
	m := newTestManagerAt(t, ts.URL)
	stuck.m = m

	abandoned := make(chan error, 1)
	var once sync.Once
	m.beforeExtraResume = func() {
		once.Do(func() {
			hookRan.Store(true)
			// Play's own pause is in already; only a new one counts.
			for len(pauseSeen) > 0 {
				<-pauseSeen
			}
			go func() { abandoned <- m.AbandonRecall(context.Background()) }()
			// Give the abandon every chance to overtake: its pause either
			// arrives (the race is open) or cannot while the resume is owed.
			select {
			case <-pauseSeen:
			case <-time.After(300 * time.Millisecond):
			}
		})
	}
	if err := m.Play(context.Background(), "spotify:playlist:abc", PlayOptions{}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	// Both have to be in before the order means anything: the extra resume the
	// seam held up, and the abandon.
	for _, wait := range []func() error{
		func() error { <-extraResumed; return nil },
		func() error { return <-abandoned },
	} {
		done := make(chan error, 1)
		go func() { done <- wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("AbandonRecall: %v", err)
			}
		case <-time.After(10 * recallConfirmWindow):
			t.Fatal("the resume check never reached its extra resume")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) == 0 || sent[len(sent)-1] != "/player/pause" {
		t.Fatalf("the engine's last command was not the abandon's pause: %v", sent)
	}
}

// Nil-safe like the rest of the manager's entry points.
func TestAbandonRecallNilManager(t *testing.T) {
	var m *Manager
	if err := m.AbandonRecall(context.Background()); err != nil {
		t.Fatalf("nil manager: %v", err)
	}
}
