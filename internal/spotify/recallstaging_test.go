// Tests for the recall fixes of #1077 (ST10, v1.0.10): the staging gate that
// keeps a cold recall's paused preamble off the box, the stale resume point,
// and the recall window that outlived a fixed 8 s.

package spotify

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The field sequence: the old track plays, the recall arms its cut and closes
// the gate, the paused load emits its BOS and a short preamble, the resume
// starts the track over under a fresh BOS. The box must receive the old track
// and then the resumed track once, never the preamble.
func TestStagingDropsThePausedPreamble(t *testing.T) {
	chain := newTestChain(t, 1<<40, nil)
	m, d, fs := newDrainHarness(t, chain, 1)
	old := synthVorbisTrack(0x01, 6, false)
	feed(d, old)

	m.ArmRecallCut()
	gen := m.beginRecallStaging()
	preamble := synthVorbisTrack(0x02, 3, false)
	feed(d, preamble)
	if !m.skipCutArmed() {
		t.Fatal("the preamble's BOS consumed the recall cut")
	}
	if !bytes.Equal(fs.buf.Bytes(), concatPages(old)) {
		t.Fatal("preamble pages reached the box")
	}

	m.markRecallStagingResumed(gen)
	resumed := synthVorbisTrack(0x03, 4, false)
	feed(d, resumed)
	if m.recallStaging() {
		t.Error("the resume's BOS must open the gate")
	}
	if m.skipCutArmed() {
		t.Error("the resume's BOS must consume the recall cut")
	}
	want := append(concatPages(old), concatPages(resumed)...)
	if !bytes.Equal(fs.buf.Bytes(), want) {
		t.Fatalf("wire carries %d bytes, want the old track plus the resumed track (%d)", fs.buf.Len(), len(want))
	}
	streams := parseChain(t, fs.buf.Bytes())
	if len(streams) != 2 || streams[1].serial != 0x03 {
		t.Fatalf("want two logical streams ending with the resumed one, got %d", len(streams))
	}
}

// A BOS that arrives before the resume was sent (a replay from the top, the
// shuffle pick) is still part of the staging and must be dropped too.
func TestStagingDropsEveryBOSBeforeTheResume(t *testing.T) {
	chain := newTestChain(t, 1<<40, nil)
	m, d, fs := newDrainHarness(t, chain, 1)
	m.beginRecallStaging()
	feed(d, synthVorbisTrack(0x10, 2, false))
	feed(d, synthVorbisTrack(0x11, 2, false))
	if fs.buf.Len() != 0 {
		t.Fatalf("%d staged bytes reached the box", fs.buf.Len())
	}
	if !m.recallStaging() {
		t.Fatal("the gate opened before the resume")
	}
}

// Without a recall nothing changes: every page goes out as before.
func TestNoStagingLeavesPlaybackUntouched(t *testing.T) {
	chain := newTestChain(t, 1<<40, nil)
	_, d, fs := newDrainHarness(t, chain, 1)
	a := synthVorbisTrack(0x20, 5, false)
	b := synthVorbisTrack(0x21, 5, false)
	feed(d, a)
	feed(d, b)
	if !bytes.Equal(fs.buf.Bytes(), append(concatPages(a), concatPages(b)...)) {
		t.Fatal("pages were altered or dropped without a recall in flight")
	}
}

