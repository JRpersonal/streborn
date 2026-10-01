package webui

import (
	"testing"

	"github.com/JRpersonal/streborn/internal/zones"
)

func zone(master string, slaves ...string) zones.Zone {
	z := zones.Zone{Master: master}
	for _, s := range slaves {
		z.Slaves = append(z.Slaves, zones.Member{DeviceID: s})
	}
	return z
}

// The replacement warning only earns its place if it stays quiet for a re-form
// of the same group. A permanent group is re-asserted on every play, so a
// warning on each of those would bury the one case it exists for.
func TestSameMembersIgnoresOrder(t *testing.T) {
	if !sameMembers(zone("m", "a", "b"), zone("m", "b", "a")) {
		t.Error("the same two members in the other order read as a different group")
	}
	if !sameMembers(zone("m"), zone("m")) {
		t.Error("two empty member lists read as different")
	}
}

func TestSameMembersSeesARealChange(t *testing.T) {
	// The reported case: a pair replaced by a group of three.
	if sameMembers(zone("m", "a", "b"), zone("m", "a", "b", "c")) {
		t.Error("a third speaker was not noticed")
	}
	if sameMembers(zone("m", "a", "b"), zone("m", "a")) {
		t.Error("a removed speaker was not noticed")
	}
	if sameMembers(zone("m", "a", "b"), zone("m", "a", "c")) {
		t.Error("a swapped speaker was not noticed")
	}
	// A duplicate must not pass as a match for two distinct members.
	if sameMembers(zone("m", "a", "b"), zone("m", "a", "a")) {
		t.Error("a duplicated member passed as the same set")
	}
}

func TestSameMasterZone(t *testing.T) {
	if !sameMasterZone(zone("m", "a"), zone("m", "b")) {
		t.Error("same master read as different")
	}
	if sameMasterZone(zone("m", "a"), zone("other", "a")) {
		t.Error("different masters read as the same")
	}
	// An empty master is not a match for another empty one: that would make
	// every unidentified record look like a replacement of every other.
	if sameMasterZone(zone("", "a"), zone("", "b")) {
		t.Error("two empty masters read as the same speaker")
	}
}
