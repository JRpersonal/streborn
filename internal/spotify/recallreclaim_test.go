package spotify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// transferEngine loses its session on the first load the way the Portable did
// on 2026-10-16: the load lands, then Spotify moves the account's playback to
// another device and every resume does nothing. A second /player/play takes
// the playback back.
type transferEngine struct {
	mu        sync.Mutex
	m         *Manager
	plays     int
	lost      bool
	paused    bool
	track     string
	lostAgain bool // every load loses the session
}

func (e *transferEngine) handler(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch r.URL.Path {
	case "/status":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"username": "u", "paused": e.paused, "buffering": false, "shuffle_context": false,
			"track": map[string]string{"uri": "spotify:track:" + e.track, "name": e.track},
		})
		return
	case "/player/play":
		e.plays++
		e.track, e.paused = "New", true
		e.m.noteLibrespotLine(`level=info msg="loaded track \"New\" (paused: true, position: 0ms, duration: 200000ms, prefetched: false)"`)
		if e.plays == 1 || e.lostAgain {
			e.lost = true
			e.m.noteLibrespotLine(`level=info msg="playback was transferred to <unknown>"`)
		} else {
			e.lost = false
		}
	case "/player/pause":
		e.paused = true
	case "/player/resume":
		if !e.lost {
			e.paused = false
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (e *transferEngine) snapshot() (plays int, paused bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.plays, e.paused
}

func fastReclaim(t *testing.T) {
	t.Helper()
	fastConfirm(t)
	s := recallReclaimSettle
	recallReclaimSettle = 50 * time.Millisecond
	t.Cleanup(func() { recallReclaimSettle = s })
}

// A recall whose session Spotify moved away while it started takes it back
// once, and then plays.
func TestRecallTakesBackATransferredSession(t *testing.T) {
	fastReclaim(t)
	e := &transferEngine{track: "Old"}
	ts := httptest.NewServer(http.HandlerFunc(e.handler))
	defer ts.Close()
	m := newTestManagerAt(t, ts.URL)
	e.m = m
	if err := m.Play(context.Background(), "spotify:playlist:abc", PlayOptions{}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if plays, paused := e.snapshot(); plays == 2 && !paused {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	plays, paused := e.snapshot()
	t.Fatalf("plays=%d paused=%v, want the recall repeated once and playing", plays, paused)
}

// Only one repeat: when the session is taken away again, the recall gives up
// instead of fighting the other device forever.
func TestRecallReclaimIsBounded(t *testing.T) {
	fastReclaim(t)
	e := &transferEngine{track: "Old", lostAgain: true}
	ts := httptest.NewServer(http.HandlerFunc(e.handler))
	defer ts.Close()
	m := newTestManagerAt(t, ts.URL)
	e.m = m
	if err := m.Play(context.Background(), "spotify:playlist:abc", PlayOptions{}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	time.Sleep(recallConfirmWindow + time.Second)
	if plays, _ := e.snapshot(); plays != 2 {
		t.Fatalf("plays=%d, want the original and exactly one repeat", plays)
	}
}
