package spotify

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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
	paths     []string
}

func (e *transferEngine) handler(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r.URL.Path != "/status" {
		e.paths = append(e.paths, time.Now().Format("05.000")+" "+r.URL.Path)
	}
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
		// A real load takes seconds; without a gap the commit can share the
		// play request's clock tick on Windows and never count as after it.
		waitForClockTick()
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
	var logBuf syncBuf
	m.logger = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.m = m
	if err := m.Play(context.Background(), "spotify:playlist:abc", PlayOptions{}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	defer func() {
		if t.Failed() {
			t.Log(logBuf.String())
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if plays, paused := e.snapshot(); plays == 2 && !paused {
			// The repeat is a Play running on the resume check's goroutine, and
			// it is still on its last steps when the engine starts playing:
			// returning now let it read the confirm timing while this test's
			// cleanup put the production values back (-race). Its closing log
			// line comes after the last of those reads.
			for time.Now().Before(deadline) && strings.Count(logBuf.String(), "spotify: recall play") < 2 {
				time.Sleep(10 * time.Millisecond)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	plays, paused := e.snapshot()
	e.mu.Lock()
	t.Log(e.paths)
	e.mu.Unlock()
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

// syncBuf is a goroutine-safe log sink for the reclaim tests.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
