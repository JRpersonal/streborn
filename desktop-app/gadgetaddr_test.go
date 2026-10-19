package main

import "testing"

// A SoundTouch 30 whose agent announced before its Wi-Fi lease published only
// its USB gadget address, 192.0.2.1, and the app showed it as a third speaker
// (2026-10-19). Every documentation range is the gadget, never the LAN.
func TestPickReachableIPSkipsEveryDocumentationRange(t *testing.T) {
	for _, ip := range []string{"192.0.2.1", "198.51.100.7", "203.0.113.1"} {
		if got := pickReachableIP([]string{ip}); got != "" {
			t.Errorf("pickReachableIP([%s]) = %q, want none", ip, got)
		}
		if got := pickReachableIP([]string{ip, "192.168.178.29"}); got != "192.168.178.29" {
			t.Errorf("pickReachableIP([%s, LAN]) = %q, want the LAN address", ip, got)
		}
	}
}
