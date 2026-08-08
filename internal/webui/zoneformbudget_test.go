package webui

import (
	"testing"
	"time"
)

// The budget covers the whole form, not just the /setZone drive: waking the
// master, reading the live zone, removing dropped members, forming, and the
// confirming read. A flat ten seconds was therefore a ceiling on group size.
//
// Measured on a twelve-speaker fleet, 2026-08-08, one speaker added at a time:
// up to five slaves formed in 4 to 8 s, six took 22 s, and seven failed five
// times in a row with "setZone: context deadline exceeded". The same fleet had
// formed a group of twelve earlier the same afternoon, so nothing was wrong
// with the eighth speaker.
func TestZoneFormBudgetGrowsWithTheGroup(t *testing.T) {
	// The sizes that used to work must not become slower to fail.
	if got := zoneFormBudget(0); got < 10*time.Second {
		t.Errorf("zoneFormBudget(0) = %v, want at least the old 10s", got)
	}

	// The size that failed in the field must now have room. Six slaves were
	// measured at 22 s, so seven needs comfortably more than that.
	if got := zoneFormBudget(7); got <= 22*time.Second {
		t.Errorf("zoneFormBudget(7) = %v, want more than the 22s six slaves measured", got)
	}

	// Strictly increasing up to the ceiling, so a bigger group never gets less
	// time than a smaller one.
	prev := time.Duration(0)
	for n := 0; n <= 20; n++ {
		got := zoneFormBudget(n)
		if got < prev {
			t.Errorf("zoneFormBudget(%d) = %v is less than for %d slaves (%v)", n, got, n-1, prev)
		}
		prev = got
	}
}

// The agent must never answer after the desktop app has stopped listening: the
// app would report a failure for a group the firmware went on to build, which
// is the confusing half of this bug rather than the slow half. The app's own
// budget for this call is 45 s.
func TestZoneFormBudgetStaysUnderTheAppsTimeout(t *testing.T) {
	const appTimeout = 45 * time.Second
	for _, n := range []int{0, 1, 6, 7, 11, 50, 1000} {
		if got := zoneFormBudget(n); got >= appTimeout {
			t.Errorf("zoneFormBudget(%d) = %v, want comfortably under the app's %v", n, got, appTimeout)
		}
	}
}
