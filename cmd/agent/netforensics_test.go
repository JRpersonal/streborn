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

func TestIsRFC1918(t *testing.T) {
	for _, ip := range []string{"192.168.178.21", "10.10.50.5", "172.16.0.1", "172.31.255.1"} {
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
