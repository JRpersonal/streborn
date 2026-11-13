package oled

import (
	"testing"
	"time"
)

func TestLongStandbyLooksEveryHalfHourWhileInStandby(t *testing.T) {
	var s longStandbyState
	t0 := time.Date(2026, 11, 12, 22, 4, 0, 0, time.UTC)

	if s.due(t0.Add(2 * time.Hour)) {
		t.Fatal("a look before any standby")
	}
	s.note(true, t0)
	for m := 1; m < 30; m++ {
		if s.due(t0.Add(time.Duration(m) * time.Minute)) {
			t.Fatalf("a look %d min into the standby, want none before 30", m)
		}
	}
	if !s.due(t0.Add(30 * time.Minute)) {
		t.Fatal("no look 30 min into the standby")
	}
	if s.due(t0.Add(31 * time.Minute)) {
		t.Fatal("a second look one minute after the first")
	}
	if !s.due(t0.Add(60 * time.Minute)) {
		t.Fatal("no look an hour into the standby")
	}
}

// The bus frame and the syslog line of one power-off both report it; the
// second must not push the next look back.
func TestLongStandbyRepeatedStandbyKeepsTheCount(t *testing.T) {
	var s longStandbyState
	t0 := time.Date(2026, 11, 12, 22, 4, 0, 0, time.UTC)
	s.note(true, t0)
	s.note(true, t0.Add(20*time.Minute))
	if !s.due(t0.Add(30 * time.Minute)) {
		t.Fatal("a repeated standby report restarted the count")
	}
}

func TestLongStandbyStopsOnWake(t *testing.T) {
	var s longStandbyState
	t0 := time.Date(2026, 11, 12, 22, 4, 0, 0, time.UTC)
	s.note(true, t0)
	s.note(false, t0.Add(10*time.Minute))
	if s.due(t0.Add(45 * time.Minute)) {
		t.Fatal("a look on a speaker that is awake")
	}
	// A new standby counts from its own start.
	s.note(true, t0.Add(50*time.Minute))
	if s.due(t0.Add(70 * time.Minute)) {
		t.Fatal("a look 20 min into the new standby")
	}
	if !s.due(t0.Add(80 * time.Minute)) {
		t.Fatal("no look 30 min into the new standby")
	}
}

// An agent that starts while the speaker is already in standby, with no
// standby time to count from, takes the first minute line as the start.
func TestLongStandbyUnknownStartCountsFromFirstMinute(t *testing.T) {
	s := longStandbyState{inStandby: true}
	t0 := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	if s.due(t0) {
		t.Fatal("a look on the very first minute line")
	}
	if s.due(t0.Add(29 * time.Minute)) {
		t.Fatal("a look before 30 min")
	}
	if !s.due(t0.Add(30 * time.Minute)) {
		t.Fatal("no look 30 min after the first minute line")
	}
}

// A clock the watch could not bring back stops the looks until the next
// standby: the nudge is a standby sent to the speaker, and sending one every
// half hour all night would keep it out of deep standby.
func TestLongStandbyStuckVerdictBlocksUntilNextStandby(t *testing.T) {
	resetLongStandby()
	t.Cleanup(resetLongStandby)

	t0 := time.Date(2026, 11, 12, 22, 4, 0, 0, time.UTC)
	longStandby.note(true, t0)
	if !longStandby.due(t0.Add(30 * time.Minute)) {
		t.Fatal("no first look")
	}
	for _, v := range []darkVerdict{darkPainted, darkRecovered, darkNotClock, darkSuperseded, darkUnreadable} {
		longStandbyVerdict(v)
		if longStandby.blocked {
			t.Fatalf("verdict %q blocked the looks", v)
		}
	}
	longStandbyVerdict(darkStuck)
	if longStandby.due(t0.Add(2 * time.Hour)) {
		t.Fatal("a look after the clock was found stuck")
	}
	longStandby.note(false, t0.Add(9*time.Hour))
	longStandby.note(true, t0.Add(9*time.Hour+time.Minute))
	if !longStandby.due(t0.Add(9*time.Hour + 31*time.Minute)) {
		t.Fatal("the next standby did not clear the block")
	}

	longStandbyVerdict(darkLeft)
	if !longStandby.blocked {
		t.Fatal("a stuck clock with no nudge sent did not block")
	}
}

// resetLongStandby puts the package state back to a fresh agent's.
func resetLongStandby() {
	longStandby.mu.Lock()
	defer longStandby.mu.Unlock()
	longStandby.inStandby = false
	longStandby.last = time.Time{}
	longStandby.blocked = false
}