// The cold sequence from the Portable live test (v1.0.10 engine, playback
// stopped before the recall): the load is not paused, its BOS and the track
// flow BEFORE the resume, and the resume brings no fresh BOS. Not one byte of
// the track may be lost: the box gets the old track, then the whole new one.
func TestColdRecallKeepsTheRealStart(t *testing.T) {
	chain := newTestChain(t, 1<<40, nil)
	m, d, fs := newDrainHarness(t, chain, 1)
	old := synthVorbisTrack(0x01, 6, false)
	feed(d, old)

	m.ArmRecallCut()
	gen := m.beginRecallStaging()
	track := synthVorbisTrack(0x30, 60, false)
	h := headerPageCount(track)
	feed(d, track[:h+10]) // BOS, headers and audio, all before the resume
	if !bytes.Equal(fs.buf.Bytes(), concatPages(old)) {
		t.Fatal("pages reached the box before the gate could tell preamble from start")
	}
	m.markRecallStagingResumed(gen)
	m.mu.Lock()
	m.stagingResumedAt = time.Now().Add(-stagingResumeGrace - time.Second)
	m.mu.Unlock()
	feed(d, track[h+10:]) // the stream just goes on: no fresh BOS
	if m.recallStaging() {
		t.Fatal("the gate stayed closed")
	}
	if m.skipCutArmed() {
		t.Fatal("the held BOS must consume the recall cut when it goes out")
	}
	want := append(concatPages(old), concatPages(track)...)
	if !bytes.Equal(fs.buf.Bytes(), want) {
		t.Fatalf("wire carries %d bytes, want the old track plus the whole new track (%d)", fs.buf.Len(), len(want))
	}
	if s := parseChain(t, fs.buf.Bytes()); len(s) != 2 {
		t.Fatalf("wire parses into %d logical streams, want 2", len(s))
	}
}

// A gate opened by a failed resume (or a stop) while it holds a track start
// delivers that start when the stream goes on.
func TestStagingOpenedEarlyDeliversTheHeldStart(t *testing.T) {
	chain := newTestChain(t, 1<<40, nil)
	m, d, fs := newDrainHarness(t, chain, 1)
	gen := m.beginRecallStaging()
	track := synthVorbisTrack(0x40, 20, false)
	h := headerPageCount(track)
	feed(d, track[:h+3])
	m.endRecallStaging(gen)
	feed(d, track[h+3:])
	if !bytes.Equal(fs.buf.Bytes(), concatPages(track)) {
		t.Fatal("the held start was lost when the gate opened early")
	}
}

// The verdicts at the edges: a full buffer before the resume stops the
// reading, after the resume it is released at once, and the hard bound opens
// the gate for a recall that never reaches its resume at all.
func TestStagingVerdictEdges(t *testing.T) {
	m := newStallTestManager()
	gen := m.beginRecallStaging()
	if v := m.stagingVerdictFor(false, 0); v != stagingNone {
		t.Fatalf("old-track page before any BOS = %v, want the normal path", v)
	}
	if v := m.stagingVerdictFor(true, 0); v != stagingHold {
		t.Fatalf("BOS before the resume = %v, want hold", v)
	}
	if v := m.stagingVerdictFor(false, stagingHoldMax); v != stagingWait {
		t.Fatalf("full buffer before the resume = %v, want wait", v)
	}
	m.markRecallStagingResumed(gen)
	if v := m.stagingVerdictFor(false, 100); v != stagingHold {
		t.Fatalf("page inside the resume grace = %v, want hold", v)
	}
	if v := m.stagingVerdictFor(false, stagingHoldMax); v != stagingRelease {
		t.Fatalf("full buffer after the resume = %v, want release", v)
	}
	if m.recallStaging() {
		t.Fatal("a release must open the gate")
	}

	m.beginRecallStaging()
	m.mu.Lock()
	m.stagingUntil = time.Now().Add(-time.Second)
	m.mu.Unlock()
	if v := m.stagingVerdictFor(false, 100); v != stagingRelease {
		t.Fatalf("held pages past the hard bound = %v, want release", v)
	}
	m.beginRecallStaging()
	m.mu.Lock()
	m.stagingUntil = time.Now().Add(-time.Second)
	m.mu.Unlock()
	if v := m.stagingVerdictFor(true, 0); v != stagingNone || m.recallStaging() {
		t.Fatalf("BOS past the hard bound = %v (gate closed %v), want the normal path", v, m.recallStaging())
	}
}

