// Tests for #1065: a speaker that stopped taking the Spotify stream made the
// engine race through a track every ~5 s (drain discarding at wire speed while
// kept hot), and the recall / auto-switch paths kept re-arming it until Spotify
// rate-limited the account.

package spotify

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDrainClock replaces the drain's clock with one that only moves when the
// drain sleeps, and returns the total time slept.
func fakeDrainClock(d *oggDrain) *time.Duration {
	t0 := time.Unix(1_700_000_000, 0)
	var slept time.Duration
	d.now = func() time.Time { return t0.Add(slept) }
	d.sleep = func(x time.Duration) { slept += x }
	return &slept
}

// noSinkDrain is the real drain step with no box attached and the engine kept
// hot, i.e. exactly the #1065 state.
func noSinkDrain(t *testing.T) (*Manager, *oggDrain, *time.Duration) {
	t.Helper()
	chain := newTestChain(t, 1<<40, nil)
	holdSeams(chain)
	m, d, _ := newDrainHarness(t, chain, 1<<20)
	m.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	m.sink = nil
	return m, d, fakeDrainClock(d)
}

// audioSpan is the playback time between the first and the last audio page.
func audioSpan(pages [][]byte) time.Duration {
	var first, last int64
	for _, p := range pages {
		if g := oggGranule(p); g > 0 {
			if first == 0 {
				first = g
			}
			last = g
		}
	}
	return time.Duration(float64(last-first) / vorbisRate * float64(time.Second))
}

func TestNoSinkWaitDecision(t *testing.T) {
	sec := int64(vorbisRate)
	cases := []struct {
		name     string
		produced int64
		elapsed  time.Duration
		want     time.Duration
	}{
		{"on time", 5 * sec, 5 * time.Second, 0},
		{"inside the lead", 5 * sec, 4500 * time.Millisecond, 0},
		{"ahead by three seconds holds two", 8 * sec, 5 * time.Second, 2 * time.Second},
		{"slightly behind discards now", 3 * sec, 4 * time.Second, 0},
		{"far behind re-anchors", 1 * sec, 10 * time.Second, -1},
	}
	for _, c := range cases {
		if got := noSinkWait(c.produced, c.elapsed); got != c.want {
			t.Errorf("%s: noSinkWait(%d, %v) = %v, want %v", c.name, c.produced, c.elapsed, got, c.want)
		}
	}
}

// The core of the fix: with no box attached and the engine kept hot, a track
// takes as long to discard as it takes to play, not the ~5 s of #1065.
func TestDrainDiscardsNoFasterThanRealtimeWithNoBoxAttached(t *testing.T) {
	_, d, slept := noSinkDrain(t)
	track := synthVorbisTrack(1, 400, false)
	feed(d, track)

	want := audioSpan(track) - time.Duration(noSinkLeadSec*float64(time.Second))
	if want < 2*time.Second {
		t.Fatalf("synthetic track too short to prove anything (%v)", audioSpan(track))
	}
	if diff := *slept - want; diff < -50*time.Millisecond || diff > 50*time.Millisecond {
		t.Fatalf("discarding a %v track with no box took %v of waiting, want about %v",
			audioSpan(track), *slept, want)
	}
}

// A box attaching mid-wait gets the next page at once: the keep-hot reason
// (fast first audio after a hardware press flap) must survive the pacing.
func TestNoSinkPacingStopsTheMomentABoxAttaches(t *testing.T) {
	m, d, slept := noSinkDrain(t)
	sink := &fakeSink{}
	var sleeps int
	base := d.sleep
	d.sleep = func(x time.Duration) {
		sleeps++
		base(x)
		m.mu.Lock()
		m.sink = sink
		m.mu.Unlock()
	}
	track := synthVorbisTrack(1, 400, false)
	feed(d, track)
	if sleeps != 1 || *slept > noSinkPaceStep {
		t.Fatalf("the drain kept waiting after the box attached: %d sleeps, %v", sleeps, *slept)
	}
	if d.forwarded == 0 { // batched for the box (the harness flush size is large)
		t.Fatal("the attached box was handed nothing")
	}
}

// A skip or recall cut drops the old track's tail outright; with no box
// attached it may race that one track, but not the next one.
func TestNoSinkRacesOnlyTheTrackACutIsWaitingOn(t *testing.T) {
	m, d, slept := noSinkDrain(t)
	old := synthVorbisTrack(1, 400, false)
	feed(d, old[:len(old)/2])
	before := *slept
	m.ArmRecallCut()
	feed(d, old[len(old)/2:])
	if *slept != before {
		t.Fatalf("the old track's tail was paced (%v) although a cut is waiting for the next track", *slept-before)
	}
	next := synthVorbisTrack(2, 400, false)
	feed(d, next)
	if got, want := *slept-before, audioSpan(next)-2*time.Second; got < want {
		t.Fatalf("the track after the cut raced too: %v of waiting for a %v track", got, audioSpan(next))
	}
}

// The no-sink BOS line is bounded; with a box attached every boundary is logged.
func TestBOSLineIsRateLimitedWithNoBoxAttached(t *testing.T) {
	_, d, _ := noSinkDrain(t)
	rec := &recHandler{}
	d.m.logger = slog.New(rec)
	for i := uint32(1); i <= 10; i++ {
		feed(d, synthVorbisTrack(i, 6, false))
	}
	n := 0
	rec.mu.Lock()
	for _, r := range rec.recs {
		if r.Message == "spotify: track boundary (BOS)" {
			n++
		}
	}
	rec.mu.Unlock()
	// Ten short tracks take a few seconds of paced time: one line, not ten.
	if n != 1 {
		t.Fatalf("%d boundary lines for 10 tracks with no box attached, want 1", n)
	}
}

