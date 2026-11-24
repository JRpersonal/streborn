package webui

import (
	"testing"
	"time"
)

// An alarm on the key the speaker is already playing leaves it alone: no wake,
// no second recall (which cut the stream for a moment), and no switch-off for
// listening the alarm did not start (mail 2026-11-24, ST20 on NDR 1 since 06:44,
// a 07:00 alarm on key 1 with a 120-minute switch-off).
func TestAlarmLeavesTheKeyAloneWhenItIsAlreadyPlaying(t *testing.T) {
	t.Parallel()
	h := newAlarmHarness(t, autoOffDoc(120))
	h.s.alarmAlreadyPlayingFn = func(slot int) bool { return slot == 3 }
	now := berlinTime(t, 2026, time.September, 7, 6, 30)
	h.at(now)
	h.s.evaluateAlarms(now)
	if !waitFor(func() bool { _, ok := h.s.alarmState.LastOutcome(); return ok }) {
		t.Fatal("the fire left no outcome")
	}
	h.s.alarmBackground.Wait()
	if got := h.firedCount(); got != 0 {
		t.Errorf("recalled the playing key %d times, want 0", got)
	}
	if got := h.wakes.Load(); got != 0 {
		t.Errorf("woke the speaker %d times, want 0", got)
	}
	if h.s.sleepStatus()["active"] == true {
		t.Error("armed the alarm's switch-off for listening the alarm did not start")
	}
	last, _ := h.s.alarmState.LastOutcome()
	if !last.OK || last.Detail != "already playing" {
		t.Errorf("outcome = %+v, want OK with detail \"already playing\"", last)
	}
	h.s.ring.mu.Lock()
	active := h.s.ring.active
	h.s.ring.mu.Unlock()
	if active {
		t.Error("the ring is still active, a snooze key would act on an alarm that never rang")
	}
}

// Another key playing is not the alarm's station: the alarm fires as before.
func TestAlarmStillFiresWhenAnotherKeyIsPlaying(t *testing.T) {
	t.Parallel()
	h := newAlarmHarness(t, weekdayDoc())
	h.s.alarmAlreadyPlayingFn = func(slot int) bool { return slot == 5 }
	now := berlinTime(t, 2026, time.September, 7, 6, 30)
	h.at(now)
	h.s.evaluateAlarms(now)
	if !waitFor(func() bool { return h.firedCount() == 1 }) {
		t.Fatalf("the alarm did not fire over another key: %d recalls", h.firedCount())
	}
}
