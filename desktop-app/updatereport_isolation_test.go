package main

import (
	"strings"
	"testing"
)

// #763: a speaker discovered on this PC's subnet that answers on no port AND
// does not reply to a ping is behind Wi-Fi client isolation, not a PC firewall.
func TestApplyIsolationDiagnosis(t *testing.T) {
	iso := failureReport{
		Phase: "install:not-reachable",
		Facts: installFacts{SubnetKnown: true, SameSubnet: true, PingRan: true, PingAlive: false},
	}

	// The isolation signature: isolation advice leads, the firewall paragraph goes.
	got := applyIsolationDiagnosis([]string{firewallAdvice, notReachableAdvice, "keep me"}, iso)
	if len(got) == 0 || got[0] != isolationAdvice {
		t.Fatalf("isolation advice must lead, got %v", got)
	}
	for _, p := range got {
		if p == firewallAdvice || p == notReachableAdvice {
			t.Fatalf("generic firewall/not-reachable advice should be dropped: %q", p)
		}
	}
	if got[len(got)-1] != "keep me" {
		t.Fatalf("unrelated advice must survive, got %v", got)
	}

	// A speaker that DOES answer a ping is not isolated: leave the advice alone.
	answers := iso
	answers.Facts.PingAlive = true
	got2 := applyIsolationDiagnosis([]string{firewallAdvice}, answers)
	if len(got2) != 1 || got2[0] != firewallAdvice {
		t.Fatalf("ping-alive must not trigger isolation advice, got %v", got2)
	}

	// A different subnet (not established as same) is not this diagnosis either.
	other := iso
	other.Facts.SameSubnet = false
	if g := applyIsolationDiagnosis([]string{firewallAdvice}, other); len(g) != 1 || g[0] != firewallAdvice {
		t.Fatalf("non-same-subnet must not trigger isolation advice, got %v", g)
	}

	// It only fires on the not-reachable phase (all ports were silent).
	wrongPhase := iso
	wrongPhase.Phase = "install:ssh-handshake"
	if g := applyIsolationDiagnosis([]string{firewallAdvice}, wrongPhase); len(g) != 1 {
		t.Fatalf("wrong phase must not trigger isolation advice, got %v", g)
	}

	// End to end: the rendered report carries the isolation paragraph.
	report := formatFailureReport(iso)
	if !strings.Contains(report, "client isolation") {
		t.Fatalf("rendered report missing the isolation guidance:\n%s", report)
	}
}