// The drain stops reading while a full buffer waits for the resume, and
// releases it in order once the resume was sent.
func TestStagingFullBufferWaitsForTheResume(t *testing.T) {
	chain := newTestChain(t, 1<<40, nil)
	m, d, fs := newDrainHarness(t, chain, 1)
	gen := m.beginRecallStaging()
	track := synthVorbisTrack(0x50, 20, false)
	h := headerPageCount(track)
	feed(d, track[:h+2])
	d.stageBytes = stagingHoldMax // pretend the buffer is full
	done := make(chan struct{})
	go func() {
		feed(d, track[h+2:h+3])
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("the drain kept reading with a full buffer before the resume")
	case <-time.After(150 * time.Millisecond):
	}
	m.markRecallStagingResumed(gen)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the resume did not release the full buffer")
	}
	if !bytes.Equal(fs.buf.Bytes(), concatPages(track[:h+3])) {
		t.Fatal("the released start is not the held pages in order")
	}
}

// An older recall finishing must not open the gate a newer one closed; a user
// stop opens it whoever owns it.
func TestStagingGenerationsAndUserStop(t *testing.T) {
	m := newStallTestManager()
	first := m.beginRecallStaging()
	second := m.beginRecallStaging()
	m.endRecallStaging(first)
	m.markRecallStagingResumed(first)
	if !m.recallStaging() {
		t.Fatal("the older recall opened the newer recall's gate")
	}
	m.mu.Lock()
	resumedAt := m.stagingResumedAt
	m.mu.Unlock()
	if !resumedAt.IsZero() {
		t.Fatal("the older recall's resume was counted for the newer one")
	}
	m.endRecallStaging(second)
	if m.recallStaging() {
		t.Fatal("the owner could not open its gate")
	}
	m.beginRecallStaging()
	m.UserStopped(context.Background(), "test")
	if m.recallStaging() {
		t.Fatal("a user stop must open the gate")
	}
}

// Play's own exits: a failed /player/play opens the gate at once, a good one
// leaves it waiting for the resume's BOS with the resume marked.
func TestPlayOpensOrArmsTheGate(t *testing.T) {
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			_, _ = w.Write([]byte(`{"username":"u","track":{"uri":"spotify:track:cur","name":"Cur"}}`))
			return
		}
		if r.URL.Path == "/player/play" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer fail.Close()
	m := newTestManagerAt(t, fail.URL)
	if err := m.Play(context.Background(), "spotify:playlist:abc", PlayOptions{}); err == nil {
		t.Fatal("Play must surface the /player/play failure")
	}
	if m.recallStaging() {
		t.Error("a failed /player/play left the gate closed: permanent mute")
	}

	ok, _, cleanup := mockLibrespot(t)
	defer cleanup()
	if err := ok.Play(context.Background(), "spotify:playlist:abc", PlayOptions{}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	ok.mu.Lock()
	staging, resumedAt := ok.staging, ok.stagingResumedAt
	ok.mu.Unlock()
	if !staging || resumedAt.IsZero() {
		t.Fatalf("after a good recall the gate must wait for the resume's BOS (staging %v, resumed %v)", staging, !resumedAt.IsZero())
	}
}

