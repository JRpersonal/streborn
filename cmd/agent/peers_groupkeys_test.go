// Tests for the roster lookups the group-key toggle resolves a template's
// speakers with: a template stores the addresses of the day it was saved, and
// a DHCP renumbering hands those to other speakers.

package main

import "testing"

func TestPeerRosterLookupsByDeviceIDAndAddress(t *testing.T) {
	peersMu.Lock()
	peersByIP = map[string]*peerEntry{
		"192.0.2.21": {name: "Living room", deviceID: "AAAA", port: 17008, reachable: true},
		"192.0.2.35": {name: "Kitchen", deviceID: "BBBB", port: 8888},
		"192.0.2.36": {name: "Kitchen (old)", deviceID: "bbbb", port: 8888, reachable: true},
		"192.0.2.40": {name: "Nameless"},
	}
	peersMu.Unlock()
	t.Cleanup(func() {
		peersMu.Lock()
		peersByIP = map[string]*peerEntry{}
		peersMu.Unlock()
	})

	if got := peerIPByDeviceID("AAAA"); got != "192.0.2.21" {
		t.Fatalf("AAAA: got %q", got)
	}
	// Case-insensitive, and the reachable entry wins over the dimmed one.
	if got := peerIPByDeviceID("BBBB"); got != "192.0.2.36" {
		t.Fatalf("BBBB: got %q, the reachable entry must win", got)
	}
	if got := peerIPByDeviceID("CCCC"); got != "" {
		t.Fatalf("unknown id must resolve to nothing, got %q", got)
	}
	if got := peerIPByDeviceID(""); got != "" {
		t.Fatalf("empty id must resolve to nothing, got %q", got)
	}
	if got := peerDeviceIDAt("192.0.2.21"); got != "AAAA" {
		t.Fatalf("id at .21: got %q", got)
	}
	if got := peerDeviceIDAt("192.0.2.40"); got != "" {
		t.Fatalf("a peer without a known id must report none, got %q", got)
	}
	if got := peerDeviceIDAt("192.0.2.99"); got != "" {
		t.Fatalf("an unlisted address must report none, got %q", got)
	}
}
