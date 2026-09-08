package main

import (
	"strings"
	"testing"
)

// #873: two reporters were told to look at their firewall and their Wi-Fi for
// a speaker that was busy running Bose's OWN out-of-box setup, on a network
// that had just carried a complete install to that same speaker. One of them
// acted on it and pressed Install again, which produced a second and worse
// failure.
func TestApplyBoxInSetupDiagnosis(t *testing.T) {
	inSetup := failureReport{
		Phase: "install:speaker-not-back",
		Facts: installFacts{BoxSetupState: "recent"},
	}

	got := applyBoxInSetupDiagnosis([]string{firewallAdvice, notReachableAdvice, "keep me"}, inSetup)
	if len(got) == 0 || got[0] != boxInSetupAdvice {
		t.Fatalf("the setup paragraph must lead, got %v", got)
	}
	for _, p := range got {
		if p == firewallAdvice || p == notReachableAdvice {
			t.Fatalf("network blame should be dropped for a speaker in its own setup: %q", p)
		}
	}
	if got[len(got)-1] != "keep me" {
		t.Fatalf("unrelated advice must survive, got %v", got)
	}

	// No episodes and no signature: this diagnosis must not fire at all, or
	// every ordinary failure would be blamed on a setup that never happened.
	quiet := failureReport{Phase: "install:not-reachable"}
	if g := applyBoxInSetupDiagnosis([]string{firewallAdvice}, quiet); len(g) != 1 || g[0] != firewallAdvice {
		t.Fatalf("a speaker that never entered setup must keep its advice, got %v", g)
	}

	// A LIFETIME episode count is not evidence. The agent never resets the
	// counter, so a speaker that ran its setup at install time and has been up
	// for three weeks still reports episodes>0; keying on it would strip the
	// firewall paragraph from every later failure on that box. Only the aged
	// STATE is read, and the agent's own "stale" is not fresh.
	stale := failureReport{
		Phase: "install:not-reachable",
		Facts: installFacts{BoxSetupState: "stale"},
	}
	if g := applyBoxInSetupDiagnosis([]string{firewallAdvice}, stale); len(g) != 1 || g[0] != firewallAdvice {
		t.Fatalf("an aged-out episode must not displace the network advice, got %v", g)
	}

	// The speaker's own log tail is the second route in: it is what an agent
	// too old to keep the counters still gives up. It has to be a REAL setup
	// access point, which is what the setupState the agent now logs says.
	fromLog := failureReport{
		Phase:  "install:speaker-not-back",
		BoxLog: "-- agent log --\nleave-setup: cleared the box's SETUP source setupState=SETUP_AP_OOB systemState=SETUP_LANG_NOT_SET\n",
	}
	if g := applyBoxInSetupDiagnosis([]string{firewallAdvice}, fromLog); len(g) == 0 || g[0] != boxInSetupAdvice {
		t.Fatalf("the log signature must trigger the diagnosis, got %v", g)
	}

	// The routine #367 clear at boot is NOT an out-of-box setup: it is the
	// normal, permanent repair of a merely stuck source on an ST300 and an scm
	// ST30, and blaming setup for it would delete the one right answer a
	// genuinely firewalled owner of those models gets.
	stuckSource := failureReport{
		Phase:  "install:not-reachable",
		BoxLog: "-- agent log --\nleave-setup: cleared the box's stuck out-of-box SETUP source setupState=SETUP_INACTIVE systemState=SETUP_LANG_SET\n",
	}
	if g := applyBoxInSetupDiagnosis([]string{firewallAdvice}, stuckSource); len(g) != 1 || g[0] != firewallAdvice {
		t.Fatalf("a routine stuck-source clear must not read as an out-of-box setup, got %v", g)
	}

	// End to end: the rendered report carries the paragraph and says the one
	// thing that matters, that this is not the user's network.
	report := formatFailureReport(inSetup)
	if !strings.Contains(report, "busy with its own setup") {
		t.Fatalf("rendered report missing the setup guidance:\n%s", report)
	}
	if !strings.Contains(report, "not your firewall") {
		t.Fatalf("rendered report does not clear the user's network:\n%s", report)
	}
}

// The log signature must recognise the shapes STR actually writes, and nothing
// else: a source change to some other source is not a setup episode.
func TestBoxSetupSignature(t *testing.T) {
	cases := []struct {
		name, tail string
		want       bool
	}{
		{"a clear of a REAL setup AP", "leave-setup: cleared ... setupState=SETUP_AP_OOB systemState=SETUP_LANG_NOT_SET", true},
		{"the routine #367 stuck-source clear", "leave-setup: cleared the box's stuck out-of-box SETUP source setupState=SETUP_INACTIVE", false},
		{"the firmware's AP warning", "box ws: the firmware raised its setup access point (the speaker is about to drop off the LAN)", true},
		{"the gabbo source flip", `box ws: source changed from=STANDBY to=SETUP`, true},
		{"an ordinary source change", `box ws: source changed from=STANDBY to=UPNP`, false},
		// Both tokens have to be on ONE line. Two unrelated lines each
		// carrying half of the signature are not a setup episode, and reading
		// them as one deletes the firewall advice a genuinely firewalled user
		// needs.
		{"the two halves on unrelated lines", strings.Join([]string{
			"box ws: source changed from=STANDBY to=UPNP",
			"lir: preset recall to=SETUP_ish slot=2",
		}, "\n"), false},
		{"an empty tail", "", false},
	}
	for _, c := range cases {
		if got := boxSetupSignature(c.tail); got != c.want {
			t.Errorf("%s: boxSetupSignature = %v, want %v", c.name, got, c.want)
		}
	}
}
