package main

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// When a dropped station counts against the speaker, and when it does not.
//
// The latch exists for a real fault: some speakers accept a native station and
// then abandon it within seconds, and moving their presets to the slower UPnP
// form is the right answer there. It is the wrong answer when the speaker never
// got any audio to keep. Then the box gave up waiting on a fetch that never
// answered, the fault is upstream of the speaker, and the downgrade buys nothing
// while costing it its native radio source.
//
// Measured on a SoundTouch 20 on 2026-09-27, whose own resolver list made every
// name lookup take ten seconds: about fifty attempts, not one upstream response,
// and four slots permanently downgraded on the strength of it.

func quietNativeLogger(t *testing.T) func() {
	t.Helper()
	prev := nativeReadyLogger
	nativeReadyLogger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return func() { nativeReadyLogger = prev }
}

// stubDelivery makes nothingDelivered answer a fixed way for one test.
func stubDelivery(t *testing.T, since time.Duration, ok bool) {
	t.Helper()
	prev := nothingDelivered
	nothingDelivered = func() (time.Duration, bool) { return since, ok }
	t.Cleanup(func() { nothingDelivered = prev })
}

// stubNoSlotMiss takes the other exemption out of the picture, so each test is
// about the one thing it names.
func stubNoSlotMiss(t *testing.T) {
	t.Helper()
	prev := lastSlotMiss
	lastSlotMiss = func() (int, time.Duration, bool) { return 0, 0, false }
	t.Cleanup(func() { lastSlotMiss = prev })
}

func resetLatchState(t *testing.T) {
	t.Helper()
	nativeDrops.Lock()
	nativeDrops.at = nil
	nativeDrops.Unlock()
	t.Cleanup(func() {
		nativeDrops.Lock()
		nativeDrops.at = nil
		nativeDrops.Unlock()
	})
}

func TestDropsWithNoAudioEverDeliveredDoNotLatchTheSpeaker(t *testing.T) {
	defer quietNativeLogger(t)()
	stubNoSlotMiss(t)
	resetLatchState(t)
	stubDelivery(t, 42*time.Second, true) // a station in play, nothing arrived

	// Well past the budget. Every one of these is the box giving up on silence.
	for i := 0; i < nativeDropBudget+6; i++ {
		noteNativeStreamDropped()
	}

	nativeDrops.Lock()
	n := len(nativeDrops.at)
	nativeDrops.Unlock()
	if n != 0 {
		t.Fatalf("%d drops were counted against a speaker that was never given any audio", n)
	}
}

func TestDropsOfAudioThatDidArriveStillLatch(t *testing.T) {
	// The fault the latch was written for has to keep working, or this guard
	// has simply disabled it.
	defer quietNativeLogger(t)()
	stubNoSlotMiss(t)
	resetLatchState(t)
	stubDelivery(t, 0, false) // audio is flowing; a drop is the speaker's doing

	for i := 0; i < nativeDropBudget; i++ {
		noteNativeStreamDropped()
	}

	nativeDrops.Lock()
	n := len(nativeDrops.at)
	nativeDrops.Unlock()
	if n < nativeDropBudget {
		t.Fatalf("only %d of %d drops were counted; the latch no longer sees the fault it exists for", n, nativeDropBudget)
	}
}
