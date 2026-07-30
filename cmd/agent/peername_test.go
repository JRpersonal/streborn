package main

import "testing"

// #494: a speaker rendered as "str-<ip>" in the picker is the same defect as a
// bare address, so the placeholder must be recognised and never a real name.
func TestPlaceholderPeerName(t *testing.T) {
	placeholders := []string{
		"str-192.0.2.10", "str-10.0.0.1", "str-192.168.178.49",
		// mDNS announces "STR-<last 6 of the device ID>", i.e. a MAC fragment,
		// which one reporter saw in the picker as "part of their MAC addresses".
		"STR-3E6CE1", "STR-c94a51", "str-AABBCC", "STR-488D",
	}
	for _, n := range placeholders {
		if !placeholderPeerName(n) {
			t.Errorf("%q must be recognised as a placeholder", n)
		}
	}
	real := []string{
		"Wohnzimmer", "SoundTouch 10b", "str-Kueche", "str-", "Arbeitszimmer300", "streborn",
		"STR-Bad",     // too short and not hex: a person chose this
		"str-office2", // letters beyond hex
		"STR",         // the bare service name, no separator
	}
	for _, n := range real {
		if placeholderPeerName(n) {
			t.Errorf("%q is a real name and must not be treated as a placeholder", n)
		}
	}
}

// A speaker that changes address must keep its identity in the roster: the
// entry moves with it, carrying the name already known, and no stale twin is
// left behind under the old address (#494, still reported on v0.9.25 after the
// stand-in names themselves were fixed).
func TestPeerKeepsItsNameAcrossAnAddressChange(t *testing.T) {
	peersMu.Lock()
	defer peersMu.Unlock()
	peersByIP = map[string]*peerEntry{
		"192.0.2.10": {name: "Kitchen", deviceID: "AABBCC001122", port: 8888},
	}

	e := adoptPeerEntryLocked("192.0.2.99", "AABBCC001122")

	if e.name != "Kitchen" {
		t.Fatalf("the moved speaker must keep its known name, got %q", e.name)
	}
	if e.port != 8888 {
		t.Fatalf("the moved speaker must keep its known port, got %d", e.port)
	}
	if _, stale := peersByIP["192.0.2.10"]; stale {
		t.Fatal("the old address must not be left behind as a second speaker")
	}
	if peersByIP["192.0.2.99"] != e {
		t.Fatal("the entry must now be reachable under the new address")
	}
	if len(peersByIP) != 1 {
		t.Fatalf("one speaker must stay one entry, got %d", len(peersByIP))
	}
}

// A genuinely new speaker still gets its own entry, and an unknown device ID
// must never adopt somebody else's name.
func TestUnknownPeerGetsItsOwnEntry(t *testing.T) {
	peersMu.Lock()
	defer peersMu.Unlock()
	peersByIP = map[string]*peerEntry{
		"192.0.2.10": {name: "Kitchen", deviceID: "AABBCC001122"},
	}

	e := adoptPeerEntryLocked("192.0.2.50", "DDEEFF334455")

	if e.name != "" {
		t.Fatalf("a speaker we have never seen must not inherit a name, got %q", e.name)
	}
	if len(peersByIP) != 2 {
		t.Fatalf("expected two speakers, got %d", len(peersByIP))
	}
}

// Without a device ID there is nothing to match on, so the entry must be new
// rather than stealing the name of whichever speaker happens to be listed.
func TestPeerWithoutDeviceIDDoesNotAdopt(t *testing.T) {
	peersMu.Lock()
	defer peersMu.Unlock()
	peersByIP = map[string]*peerEntry{
		"192.0.2.10": {name: "Kitchen", deviceID: "AABBCC001122"},
	}

	e := adoptPeerEntryLocked("192.0.2.77", "")

	if e.name != "" {
		t.Fatalf("an unidentified speaker must not adopt a name, got %q", e.name)
	}
	if len(peersByIP) != 2 {
		t.Fatalf("expected two speakers, got %d", len(peersByIP))
	}
}
