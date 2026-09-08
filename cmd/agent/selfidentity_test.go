package main

import (
	"log/slog"
	"testing"
	"time"
)

// announcerSelfDeviceIDs is the function main.go itself calls inside the roster's
// self seam, so these run the real composition and not a copy of it.
// TestPurgeSelfPeers and TestSeedPeersDropsSelf set the seam themselves and
// therefore prove only the comparison; these prove what the comparison is fed.

// The announcer is created seconds after the roster seams are wired, so the
// list has to survive being asked before it exists - and it must still name this
// speaker, because the boot id is announced from the very first record.
func TestSelfDeviceIDsBeforeTheAnnouncerExists(t *testing.T) {
	got := announcerSelfDeviceIDs(nil, "AABBCCDDEEFF")
	if len(got) != 1 || got[0] != "AABBCCDDEEFF" {
		t.Fatalf("self ids = %v, want just the boot id", got)
	}
}

// The announced values come first and the boot id stays in the list. Both
// matter: the announced deviceID is what a stale self-announcement carries
// (#697), the firmware id is what the desktop app addresses a speaker by when
// it could read one, and the boot id keeps an entry adopted from an earlier
// announcement recognisable.
func TestSelfDeviceIDsCollectsEveryIdentityOnce(t *testing.T) {
	got := selfDeviceIDs("AABBCCDDEEFF",
		func() string { return "aabbccddeeff" }, // the same box, announced
		func() string { return "112233445566" }, // the firmware's SoundTouch id
	)
	if len(got) != 2 {
		t.Fatalf("self ids = %v, want the two distinct identities", got)
	}
	if got[0] != "aabbccddeeff" || got[1] != "112233445566" {
		t.Fatalf("self ids = %v, want the announced ones first", got)
	}
}

// A value that is not there yet must not become an identity: an empty firmware
// id would otherwise match every roster entry that carries no id and empty the
// roster.
func TestSelfDeviceIDsDropsEmpties(t *testing.T) {
	got := selfDeviceIDs("", func() string { return "" }, nil, func() string { return "  " })
	if len(got) != 0 {
		t.Fatalf("self ids = %v, want none", got)
	}
	if matchesSelfDeviceIDWith(nil, "") || matchesSelfDeviceIDWith([]string{""}, "") {
		t.Fatal("an empty id matched this speaker")
	}
}

// matchesSelfDeviceIDWith runs the roster's self-check against a fixed list, so
// the case rules can be tested without touching the package seam.
func matchesSelfDeviceIDWith(ids []string, id string) bool {
	prev := peerSelfDeviceIDsFn
	defer func() { peerSelfDeviceIDsFn = prev }()
	peerSelfDeviceIDsFn = func() []string { return ids }
	return matchesSelfDeviceID(id)
}

// The ids reach the roster from three sources with three casing habits (the
// mDNS browse uppercases, the firmware answers as it likes, the app forwards
// what it stored), so the comparison cannot be a plain ==.
func TestMatchesSelfDeviceIDIgnoresCase(t *testing.T) {
	ids := []string{"AABBCCDDEEFF", "112233445566"}
	for _, id := range []string{"aabbccddeeff", "AABBCCDDEEFF", " 112233445566 "} {
		if !matchesSelfDeviceIDWith(ids, id) {
			t.Errorf("%q was not recognised as this speaker", id)
		}
	}
	if matchesSelfDeviceIDWith(ids, "999999999999") {
		t.Error("another speaker's id was taken for this one")
	}
}

// The firmware id is an identity of this box too, so a roster entry that names
// the box by it is still us. That is the shape the desktop app produces: it
// resolves a speaker's id from the firmware where it can, so its seeds and zone
// rows can name a box by an id its own mDNS record never carried.
func TestPurgeSelfPeersAlsoDropsTheFirmwareIdentity(t *testing.T) {
	prevDev, prevName := peerSelfDeviceIDsFn, peerSelfNameFn
	defer func() { peerSelfDeviceIDsFn, peerSelfNameFn = prevDev, prevName }()
	peerSelfDeviceIDsFn = func() []string {
		return selfDeviceIDs("AABBCCDDEEFF",
			func() string { return "AABBCCDDEEFF" },
			func() string { return "112233445566" })
	}
	peerSelfNameFn = func() string { return "Küche" }

	peersMu.Lock()
	peersByIP = map[string]*peerEntry{
		"192.0.2.174": {name: "str-192.0.2.174", deviceID: "112233445566", lastSeen: time.Now()}, // us, by the firmware id
		"192.0.2.175": {name: "str-192.0.2.175", deviceID: "AABBCCDDEEFF", lastSeen: time.Now()}, // us, by the announced id
		"192.0.2.100": {name: "Wohnzimmer", deviceID: "999999999999", lastSeen: time.Now()},      // a real peer
	}
	peersMu.Unlock()

	purgeSelfPeers(slog.Default())

	peersMu.Lock()
	defer peersMu.Unlock()
	if len(peersByIP) != 1 {
		t.Fatalf("want only the real peer left, got %d: %v", len(peersByIP), peersByIP)
	}
	if _, ok := peersByIP["192.0.2.100"]; !ok {
		t.Fatal("the real peer was purged")
	}
}
