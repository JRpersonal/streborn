package marge

import (
	"encoding/xml"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// What counts as a stereo pair, and what a speaker does with something that is
// not one.
//
// A six-speaker bundle on 2026-09-27 caught both halves of one pair holding a
// record that named ITSELF as master with zero roles, with different group ids,
// for about twenty minutes after the master's update reboot. The firmware posts
// such a document on every boot-time re-register, from its own point of view,
// and it went into the store unchecked.
//
// The rule that would have refused it existed, inline in SetCanonicalGroup, so
// it applied to documents STR installs and to nothing else. Two comments in this
// tree, mine, claimed a validateGroup function enforced it generally. No such
// function existed. This is that rule, made real.
//
// The reason it matters beyond tidiness: GroupPair answered yes to those
// records, so both halves told Spotify they led the pair and neither stood down,
// which is the two-entries symptom #976 was about, arriving through a new door.

func quietServer(t *testing.T) *Server {
	t.Helper()
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// The shape the firmware posts when it re-registers itself: a master, no roles.
const selfOnlyDoc = `<group id="str-grp-AABBCCDDEEFF"><name>Grace pair</name><masterDeviceId>AABBCCDDEEFF</masterDeviceId><roles></roles></group>`

// A real pair.
const realPairDoc = `<group id="str-grp-AABBCCDDEEFF"><name>Grace pair</name><masterDeviceId>AABBCCDDEEFF</masterDeviceId><roles><groupRole><deviceId>AABBCCDDEEFF</deviceId><role>LEFT</role><ipAddress>192.0.2.10</ipAddress></groupRole><groupRole><deviceId>112233445566</deviceId><role>RIGHT</role><ipAddress>192.0.2.11</ipAddress></groupRole></roles></group>`

func TestADocumentWithNoRolesIsNotAPair(t *testing.T) {
	var g groupRecord
	if err := unmarshalGroup(t, selfOnlyDoc, &g); err != nil {
		t.Fatal(err)
	}
	if describesPair(&g) {
		t.Error("a document naming itself master with no roles was accepted as a stereo pair")
	}
	var real groupRecord
	if err := unmarshalGroup(t, realPairDoc, &real); err != nil {
		t.Fatal(err)
	}
	if !describesPair(&real) {
		t.Error("a document with a master and two roles was refused")
	}
	if describesPair(nil) {
		t.Error("nil was accepted as a pair")
	}
}

func TestGroupPairRefusesARecordThatIsNotAPair(t *testing.T) {
	s := quietServer(t)
	// Straight into the store, the way a firmware post used to land there.
	var g groupRecord
	if err := unmarshalGroup(t, selfOnlyDoc, &g); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.group = &g
	s.mu.Unlock()

	if _, _, ok := s.GroupPair(); ok {
		t.Fatal("GroupPair reported a pair for a record with no roles; both halves of a split pair would claim to lead it")
	}

	// And the real thing still reads as a pair.
	if err := s.SetCanonicalGroup(realPairDoc); err != nil {
		t.Fatalf("SetCanonicalGroup refused a real pair: %v", err)
	}
	master, name, ok := s.GroupPair()
	if !ok {
		t.Fatal("GroupPair refused a real pair")
	}
	if master != "AABBCCDDEEFF" || name != "Grace pair" {
		t.Errorf("GroupPair = %q/%q, want the master and name from the document", master, name)
	}
}

func TestSetCanonicalGroupStillRefusesTheSameShape(t *testing.T) {
	s := quietServer(t)
	err := s.SetCanonicalGroup(selfOnlyDoc)
	if err == nil {
		t.Fatal("SetCanonicalGroup accepted a document with no roles")
	}
	if !strings.Contains(err.Error(), "two roles") {
		t.Errorf("error does not say what is wrong with it: %v", err)
	}
}

// unmarshalGroup parses a group document the way the server does, so the tests
// exercise the real shape rather than a hand-built struct.
func unmarshalGroup(t *testing.T, doc string, into *groupRecord) error {
	t.Helper()
	return xml.Unmarshal([]byte(doc), into)
}