// guardManager is a manager with the auto-switch callback counted.
func guardManager(t *testing.T) (*Manager, *atomic.Int32) {
	t.Helper()
	m := newStallTestManager()
	var fired atomic.Int32
	m.onActivate = func(context.Context) { fired.Add(1) }
	return m, &fired
}

// age moves the guard's and the debounces' clocks back by d, so the next call
// is a separate activation.
func age(m *Manager, d time.Duration) {
	m.mu.Lock()
	m.idleActivationAt = m.idleActivationAt.Add(-d)
	m.lastActivate = m.lastActivate.Add(-d)
	m.engineHotUntil = time.Time{}
	m.mu.Unlock()
}

func TestRecallKeepAliveStopsAfterTwoAttemptsWithoutAudio(t *testing.T) {
	m, _ := guardManager(t)
	for i := 1; i <= 2; i++ {
		m.SetRecalling()
		if !m.engineHot() {
			t.Fatalf("attempt %d did not keep the engine hot", i)
		}
		age(m, 10*time.Second)
	}
	m.SetRecalling()
	if m.engineHot() {
		t.Fatal("the third attempt without any audio delivered kept the engine hot again")
	}
	if !m.recalling() {
		t.Fatal("the recall mark itself must still be set")
	}
}

func TestOneRecallCallingSetRecallingTwiceCountsOnce(t *testing.T) {
	m, _ := guardManager(t)
	m.SetRecalling() // web UI
	m.SetRecalling() // Play, moments later
	age(m, 10*time.Second)
	m.SetRecalling() // a second, separate recall
	if !m.engineHot() {
		t.Fatal("two recalls (one of them marked twice) already tripped the guard")
	}
}

// #1947: a key Spotify refused (lapsed Free account) marked twice, once from
// the web UI and once from Play after the account switch. The Premium key
// pressed next was the third attempt and lost its keep-alive.
func TestARefusedRecallDoesNotCountAgainstTheSpeaker(t *testing.T) {
	m, _ := guardManager(t)
	m.SetRecalling() // web UI, refused Free key
	age(m, 9*time.Second)
	m.SetRecalling() // Play, after the account switch
	m.forgiveRefusedRecall()
	age(m, 15*time.Second)
	m.SetRecalling() // the Premium key, web UI
	if !m.engineHot() {
		t.Fatal("the Premium key after a refused one lost its keep-alive")
	}
	age(m, 7*time.Second)
	m.SetRecalling() // its Play, after switching back
	if !m.engineHot() {
		t.Fatal("the Premium key's own second mark tripped the guard")
	}
}

func TestAutoSwitchStandsDownAfterTwoAttemptsWithoutAudio(t *testing.T) {
	m, fired := guardManager(t)
	for i := 0; i < 5; i++ {
		m.maybeActivate()
		age(m, 10*time.Second)
	}
	time.Sleep(50 * time.Millisecond) // the callback runs in a goroutine
	if got := fired.Load(); got != idleGuardMax {
		t.Fatalf("auto-switch fired %d times for a speaker that never took audio, want %d", got, idleGuardMax)
	}
}

func TestGuardWarnsOncePerTrip(t *testing.T) {
	m, _ := guardManager(t)
	rec := &recHandler{}
	m.logger = slog.New(rec)
	for i := 0; i < 6; i++ {
		m.SetRecalling()
		m.maybeActivate()
		age(m, 10*time.Second)
	}
	n := 0
	rec.mu.Lock()
	for _, r := range rec.recs {
		if r.Level == slog.LevelWarn {
			n++
		}
	}
	rec.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d WARN lines for one trip, want 1", n)
	}
}

// Audio reaching a box resets the guard; a bare attach that receives nothing
// (the #1065 speaker's forwardedKB=0 attaches) does not.
func TestGuardResetsWhenTheBoxReceivesAudio(t *testing.T) {
	m, _ := guardManager(t)
	for i := 0; i < 3; i++ {
		m.SetRecalling()
		age(m, 10*time.Second)
	}
	sink := &fakeSink{}
	m.mu.Lock()
	m.sink = sink
	m.mu.Unlock()
	track := synthVorbisTrack(1, 4, false)
	m.forward(sink, track[0]) // header page only: no audio yet
	m.SetRecalling()
	if m.engineHot() {
		t.Fatal("a header-only attach lifted the guard")
	}
	age(m, 10*time.Second)
	m.forward(sink, track[len(track)-1]) // first audio page
	m.SetRecalling()
	if !m.engineHot() {
		t.Fatal("the guard stayed closed after the box received audio")
	}
}

func TestGuardResetsAfterQuietMinutes(t *testing.T) {
	m, _ := guardManager(t)
	for i := 0; i < 3; i++ {
		m.SetRecalling()
		age(m, 10*time.Second)
	}
	age(m, idleGuardQuiet)
	m.SetRecalling()
	if !m.engineHot() {
		t.Fatal("the guard stayed closed after two quiet minutes")
	}
}
