package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// #1322: a Wave woken for a folder from the app. The forced pass, held on the
// Wave's own source, was released the moment the source went INVALID_SOURCE on
// the way to STR's stream, wrote the native presets, and the box switched to
// LOCAL_INTERNET_RADIO. The first track never played.
func TestAPlayThatIsStartingHoldsTheForcedPass(t *testing.T) {
	cases := []struct {
		src     string
		playing bool
		known   bool
		since   time.Duration
		hold    bool
	}{
		{"INVALID_SOURCE", false, true, 3 * time.Second, true},
		{"UPNP", false, true, 10 * time.Second, true},
		{"STANDBY", false, true, 2 * time.Second, true},
		{"", false, false, 2 * time.Second, true},
		// Flowing audio is forcedWriteHold's business, with its own ceiling.
		{"UPNP", true, true, 10 * time.Second, false},
		// A source the listener chose is held by the existing rule.
		{"BLUETOOTH", false, true, 3 * time.Second, false},
		// Outside the window, or with no play at all, nothing changes.
		{"INVALID_SOURCE", false, true, userPlayStartWindow, false},
		{"INVALID_SOURCE", false, true, -1, false},
	}
	for _, c := range cases {
		if got := userPlayStartingHold(c.src, c.playing, c.known, c.since); got != c.hold {
			t.Errorf("userPlayStartingHold(%q, playing=%v, since=%v) = %v, want %v",
				c.src, c.playing, c.since, got, c.hold)
		}
	}
}

func TestSinceLastUserPlayWithoutAWebuiIsUnknown(t *testing.T) {
	old := lastUserPlayStartFn
	t.Cleanup(func() { lastUserPlayStartFn = old })
	lastUserPlayStartFn = nil
	if d := sinceLastUserPlay(); d >= 0 {
		t.Fatalf("no webui wired, got %v, want -1", d)
	}
	lastUserPlayStartFn = func() time.Time { return time.Time{} }
	if d := sinceLastUserPlay(); d >= 0 {
		t.Fatalf("never played, got %v, want -1", d)
	}
	lastUserPlayStartFn = func() time.Time { return time.Now().Add(-5 * time.Second) }
	if d := sinceLastUserPlay(); d < 4*time.Second || d > 10*time.Second {
		t.Fatalf("played 5 s ago, got %v", d)
	}
}

// The wiring, which a pure helper test cannot see: the held pass asks before
// the play-hold, and a night in standby does not count towards the ceiling.
func TestTheReconcileLoopUsesTheStartHoldAndForgetsStandbyTime(t *testing.T) {
	b, err := os.ReadFile("reconcile.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "userPlayStartingHold(src, playing, playKnown, since)")
	j := strings.Index(src, "hold, ceilingHit := forcedWriteHold(")
	if i < 0 || j < 0 || i > j {
		t.Error("the forced pass no longer checks for a starting play before the playback hold")
	}
	k := strings.Index(src, "deferPresetResyncForStandby(logger, retryHeldSrc)")
	if k < 0 || !strings.Contains(src[k:min(k+800, len(src))], "forceHeldSince = time.Time{}") {
		t.Error("the standby wait no longer resets the hold ceiling, so a night asleep fires the pass into the morning's resume")
	}
	m, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(m), "lastUserPlayStartFn = webuiSrv.LastUserPlayStart") {
		t.Error("the start hold is not wired to the webui, so it never holds")
	}
}
