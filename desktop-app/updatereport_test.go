package main

import "testing"

// TestReachedThisSessionSuppressesNetworkBlame pins the #03 fix (Klaus,
// 2026-09-03): the app SSH'd into the speaker and ran the installer, then the
// box did not come back on the network. The report must NOT then blame the
// network (client isolation / firewall / guest Wi-Fi), because none of that
// would have let the SSH install run. It must lead with the agent-not-up advice.
func TestReachedThisSessionSuppressesNetworkBlame(t *testing.T) {
	r := failureReport{
		Phase:   "install:agent-not-up",
		History: "install_str: ssh ok\nrepair_ssh: install ran (outBytes=671)\nstep=reboot code=agent-not-up",
		Facts:   installFacts{SubnetKnown: true, SameSubnet: true, PingRan: true, PingAlive: false},
	}
	if !reachedThisSession(r) {
		t.Fatal("SSH-install evidence must count as reached-this-session")
	}
	got := applyReachedThisSession([]string{notReachableAdvice, firewallAdvice, "keep me"}, r)
	if len(got) == 0 || got[0] != agentNotUpAdvice {
		t.Fatalf("must lead with agentNotUpAdvice, got %v", got)
	}
	for _, p := range got {
		if p == notReachableAdvice || p == firewallAdvice || p == isolationAdvice {
			t.Error("network-blame advice must be dropped once the app reached the box")
		}
	}
	// A non-blame paragraph is kept.
	if got[len(got)-1] != "keep me" {
		t.Error("unrelated advice must be preserved")
	}
	// applyIsolationDiagnosis must now be a no-op: isolation is suppressed.
	if iso := applyIsolationDiagnosis(got, r); iso[0] != agentNotUpAdvice {
		t.Error("applyIsolationDiagnosis must not re-add isolation once the app reached the box")
	}
}

// TestReachedThisSessionFalseForDifferentSubnet pins the other side (Walter,
// 2026-09-03): the SoundTouch was on a different subnet (192.168.1.1), no SSH
// ran, so the network advice is correct and must stay. A bare "agent-not-up"
// with no SSH evidence must not be mistaken for a reached-this-session install.
func TestReachedThisSessionFalseForDifferentSubnet(t *testing.T) {
	r := failureReport{
		Phase:   "install:agent-not-up",
		History: "network install started\nthe agent did not come up in time",
		Facts:   installFacts{SubnetKnown: true, SameSubnet: false, PingRan: true, PingAlive: false},
	}
	if reachedThisSession(r) {
		t.Fatal("a network install with no SSH evidence must not count as reached (wrong subnet)")
	}
	got := applyReachedThisSession([]string{notReachableAdvice}, r)
	if len(got) != 1 || got[0] != notReachableAdvice {
		t.Fatalf("network advice must be kept for a different-subnet box, got %v", got)
	}
}
