package main

import (
	"testing"
	"time"
)

// reset clears the decision state so each test starts from "nothing said yet".
func resetResHealth() {
	resHealthMu.Lock()
	defer resHealthMu.Unlock()
	resHealthHaveLast = false
	resHealthLastAt = time.Time{}
	resHealthLastMem, resHealthLastRSS, resHealthLastThr = 0, 0, 0
}

// The instrument exists to show the memory trend before an OOM freeze. Quieting
// it must never cost a movement, only the flat repeats that were burying them in
// a 32 KB ring that has to survive a reboot.
func TestResourceHealthLogsWhatMatters(t *testing.T) {
	const total = 120000
	t0 := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	resetResHealth()
	if why, ok := resourceHealthWorthLogging(60000, total, 11000, 9, t0); !ok || why != "first" {
		t.Fatalf("first reading: why=%q ok=%v, want first/true", why, ok)
	}
	// A flat box a minute later says nothing new.
	if _, ok := resourceHealthWorthLogging(60000, total, 11000, 9, t0.Add(time.Minute)); ok {
		t.Error("an unchanged reading was logged; that is the noise this removes")
	}
	// Small drift is still noise.
	if _, ok := resourceHealthWorthLogging(58000, total, 11050, 9, t0.Add(2*time.Minute)); ok {
		t.Error("a 3 % drift was logged")
	}
	// A real drop is the signal.
	if why, ok := resourceHealthWorthLogging(50000, total, 11050, 9, t0.Add(3*time.Minute)); !ok || why != "memory-moved" {
		t.Errorf("a 14 %% memory drop: why=%q ok=%v, want memory-moved/true", why, ok)
	}
	// The agent's own footprint growing is how we tell OUR leak from BoseApp's.
	if why, ok := resourceHealthWorthLogging(50000, total, 13500, 9, t0.Add(4*time.Minute)); !ok || why != "agent-rss-moved" {
		t.Errorf("agent RSS +22 %%: why=%q ok=%v, want agent-rss-moved/true", why, ok)
	}
	// A thread count change is cheap to carry and pins a goroutine leak.
	if why, ok := resourceHealthWorthLogging(50000, total, 13500, 14, t0.Add(5*time.Minute)); !ok || why != "threads-changed" {
		t.Errorf("thread count change: why=%q ok=%v, want threads-changed/true", why, ok)
	}
}

// Once memory is genuinely low, every reading counts: that is the run-up this
// whole instrument was added for, and it must not be sampled away.
func TestResourceHealthNeverQuietsALowBox(t *testing.T) {
	const total = 120000
	t0 := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	resetResHealth()
	resourceHealthWorthLogging(20000, total, 11000, 9, t0) // first
	for i := 1; i <= 5; i++ {
		why, ok := resourceHealthWorthLogging(20000, total, 11000, 9, t0.Add(time.Duration(i)*time.Minute))
		if !ok || why != "low-memory" {
			t.Fatalf("reading %d on a low box: why=%q ok=%v, want low-memory/true", i, why, ok)
		}
	}
}

// A box that never moves still needs a trend line, so one anchor an hour lands.
func TestResourceHealthDropsAnHourlyAnchor(t *testing.T) {
	const total = 120000
	t0 := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	resetResHealth()
	resourceHealthWorthLogging(60000, total, 11000, 9, t0)
	if _, ok := resourceHealthWorthLogging(60000, total, 11000, 9, t0.Add(59*time.Minute)); ok {
		t.Error("logged before the hour was up")
	}
	if why, ok := resourceHealthWorthLogging(60000, total, 11000, 9, t0.Add(61*time.Minute)); !ok || why != "hourly-anchor" {
		t.Errorf("after an hour: why=%q ok=%v, want hourly-anchor/true", why, ok)
	}
}

// How much of the old traffic disappears on an idle box: the old code wrote one
// line every five minutes forever, the new one writes one an hour.
func TestResourceHealthIdleBoxCollapsesToHourlyAnchors(t *testing.T) {
	const total = 120000
	t0 := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	resetResHealth()
	logged := 0
	for i := 0; i < 12*24; i++ { // 24 hours at the old 5-minute cadence
		if _, ok := resourceHealthWorthLogging(60000, total, 11000, 9, t0.Add(time.Duration(i)*5*time.Minute)); ok {
			logged++
		}
	}
	if logged > 26 { // 24 anchors + the first reading, with a little slack
		t.Errorf("an idle day still logged %d lines, want about 25 (was 288)", logged)
	}
	if logged < 20 {
		t.Errorf("an idle day logged only %d lines; the trend anchor is missing", logged)
	}
}

func TestMoved(t *testing.T) {
	cases := []struct {
		prev, cur int64
		frac      float64
		want      bool
	}{
		{100, 100, 0.08, false},
		{100, 105, 0.08, false},
		{100, 92, 0.08, true},
		{100, 108, 0.08, true},
		{0, 50, 0.08, true},  // first real reading after a failed read
		{-1, 50, 0.08, true}, // /proc unreadable last time
		{100, 0, 0.08, true}, // reading failed now: worth knowing
	}
	for _, tc := range cases {
		if got := moved(tc.prev, tc.cur, tc.frac); got != tc.want {
			t.Errorf("moved(%d, %d, %.2f) = %v, want %v", tc.prev, tc.cur, tc.frac, got, tc.want)
		}
	}
}
