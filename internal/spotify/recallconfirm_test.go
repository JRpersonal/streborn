// Tests for the recall against an engine that loads off its player loop (the
// go-librespot upstream merge of 2026-09): /player/play answers before the
// track has landed, a resume can be lost to a late paused load, and a shuffle
// toggle sent too early is dropped (ST30, 2026-10-10: a silent preset switch).

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

// asyncEngine is a fake go-librespot with an asynchronous track load. While a
// load runs, /status reports buffering and still names the previous track.
type asyncEngine struct {
	mu        sync.Mutex
	loadDelay time.Duration
	landsAt   time.Time
	loading   bool
	paused    bool
	shuffle   bool
	track     string
	// resumeLostWhileLoading: a resume during a load is forgotten when the
	// load commits paused (the observed failure).
	resumeLostWhileLoading bool
	// lateLoadAfterResume: the first resume after a landed load is followed
	// by one more paused load commit (the ST30 sequence: "loaded ... paused:
	// true" 3.7 s after the resume).
	lateLoadAfterResume bool
	lateLoadDone        bool
	// dropShuffleToggle: the first shuffle toggle is reverted, as a toggle
	// superseded by a newer load is.
	dropShuffleToggle bool
	shuffleDropped    bool

	resumes   []time.Time
	landedAt  time.Time
	shuffleAt []time.Time
}

func (e *asyncEngine) settleLocked() {
	if e.loading && !time.Now().Before(e.landsAt) {
		e.loading = false
		e.track = "New"
		e.paused = true // the load was asked for paused
		e.landedAt = time.Now()
	}
}

