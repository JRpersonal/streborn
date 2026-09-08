package webui

import (
	"testing"
	"time"
)

// Forming a group against a speaker that is asleep means STR wakes it first.
// The wake looks exactly like a user pressing power, and the power-on resume
// then started the last station: a group formed while nothing was playing and
// music came on (#900, 2026-09-08).
//
// The existing guard asks whether the box is in a zone, because a standalone
// box can only have been woken by a person. That reasoning has a hole of about
// four seconds, which is precisely the window STR uses to wake a master BEFORE
// the zone exists:
//
//	17:23:50 power-on detected, attempting last-station resume
//	17:23:51 wake: quiet wake for a group operation
//	17:23:53 wake resume: resumed last stream after power-on
//	17:23:54 zone: forming (beta)
//
// A quiet wake is STR's own, so while one is armed a power-on frame is not a
// user press.
func TestQuietWakeMarksTheWakeAsSTRsOwn(t *testing.T) {
	s := &Server{}
	if s.quietWakeActive() {
		t.Fatal("no quiet wake armed, must not report one")
	}

	s.quietWakeMu.Lock()
	s.quietWakeVol = 32
	s.quietWakeUntil = time.Now().Add(quietWakeRestoreAfter)
	s.quietWakeMu.Unlock()

	if !s.quietWakeActive() {
		t.Error("a wake STR did for a group operation must be recognisable as its own")
	}

	// The zone join clears it, and from then on a power press is a power press
	// again. Cleared here the way restoreQuietWakeVolume does, without the box
	// call it would make.
	s.quietWakeMu.Lock()
	s.quietWakeUntil = time.Time{}
	s.quietWakeMu.Unlock()

	if s.quietWakeActive() {
		t.Error("once the quiet wake is over, a wake must count as a user press again")
	}
}

// The window is bounded: a speaker cannot be locked out of its power-on resume
// for good because one group operation touched it.
func TestQuietWakeWindowIsBounded(t *testing.T) {
	s := &Server{}
	s.quietWakeMu.Lock()
	s.quietWakeVol = 20
	s.quietWakeUntil = time.Now().Add(quietWakeRestoreAfter)
	until := s.quietWakeUntil
	s.quietWakeMu.Unlock()

	if d := time.Until(until); d > time.Minute {
		t.Errorf("quiet wake window is %v, too long to suppress a user's power press", d)
	}
	if !s.quietWakeActive() {
		t.Fatal("armed wake not reported")
	}
}
