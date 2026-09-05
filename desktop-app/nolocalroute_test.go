package main

import (
	"errors"
	"strings"
	"testing"
)

// EADDRNOTAVAIL ("can't assign requested address") is the PC failing to open a
// local socket, not the speaker refusing the connection, so it must get the
// PC-network advice, not the firewall/same-Wi-Fi advice that sends the user
// hunting in the wrong place (field 2026-09-05: a Mac hit it against the speaker,
// the router and a media server alike while still receiving their multicast).
func TestNoLocalRouteAdvice(t *testing.T) {
	yes := []string{
		"dial tcp 192.168.1.27:8888: connect: can't assign requested address",
		"dial tcp 10.0.0.5:8090: connect: cannot assign requested address",
		"connectex: The requested address is not valid in its context.", // Windows WSAEADDRNOTAVAIL
	}
	for _, m := range yes {
		if !noLocalRoute(errors.New(m)) {
			t.Errorf("noLocalRoute should match %q", m)
		}
		got := reachabilityHint(errors.New(m)).Error()
		if !strings.Contains(got, noLocalRouteAdvice) {
			t.Errorf("reachabilityHint(%q) should carry the PC-network advice, got %q", m, got)
		}
		if strings.Contains(got, firewallAdvice) {
			t.Errorf("reachabilityHint(%q) must NOT carry the firewall advice", m)
		}
	}
	no := []string{
		"dial tcp 192.168.1.27:8888: connect: connection refused",
		"dial tcp 192.168.1.27:8888: i/o timeout",
		"no route to host",
	}
	for _, m := range no {
		if noLocalRoute(errors.New(m)) {
			t.Errorf("noLocalRoute should NOT match %q", m)
		}
	}
}
