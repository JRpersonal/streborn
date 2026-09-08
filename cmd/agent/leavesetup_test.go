package main

// The leave-setup watcher's cadence and its budget.
//
// #873: on the reporter's ST10 an entry into SETUP landed 14 s after the fast
// window closed and then sat 4m46s before the next check; the one after that
// sat 2m45s. About seven minutes of a thirteen-minute episode was STR's own
// poll gap. The fix is not a wider fixed window (that was tried once already,
// for a box that entered SETUP forty seconds late, and this one entered ten
// minutes late) but a cadence that follows the STATE.

import (
	"testing"
	"time"
)

func TestLeaveSetupCadenceFollowsTheState(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Minute)  // the fast window has closed
	future := now.Add(time.Minute) // still inside it
	cases := []struct {
		name       string
		fastUntil  time.Time
		inSetup    bool
		backingOff bool
		want       time.Duration
	}{
		{"inside the post-boot window", future, false, false, leaveSetupFast},
		{"healthy box, window closed", past, false, false, leaveSetupSlow},
		// The measured failure: the box IS in setup and the window has closed,
		// which used to drop the check to the five-minute maintenance ticker.
		{"in setup after the window closed", past, true, false, leaveSetupFast},
		// A box whose POST /setup keeps failing lands here too: the budget
		// counts ATTEMPTS, so failures spend it just like successes and the
		// watcher drops to the backoff instead of polling and warning every
		// fifteen seconds for the rest of the box's uptime.
		{"in setup, clears keep failing", past, true, true, leaveSetupBackoff},
		{"in setup, budget spent", past, true, true, leaveSetupBackoff},
		{"backing off outranks the fast window", future, true, true, leaveSetupBackoff},
	}
	for _, c := range cases {
		if got := leaveSetupCadence(now, c.fastUntil, c.inSetup, c.backingOff); got != c.want {
			t.Errorf("%s: cadence = %s, want %s", c.name, got, c.want)
		}
	}
}

// The cap on the fight: a POST /setup is a WRITE, and past the first few of
// them STR is not repairing anything, it is writing to the speaker once a
// minute for the rest of an episode that can last a quarter of an hour. The
// budget counts ATTEMPTS, so a box whose clear always fails reaches the cap
// too instead of being polled and warned about forever.
func TestLeaveSetupClearBudgetCountsAttempts(t *testing.T) {
	for attempts := 0; attempts < leaveSetupClearsPerEpisode; attempts++ {
		if leaveSetupBudgetSpent(attempts) {
			t.Errorf("%d attempts already spent the budget of %d", attempts, leaveSetupClearsPerEpisode)
		}
	}
	if !leaveSetupBudgetSpent(leaveSetupClearsPerEpisode) {
		t.Errorf("%d attempts must spend the budget", leaveSetupClearsPerEpisode)
	}
	// The counter is per EPISODE, and the watcher resets it when the box
	// leaves setup, so a speaker that does this once a month is served at full
	// speed every time.
	if leaveSetupBudgetSpent(0) {
		t.Error("a fresh episode must start with a full budget")
	}
}

// The budget only means something if a flapping source cannot refill it.
//
// #873's box flips between SETUP and INVALID_SOURCE for several minutes. Every
// flip back into SETUP is an "entry" as far as the poll is concerned, and a
// budget that refilled on each of them would have let STR POST /setup every
// fifteen seconds for the whole quarter of an hour: the write storm the cap
// exists to prevent, on the exact box that provoked it.
func TestLeaveSetupFlappingSourceIsOneEpisode(t *testing.T) {
	start := time.Now()
	cases := []struct {
		name       string
		now        time.Time
		lastSeen   time.Time
		wasInSetup bool
		want       bool
	}{
		{"the first SETUP ever seen", start, time.Time{}, false, true},
		{"still in setup on the next pass", start.Add(15 * time.Second), start, true, false},
		{"flipped to INVALID_SOURCE and straight back",
			start.Add(30 * time.Second), start, false, false},
		{"flapping for minutes is still one episode",
			start.Add(leaveSetupEpisodeGap - time.Minute), start, false, false},
		{"a genuinely separate episode later",
			start.Add(leaveSetupEpisodeGap + time.Minute), start, false, true},
	}
	for _, c := range cases {
		if got := leaveSetupNewEpisode(c.now, c.lastSeen, c.wasInSetup); got != c.want {
			t.Errorf("%s: leaveSetupNewEpisode = %v, want %v", c.name, got, c.want)
		}
	}
}
