package main

import (
	"net"
	"testing"
)

// A broadcast address that slipped into the candidate list showed up as a
// "phantom speaker" and made Update All report an error while the real speakers
// updated fine (field 2026-09-04). ipIsBroadcastOf is the mask math that decides
// what to filter, and the point of computing it per mask is that .255 is the
// broadcast of a /24 but a perfectly ordinary host on a /23.
func TestIPIsBroadcastOf(t *testing.T) {
	mustNet := func(cidr string) *net.IPNet {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatalf("bad cidr %s: %v", cidr, err)
		}
		return n
	}
	cases := []struct {
		ip   string
		cidr string
		want bool
	}{
		{"192.168.0.255", "192.168.0.10/24", true},  // /24 broadcast -> filtered
		{"192.168.0.1", "192.168.0.10/24", false},   // ordinary host
		{"192.168.0.10", "192.168.0.10/24", false},  // the interface itself
		{"192.168.0.255", "192.168.0.10/23", false}, // /23: .255 is a valid host, keep it
		{"192.168.1.255", "192.168.0.10/23", true},  // /23 broadcast is .1.255
		{"10.0.0.255", "10.0.0.5/24", true},
		{"10.1.2.3", "10.0.0.5/8", false}, // /8 broadcast is 10.255.255.255
		{"10.255.255.255", "10.0.0.5/8", true},
	}
	for _, c := range cases {
		got := ipIsBroadcastOf(net.ParseIP(c.ip), mustNet(c.cidr))
		if got != c.want {
			t.Errorf("ipIsBroadcastOf(%s, %s) = %v, want %v", c.ip, c.cidr, got, c.want)
		}
	}
}
