package main

import "testing"

// An owner on a congested Wi-Fi found one speaker listed twice in the Update
// list, once on its real address and once on a 169.254 self-assigned one, which
// blocked Update All (2026-08-23). A speaker whose DHCP fails announces a
// self-assigned address; STR recorded it, the speaker later got a real lease and
// announced that too, and both stayed.
//
// From a normal LAN a 169.254 address cannot be routed to at all, so that entry
// was never reachable. It stays usable in the one case where it means
// something: this machine on link-local too, which is a direct cable with no
// DHCP.

func withLinkLocalHost(t *testing.T, present bool) {
	t.Helper()
	old := hostHasLinkLocalIPv4
	hostHasLinkLocalIPv4 = func() bool { return present }
	t.Cleanup(func() { hostHasLinkLocalIPv4 = old })
}

func TestPickReachableIPDropsASelfAssignedAddressFromANormalLAN(t *testing.T) {
	withLinkLocalHost(t, false)

	if got := pickReachableIP([]string{"169.254.12.7"}); got != "" {
		t.Errorf("got %q, want it dropped: unroutable from here", got)
	}
	// And when the speaker announces both, the real one wins, which is what
	// stops the duplicate.
	if got := pickReachableIP([]string{"169.254.12.7", "192.168.1.34"}); got != "192.168.1.34" {
		t.Errorf("got %q, want 192.168.1.34", got)
	}
	if got := pickReachableIP([]string{"192.168.1.34", "169.254.12.7"}); got != "192.168.1.34" {
		t.Errorf("got %q, want 192.168.1.34 regardless of order", got)
	}
}

func TestPickReachableIPKeepsLinkLocalOnADirectCable(t *testing.T) {
	withLinkLocalHost(t, true)

	if got := pickReachableIP([]string{"169.254.12.7"}); got != "169.254.12.7" {
		t.Errorf("got %q, want the link-local address kept when this host has one too", got)
	}
	// A real address still outranks it.
	if got := pickReachableIP([]string{"169.254.12.7", "10.0.0.5"}); got != "10.0.0.5" {
		t.Errorf("got %q, want 10.0.0.5", got)
	}
}

func TestPickReachableIPStillSkipsTheUnroutableOnes(t *testing.T) {
	withLinkLocalHost(t, false)

	// The box's USB gadget interface and loopback were already excluded, and
	// must stay excluded: returning either shows a speaker nothing can reach.
	if got := pickReachableIP([]string{"203.0.113.2", "127.0.0.1"}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := pickReachableIP([]string{"203.0.113.2", "192.168.1.34"}); got != "192.168.1.34" {
		t.Errorf("got %q, want 192.168.1.34", got)
	}
	if got := pickReachableIP(nil); got != "" {
		t.Errorf("got %q, want empty for no addresses", got)
	}
}
