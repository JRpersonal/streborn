// #923: six to seven seconds of silence between folder tracks.
//
// Timed against the same Synology folder played twice on one ST20, once driven
// by STR and once by the box itself: 157 s against 150 s, and 236 s against
// 230 s. The box's own advance is 0.8 s, which is why the Bose app sounds
// gapless and STR does not.
//
// The chassis finishes a file and freezes in PLAY_STATE at EOF instead of
// emitting STOP, so the STOP branch never runs and every track ends on the
// wall-clock net at end + queueTimerMargin.
//
// Those six seconds are a grace for a box that reports NO position: with nothing
// to corroborate the length against, cutting a buffering track short is the
// worse failure. A box that reports a position AND reports it sitting at the end
// has already said the track is over.

package webui

import (
	"testing"
	"time"
)

func TestTheGraceIsSkippedWhenTheBoxConfirmsTheEnd(t *testing.T) {
	s := quietServer("192.0.2.10")
	const end = 3 * time.Minute

	// The #923 shape: the box reports a position, and it is at the end.
	if m := s.advanceMargin(end, end); m != 0 {
		t.Fatalf("margin %s with the position at the end; that is the gap users hear", m)
	}
	// Just inside the epsilon still counts as the end.
	if m := s.advanceMargin(end-queueEndEpsilon+time.Second, end); m != 0 {
		t.Fatalf("margin %s for a position inside the end epsilon", m)
	}
}

func TestTheGraceStaysWhereThereIsNothingToCorroborateWith(t *testing.T) {
	s := quietServer("192.0.2.10")
	const end = 3 * time.Minute

	// No position at all: the FRITZ!Box-class case the margin was written for.
	if m := s.advanceMargin(0, end); m != queueTimerMargin {
		t.Fatalf("margin %s with no position reported, want the full %s", m, queueTimerMargin)
	}
	// A position that is nowhere near the end: the track is still running, and
	// cutting it off is the failure the grace exists to prevent.
	if m := s.advanceMargin(30*time.Second, end); m != queueTimerMargin {
		t.Fatalf("margin %s mid-track, want the full %s", m, queueTimerMargin)
	}
}

// The floor is the poll interval, and it stays where it is: a faster poll would
// run on every speaker all day for this one case.
func TestThePollIntervalIsNotTightened(t *testing.T) {
	if queuePollInterval < 4*time.Second {
		t.Fatalf("queuePollInterval dropped to %s; that runs on every speaker all day", queuePollInterval)
	}
}
