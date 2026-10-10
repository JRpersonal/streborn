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
	mu sync.Mutex
	// m receives the engine's "loaded track" log line when a load commits,
	// the way the agent reads go-librespot's stderr.
	m         *Manager
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
	// pauseClearsBuffering: a pause while a load runs acts on the PREVIOUS
	// stream, whose pause event clears the buffering flag (the ST30 soft
	// recall sequence).
	pauseClearsBuffering bool
	bufferingCleared     bool

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
		e.bufferingCleared = false
		if e.m != nil {
			e.m.noteLibrespotLine(`level=info msg="loaded track \"New\" (paused: true, position: 0ms, duration: 200000ms, prefetched: false)"`)
		}
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
			"buffering":       e.loading && !e.bufferingCleared,
			"shuffle_context": e.shuffle,
			"track":           map[string]string{"uri": "spotify:track:" + e.track, "name": e.track},
		})
		return
	case "/player/play":
		e.loading = true
		e.landsAt = time.Now().Add(e.loadDelay)
	case "/player/pause":
		e.paused = true
		if e.loading && e.pauseClearsBuffering {
			e.bufferingCleared = true
		}
	case "/player/resume":
		e.resumes = append(e.resumes, time.Now())
		if e.loading && e.resumeLostWhileLoading {
			break
		}
		if e.loading && e.bufferingCleared {
			// The resume woke the previous stream, which plays until the load
			// lands paused.
			e.bufferingCleared = false
			e.paused = false
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
	e.m = m

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
	e.m = m

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
	stuck.m = m
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
	m       *Manager
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
	if r.URL.Path == "/player/play" && s.m != nil {
		s.m.noteLibrespotLine(`level=info msg="loaded track \"X\" (paused: true, position: 0ms, duration: 200000ms, prefetched: false)"`)
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
	e.m = m
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
	e.m = m
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

// The soft recall route (webui -> PlayAccount -> Play) on the ST30, four
// switches in a row: the recall's pause hit the PREVIOUS stream while the
// load ran, which cleared the buffering flag, so the load looked finished;
// the resume woke the previous stream, and the load then landed paused. The
// recall must wait for the load to commit and leave the engine playing.
func TestSoftRecallSurvivesALatePausedLoad(t *testing.T) {
	fastConfirm(t)
	e := &asyncEngine{loadDelay: 500 * time.Millisecond, track: "Old", pauseClearsBuffering: true}
	ts := httptest.NewServer(http.HandlerFunc(e.handler))
	defer ts.Close()
	m := newTestManagerAt(t, ts.URL)
	e.m = m
	if err := m.PlayAccount(context.Background(), "spotify:playlist:slot3", "", PlayOptions{}); err != nil {
		t.Fatalf("PlayAccount: %v", err)
	}
	waitPlaying(t, e, 3*time.Second)
	_, _, resumes, landedAt, _ := e.state()
	if landedAt.IsZero() || len(resumes) == 0 || resumes[len(resumes)-1].Before(landedAt) {
		t.Fatal("no resume reached the engine after the recalled track landed")
	}
	// Stays playing: nothing pauses it again inside the check's window.
	time.Sleep(500 * time.Millisecond)
	if paused, _, _, _, _ := e.state(); paused {
		t.Fatal("the engine ended paused")
	}
}

// The same late paused load on the warm same-context fast path (a shuffle
// press loads the pick): the shared resume check covers it too.
func TestWarmRecallResumesALatePausedLoad(t *testing.T) {
	fastConfirm(t)
	e := &asyncEngine{track: "Old", paused: false}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/player/next" {
			// The pick loads in the background and commits paused, after the
			// fast path's resume.
			e.mu.Lock()
			e.loading = true
			e.landsAt = time.Now().Add(300 * time.Millisecond)
			e.resumeLostWhileLoading = true
			e.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		e.handler(w, r)
	}))
	defer ts.Close()
	m := newTestManagerAt(t, ts.URL)
	e.m = m
	const ctxURI = "spotify:playlist:warm"
	makeWarm(m, ctxURI)
	if err := m.Play(context.Background(), ctxURI, PlayOptions{Shuffle: true}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	waitPlaying(t, e, 3*time.Second)
}
