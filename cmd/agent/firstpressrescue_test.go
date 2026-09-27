package main

import (
	"testing"
	"time"
)

// The measured case, from an ST30 on 2026-09-27: the owner pressed key 2 on a
// sleeping speaker, the firmware woke into the wrong state, threw the first
// fetch away, logged "Could not obtain first-frame" and then
// "PlaybackFailure ERROR_NO_DECODED_DATA" two seconds later, and only the
// owner's second press started the music.
func TestTheSecondPressIsTakenOverAfterAFailedFirstOne(t *testing.T) {
	var f firstPressRescue
	press := time.Now()
	f.noteNativeActivation(2, press)

	// The failure arrives 2.1 s later, as it did on the speaker.
	if got := f.claim(press.Add(2100 * time.Millisecond)); got != 2 {
		t.Fatalf("claim = %d, want the pressed key 2", got)
	}
	// ONE rescue per press. The firmware logs both a no-first-frame line and a
	// PlaybackFailure line for the same silence; two presses would start the
	// station twice.
	if got := f.claim(press.Add(2200 * time.Millisecond)); got != 0 {
		t.Fatalf("a second failure from the same press claimed another rescue (%d)", got)
	}
}

func TestAFailureThatIsNotAboutThePressIsLeftAlone(t *testing.T) {
	var f firstPressRescue

	// Nothing was pressed: a failure on its own is not ours to answer.
	if got := f.claim(time.Now()); got != 0 {
		t.Errorf("claim without a press = %d, want 0", got)
	}

	// A failure long after the press is a different story. Pressing the key
	// then would restart music somebody is listening to.
	press := time.Now()
	f.noteNativeActivation(3, press)
	if got := f.claim(press.Add(firstPressWindow + time.Second)); got != 0 {
		t.Errorf("a failure %v after the press claimed a rescue (%d)", firstPressWindow+time.Second, got)
	}

	// A failure logged BEFORE the press belongs to whatever came before it.
	press2 := time.Now()
	f.noteNativeActivation(4, press2)
	if got := f.claim(press2.Add(-2 * time.Second)); got != 0 {
		t.Errorf("a failure before the press claimed a rescue (%d)", got)
	}
}

// A new press re-arms: two bad wakes in a row each get their one retry.
func TestEachPressGetsItsOwnRescue(t *testing.T) {
	var f firstPressRescue
	first := time.Now()
	f.noteNativeActivation(1, first)
	if got := f.claim(first.Add(time.Second)); got != 1 {
		t.Fatalf("first claim = %d, want 1", got)
	}
	second := first.Add(time.Minute)
	f.noteNativeActivation(6, second)
	if got := f.claim(second.Add(time.Second)); got != 6 {
		t.Fatalf("second claim = %d, want 6", got)
	}
}
