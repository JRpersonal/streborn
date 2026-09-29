package boxcli

import (
	"testing"
	"time"
)

// The arithmetic that made the retry unreachable. WakeAndWait spends up to
// selfWakeGrace watching for a self-wake, then toggles once and waits
// toggleEvery before considering a second toggle. A deadline below the sum of
// those two fires first, every time, so a speaker that ignored the first toggle
// is reported as asleep after exactly one attempt.
//
// Five of the nine callers passed six seconds, which is under that sum.
func TestTheWakeBudgetCanContainASecondAttempt(t *testing.T) {
	if minWakeBudget <= selfWakeGrace+toggleEvery {
		t.Fatalf("minWakeBudget %s does not outlast grace %s + toggleEvery %s, so the second toggle can never be reached",
			minWakeBudget, selfWakeGrace, toggleEvery)
	}
	// The margin has to leave room for at least one poll after the re-toggle
	// decision, which happens every 400 ms.
	if margin := minWakeBudget - (selfWakeGrace + toggleEvery); margin < time.Second {
		t.Errorf("only %s of margin after the retry point, too tight to poll", margin)
	}
	// And it must not grow into a wait a user reads as a hang.
	if minWakeBudget > 15*time.Second {
		t.Errorf("minWakeBudget %s is long enough to read as a hang", minWakeBudget)
	}
}

// The six seconds the callers used to pass is exactly the value this floor
// exists to correct, so it must be raised rather than honoured.
func TestASixSecondBudgetIsRaised(t *testing.T) {
	if 6*time.Second >= minWakeBudget {
		t.Fatal("six seconds is no longer below the floor, so this test has lost its point")
	}
}
