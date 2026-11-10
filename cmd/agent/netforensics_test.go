package main

import (
	"strings"
	"testing"
)

func nfSnap(carrier, hasIP bool, arp string) netForensicsSnapshot {
	return netForensicsSnapshot{IfaceCarrier: carrier, HasLANIP: hasIP, GatewayARP: arp}
}

func TestNetDegradationsCatchesTheBlackholeSignatures(t *testing.T) {
	healthy := nfSnap(true, true, "reachable")
	cases := []struct {
		name string
		cur  netForensicsSnapshot
		want string // substring expected in a degradation line; "" = none
	}{
		{"carrier lost", nfSnap(false, true, "reachable"), "lost carrier"},
		{"lan ip gone", nfSnap(true, false, "reachable"), "LAN IP disappeared"},
		{"gateway arp incomplete", nfSnap(true, true, "incomplete"), "ARP table"},
		{"gateway arp absent", nfSnap(true, true, "absent"), "ARP table"},
		{"all healthy", healthy, ""},
	}
	for _, c := range cases {
		got := netDegradations(healthy, c.cur)
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("%s: expected no degradation, got %v", c.name, got)
			}
			continue
		}
		found := false
		for _, g := range got {
			if strings.Contains(g, c.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: expected a degradation containing %q, got %v", c.name, c.want, got)
		}
	}
}

// A box coming back UP must never read as a degradation, and must read as a
// recovery.
func TestNetRecoveryIsNotADegradation(t *testing.T) {
	down := nfSnap(false, false, "absent")
	up := nfSnap(true, true, "reachable")
	if d := netDegradations(down, up); len(d) != 0 {
		t.Errorf("recovery misread as degradation: %v", d)
	}
	if !netRecovered(down, up) {
		t.Error("netRecovered should be true when the box came back")
	}
	if netRecovered(up, up) {
		t.Error("a steady healthy box is not a recovery event")
	}
}

// An address change is reported with both sides, and an unchanged or merely
// reordered set is not a change.
func TestLanAddrChange(t *testing.T) {
	snap := func(addrs ...string) netForensicsSnapshot { return netForensicsSnapshot{LANAddrs: addrs} }
	for _, c := range []struct {
		name     string
		last     netForensicsSnapshot
		cur      netForensicsSnapshot
		from, to string
		changed  bool
	}{
		{"same address", snap("192.0.2.1"), snap("192.0.2.1"), "192.0.2.1", "192.0.2.1", false},
		{"new lease on another address", snap("192.0.2.1"), snap("192.0.2.7"), "192.0.2.1", "192.0.2.7", true},
		{"address lost", snap("192.0.2.1"), snap(), "192.0.2.1", "none", true},
		{"address back", snap(), snap("192.0.2.1"), "none", "192.0.2.1", true},
		{"still none", snap(), snap(), "none", "none", false},
		{"same set, other order", snap("192.0.2.1", "198.51.100.2"), snap("198.51.100.2", "192.0.2.1"), "192.0.2.1,198.51.100.2", "192.0.2.1,198.51.100.2", false},
	} {
		from, to, changed := lanAddrChange(c.last, c.cur)
		if from != c.from || to != c.to || changed != c.changed {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q, %v)", c.name, from, to, changed, c.from, c.to, c.changed)
		}
	}
}

func TestIsRFC1918(t *testing.T) {
	for _, ip := range []string{"192.168.1.21", "10.0.0.5", "172.16.0.1", "172.31.255.1"} {
		if !isRFC1918(ip) {
			t.Errorf("%s should be RFC1918", ip)
		}
	}
	// 203.0.113.x is the usb0 internal bridge link, must never count as the LAN.
	for _, ip := range []string{"203.0.113.1", "8.8.8.8", "169.254.1.1", "172.32.0.1"} {
		if isRFC1918(ip) {
			t.Errorf("%s should NOT be RFC1918", ip)
		}
	}
}
