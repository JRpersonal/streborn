package main

import (
	"testing"
	"time"
)

// A peer restored from NAND without a name was invisible forever: the listing
// skips a nameless entry, and the only code that asks a speaker its name asked
// only when the name was a "str-..." placeholder. Live on 2026-08-15, one
// speaker offered four of the five peers it had restored, and the missing one
// was up and reachable the whole time.
func TestANamelessPeerIsProbedForItsName(t *testing.T) {
	peersMu.Lock()
	peersByIP = map[string]*peerEntry{
		"192.0.2.11": {name: "", lastSeen: time.Now()},                     // nameless and fresh
		"192.0.2.12": {name: "Kitchen", lastSeen: time.Now()},              // named and fresh
		"192.0.2.13": {name: "str-192.0.2.13", lastSeen: time.Now()},       // placeholder, fresh
		"192.0.2.14": {name: "Bath", lastSeen: time.Now().Add(-time.Hour)}, // named but stale
	}
	peersMu.Unlock()
	t.Cleanup(func() {
		peersMu.Lock()
		peersByIP = map[string]*peerEntry{}
		peersProbing = false
		peersMu.Unlock()
	})

	got := probeCandidates()
	in := func(ip string) bool {
		for _, c := range got {
			if c == ip {
				return true
			}
		}
		return false
	}
	if !in("192.0.2.11") {
		t.Error("a nameless peer must be probed however fresh it is: nothing else will ever name it")
	}
	if !in("192.0.2.14") {
		t.Error("a stale peer is still a candidate, that was the original purpose")
	}
	if in("192.0.2.12") {
		t.Error("a named, fresh peer needs nothing")
	}
}
