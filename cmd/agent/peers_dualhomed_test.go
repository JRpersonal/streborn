package main

import (
	"testing"
	"time"
)

// #165 on the phone page: a speaker on the cable and on Wi-Fi sits in the
// roster under both addresses, one of them possibly still under its
// "str-<ip>" placeholder name. It must be listed once.
func TestCachedPeerLinksCollapsesSameDevice(t *testing.T) {
	now := time.Now()
	peersMu.Lock()
	peersByIP = map[string]*peerEntry{
		// The app's seed (cable address), named, firmware id.
		"192.0.2.20": {name: "Wohnzimmer", boxDeviceID: "AABBCCDDEEFF", reachable: true, lastSeen: now, port: 8888},
		// mDNS at the Wi-Fi address, placeholder name, same firmware id plus the
		// agent's own (different case on purpose).
		"192.0.2.21": {name: "str-192.0.2.21", deviceID: "112233445566", boxDeviceID: "aabbccddeeff", reachable: true, lastSeen: now.Add(-time.Second), port: 8888},
		// Another speaker entirely.
		"192.0.2.30": {name: "Küche", deviceID: "665544332211", reachable: true, lastSeen: now, port: 8888},
	}
	peersMu.Unlock()
	t.Cleanup(func() {
		peersMu.Lock()
		peersByIP = map[string]*peerEntry{}
		peersMu.Unlock()
	})

	links := cachedPeerLinks()
	got := map[string]string{}
	for _, l := range links {
		got[l.IP] = l.Name
	}
	if len(links) != 2 || got["192.0.2.20"] != "Wohnzimmer" || got["192.0.2.30"] != "Küche" {
		t.Fatalf("want Wohnzimmer once (fresher, cable) and Küche, got %v", got)
	}
}

// A dimmed entry for the same device never hides the reachable one.
func TestCachedPeerLinksSameDevicePrefersReachable(t *testing.T) {
	now := time.Now()
	peersMu.Lock()
	peersByIP = map[string]*peerEntry{
		"192.0.2.20": {name: "Wohnzimmer", deviceID: "AABBCCDDEEFF", reachable: false, lastSeen: now, port: 8888},
		"192.0.2.21": {name: "str-192.0.2.21", deviceID: "AABBCCDDEEFF", reachable: true, lastSeen: now.Add(-time.Minute), port: 8888},
	}
	peersMu.Unlock()
	t.Cleanup(func() {
		peersMu.Lock()
		peersByIP = map[string]*peerEntry{}
		peersMu.Unlock()
	})

	links := cachedPeerLinks()
	if len(links) != 1 || links[0].IP != "192.0.2.21" {
		t.Fatalf("want only the reachable address, got %+v", links)
	}
}
