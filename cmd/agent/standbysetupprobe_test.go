package main

import "testing"

// A speaker in STANDBY is not asked /setup: BoseApp's system controller takes
// every GET /setup as an event, and a five-minute question all night is what
// a sleeping speaker got from STR instead of its deep standby.
func TestSetupProbeSkippedWhileTheSpeakerSleeps(t *testing.T) {
	cases := map[string]bool{
		"STANDBY":        false,
		"":               true, // nothing known yet: ask, as before
		"SETUP":          true, // the very state the probe exists for
		"UPNP":           true,
		"INVALID_SOURCE": true,
	}
	for src, want := range cases {
		if got := setupProbeNeeded(src); got != want {
			t.Errorf("setupProbeNeeded(%q) = %v, want %v", src, got, want)
		}
	}
}

// The source the pass reads comes from what STR already saw: a gabbo edge or
// a now_playing read. A failed read ("") must not wipe a known source.
func TestLastKnownBoxSourceKeepsTheLastRealReading(t *testing.T) {
	prev := lastKnownBoxSource()
	t.Cleanup(func() { lastBoxSource.Store(prev) })

	noteBoxSource("STANDBY")
	noteBoxSource("")
	if got := lastKnownBoxSource(); got != "STANDBY" {
		t.Fatalf("after a failed read the source is %q, want STANDBY kept", got)
	}
	noteBoxSource("SETUP")
	if got := lastKnownBoxSource(); got != "SETUP" {
		t.Fatalf("source = %q, want SETUP", got)
	}
	if setupProbeNeeded(lastKnownBoxSource()) != true {
		t.Fatal("a speaker that left standby for SETUP must be asked /setup again")
	}
}