// A resume point whose track left the context is forgotten after the
// replay from the top, so the next press does not pay for the failed seek.
func TestStaleResumePointIsForgotten(t *testing.T) {
	var m *Manager
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			_, _ = w.Write([]byte(`{"username":"u","track":{"uri":"spotify:track:cur","name":"Cur"}}`))
			return
		}
		if r.URL.Path == "/player/play" {
			b, _ := io.ReadAll(r.Body)
			if strings.Contains(string(b), "skip_to_uri") {
				m.mu.Lock()
				// Ahead of the play stamp even on a coarse (Windows) clock.
				m.lastSeekFailAt = time.Now().Add(10 * time.Millisecond)
				m.mu.Unlock()
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	m = newTestManagerAt(t, ts.URL)
	const ctxURI = "spotify:playlist:volatile"
	m.resume.note(ctxURI, "spotify:track:gone")
	if err := m.Play(context.Background(), ctxURI, PlayOptions{}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	if got := m.resume.trackFor(ctxURI); got != "" {
		t.Fatalf("stale resume point kept: %q", got)
	}
}

// forget only drops the point it was told about.
func TestResumeForgetKeepsANewerPoint(t *testing.T) {
	s := newResumeStore("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.note("spotify:playlist:a", "spotify:track:new")
	s.forget("spotify:playlist:a", "spotify:track:old")
	if s.trackFor("spotify:playlist:a") != "spotify:track:new" {
		t.Fatal("forget dropped a point it was not asked to drop")
	}
	s.forget("spotify:playlist:a", "spotify:track:new")
	if s.trackFor("spotify:playlist:a") != "" || len(s.order) != 0 || !s.dirty {
		t.Fatal("forget left the point behind or did not mark the store for a flush")
	}
	var nilStore *resumeStore
	nilStore.forget("spotify:playlist:a", "spotify:track:new") // must not panic
}

// The recall window stays open while Play runs, however long the engine takes
// (10.8 s in the field), and shrinks back to a short tail once it returns.
func TestRecallWindowHeldUntilTheRecallFinishes(t *testing.T) {
	var (
		mu       sync.Mutex
		m        *Manager
		heldLeft time.Duration
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			_, _ = w.Write([]byte(`{"username":"u","track":{"uri":"spotify:track:cur","name":"Cur"}}`))
			return
		}
		if r.URL.Path == "/player/play" {
			m.mu.Lock()
			left := time.Until(m.recallUntil)
			m.mu.Unlock()
			mu.Lock()
			heldLeft = left
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	m = newTestManagerAt(t, ts.URL)
	if err := m.Play(context.Background(), "spotify:playlist:abc", PlayOptions{}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if heldLeft < recallWindowMax-5*time.Second {
		t.Fatalf("recall window during the play POST = %v, want it held near %v", heldLeft, recallWindowMax)
	}
	left := time.Until(m.recallUntil)
	if left <= 0 || left > recallWindow {
		t.Fatalf("recall window after Play = %v, want a short tail within %v", left, recallWindow)
	}
}

// A slow recall's own track start, arriving past the old fixed 8 s, must not
// read as a Spotify-app skip while Play still holds the window.
func TestSlowRecallBoundaryIsNoAppSkip(t *testing.T) {
	m := newAppSkipTestManager()
	fired := make(chan struct{}, 1)
	m.SetOnActivate(func(context.Context) { fired <- struct{}{} })
	m.sink = io.Discard
	m.noteLibrespotLine(`level=info msg="loaded track \"a\" (duration: 200000ms, prefetched: true)"`)
	m.noteTrackBoundaryCut(0, 0)
	m.SetRecalling()
	m.holdRecallWindow()
	// loadAfterMs=10792 in the field: the window must still be open then.
	m.mu.Lock()
	until := m.recallUntil
	m.mu.Unlock()
	if until.Before(time.Now().Add(11 * time.Second)) {
		t.Fatalf("held window ends in %v, a 10.8 s recall outlives it", time.Until(until))
	}
	m.noteTrackBoundaryCut(60*vorbisRate, 4096)
	select {
	case <-fired:
		t.Fatal("a slow recall's own boundary re-pointed the box as an app skip")
	case <-time.After(50 * time.Millisecond):
	}
}

// A user stop during the recall stands: the recall's end does not reopen it.
func TestRecallWindowEndRespectsAUserStop(t *testing.T) {
	m := newStallTestManager()
	start := time.Now()
	m.SetRecalling()
	held := m.holdRecallWindow()
	m.UserStopped(context.Background(), "test")
	m.endRecallWindow(start, held)
	if m.recalling() {
		t.Fatal("the recall's end reopened a window the user stop closed")
	}
}