func (e *asyncEngine) handler(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.settleLocked()
	switch r.URL.Path {
	case "/status":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"username":        "u",
			"paused":          e.paused,
			"buffering":       e.loading,
			"shuffle_context": e.shuffle,
			"track":           map[string]string{"uri": "spotify:track:" + e.track, "name": e.track},
		})
		return
	case "/player/play":
		e.loading = true
		e.landsAt = time.Now().Add(e.loadDelay)
	case "/player/pause":
		e.paused = true
	case "/player/resume":
		e.resumes = append(e.resumes, time.Now())
		if e.loading && e.resumeLostWhileLoading {
			break
		}
		e.paused = false
		if e.lateLoadAfterResume && !e.lateLoadDone {
			e.lateLoadDone = true
			e.loading = true
			e.landsAt = time.Now().Add(150 * time.Millisecond)
		}
	case "/player/shuffle_context":
		var body struct {
			Shuffle bool `json:"shuffle_context"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		e.shuffleAt = append(e.shuffleAt, time.Now())
		if e.dropShuffleToggle && !e.shuffleDropped && body.Shuffle != e.shuffle {
			e.shuffleDropped = true
			break
		}
		e.shuffle = body.Shuffle
	}
	w.WriteHeader(http.StatusOK)
}

func (e *asyncEngine) state() (paused bool, shuffle bool, resumes []time.Time, landedAt time.Time, shuffleAt []time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.settleLocked()
	return e.paused, e.shuffle, append([]time.Time(nil), e.resumes...), e.landedAt, append([]time.Time(nil), e.shuffleAt...)
}

func fastConfirm(t *testing.T) {
	t.Helper()
	w, p, s := recallConfirmWindow, recallConfirmPoll, recallResumeSettle
	recallConfirmWindow, recallConfirmPoll, recallResumeSettle = 3*time.Second, 50*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { recallConfirmWindow, recallConfirmPoll, recallResumeSettle = w, p, s })
}

func waitPlaying(t *testing.T, e *asyncEngine, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if paused, _, _, _, _ := e.state(); !paused {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the engine never played after the recall")
}

// The resume and the shuffle toggle must wait for the track load to land:
// /player/play answering is not the track being there.
func TestRecallWaitsForTheAsyncLoad(t *testing.T) {
	fastConfirm(t)
	e := &asyncEngine{loadDelay: 600 * time.Millisecond, track: "Old", shuffle: true, resumeLostWhileLoading: true}
	ts := httptest.NewServer(http.HandlerFunc(e.handler))
	defer ts.Close()
	m := newTestManagerAt(t, ts.URL)

	if err := m.Play(context.Background(), "spotify:playlist:abc", PlayOptions{}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	waitPlaying(t, e, 2*time.Second)
	_, shuffle, resumes, landedAt, shuffleAt := e.state()
	if len(resumes) != 1 {
		t.Fatalf("%d resumes, want exactly one sent after the load", len(resumes))
	}
	if landedAt.IsZero() || resumes[0].Before(landedAt) {
		t.Fatal("the resume was sent before the track load landed")
	}
	if len(shuffleAt) == 0 || shuffleAt[0].Before(landedAt) {
		t.Fatal("the shuffle toggle was sent before the track load landed")
	}
	if shuffle {
		t.Fatal("a non-shuffle preset left the engine shuffled")
	}
}

// The ST30 sequence: a load commits paused after the resume. The recall must
// notice the engine is still paused and resume again.
func TestRecallResumesAgainAfterALatePausedLoad(t *testing.T) {
	fastConfirm(t)
	e := &asyncEngine{loadDelay: 100 * time.Millisecond, track: "Old", lateLoadAfterResume: true}
	ts := httptest.NewServer(http.HandlerFunc(e.handler))
	defer ts.Close()
	m := newTestManagerAt(t, ts.URL)

	if err := m.Play(context.Background(), "spotify:playlist:abc", PlayOptions{}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	// Right after Play the late load has re-paused the engine; the confirmation
	// must bring it back.
	time.Sleep(300 * time.Millisecond)
	waitPlaying(t, e, 2*time.Second)
	_, _, resumes, _, _ := e.state()
	if len(resumes) < 2 || len(resumes) > 1+recallResumeRetries {
		t.Fatalf("%d resumes, want the original plus 1..%d retries", len(resumes), recallResumeRetries)
	}
}

// Retries are bounded: an engine that never plays gets the original resume
// plus recallResumeRetries, no more.
func TestRecallResumeRetriesAreBounded(t *testing.T) {
	fastConfirm(t)
	stuck := &stuckEngine{}
	ts := httptest.NewServer(http.HandlerFunc(stuck.handler))
	defer ts.Close()
	m := newTestManagerAt(t, ts.URL)
	if err := m.Play(context.Background(), "spotify:playlist:abc", PlayOptions{}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	time.Sleep(recallConfirmWindow + 500*time.Millisecond)
	stuck.mu.Lock()
	n := stuck.resumes
	stuck.mu.Unlock()
	if n != 1+recallResumeRetries {
		t.Fatalf("%d resumes, want %d", n, 1+recallResumeRetries)
	}
}

type stuckEngine struct {
	mu      sync.Mutex
	resumes int
}

func (s *stuckEngine) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == "/status" {
		_, _ = w.Write([]byte(`{"username":"u","paused":true,"buffering":false,"track":{"uri":"spotify:track:x","name":"X"}}`))
		return
	}
	if r.URL.Path == "/player/resume" {
		s.resumes++
	}
	w.WriteHeader(http.StatusOK)
}

// A shuffle toggle the engine dropped is set again, so a non-shuffle preset
// does not keep the previous preset's shuffle.
func TestRecallReappliesADroppedShuffle(t *testing.T) {
	fastConfirm(t)
	e := &asyncEngine{loadDelay: 50 * time.Millisecond, track: "Old", shuffle: true, dropShuffleToggle: true}
	ts := httptest.NewServer(http.HandlerFunc(e.handler))
	defer ts.Close()
	m := newTestManagerAt(t, ts.URL)
	if err := m.Play(context.Background(), "spotify:playlist:abc", PlayOptions{}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, shuffle, _, _, _ := e.state(); !shuffle {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the dropped shuffle toggle was never set again")
}

// A load that is still running after the first wait is waited for, not
// mistaken for a missing resume track (which would replay from the top and
// throw the resume point away).
func TestSlowLoadIsNotASeekFailure(t *testing.T) {
	fastConfirm(t)
	old := recallSlowLoadWait
	recallSlowLoadWait = 3 * time.Second
	t.Cleanup(func() { recallSlowLoadWait = old })
	e := &asyncEngine{loadDelay: 5500 * time.Millisecond, track: "Old"}
	ts := httptest.NewServer(http.HandlerFunc(e.handler))
	defer ts.Close()
	m := newTestManagerAt(t, ts.URL)
	const ctxURI = "spotify:playlist:slow"
	m.resume.note(ctxURI, "spotify:track:here")
	if err := m.Play(context.Background(), ctxURI, PlayOptions{}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	if got := m.resume.trackFor(ctxURI); got != "spotify:track:here" {
		t.Fatalf("a slow load cost the resume point (now %q)", got)
	}
	waitPlaying(t, e, 2*time.Second)
}
