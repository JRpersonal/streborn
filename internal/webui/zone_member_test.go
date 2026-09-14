package webui

import (
	"testing"

	"github.com/JRpersonal/streborn/internal/zones"
)

// The stereo section now tells an owner whose SoundTouch 10s are all in saved
// groups to "remove two of them from their group first, then pair them". Until
// this endpoint existed the app had no way to do that: the only control on a
// saved group was the x that deletes the whole group (#938).

func TestAMemberIsFoundByAddressOrByDeviceID(t *testing.T) {
	in := []zones.Member{
		{DeviceID: "DEV-A", IP: "192.0.2.11"},
		{DeviceID: "DEV-B", IP: "192.0.2.12"},
		{DeviceID: "DEV-C", IP: "192.0.2.13"},
	}
	// A two-chip speaker announces a different deviceID over discovery than the
	// one the firmware lists, so a saved group can only be trusted to carry the
	// address. Both have to work.
	for _, c := range []struct{ ip, dev, want string }{
		{"192.0.2.12", "", "DEV-B"},
		{"", "DEV-C", "DEV-C"},
		{"192.0.2.11", "DEV-C", "DEV-A"}, // the address wins, it is the reliable one
	} {
		kept, removed := withoutMember(in, c.ip, c.dev)
		if removed == nil || removed.DeviceID != c.want {
			t.Fatalf("ip=%q dev=%q removed %v, want %s", c.ip, c.dev, removed, c.want)
		}
		if len(kept) != 2 {
			t.Errorf("ip=%q dev=%q kept %d, want 2", c.ip, c.dev, len(kept))
		}
		for _, k := range kept {
			if k.DeviceID == c.want {
				t.Errorf("the removed member is still in the list: %v", kept)
			}
		}
	}
}

// Removing somebody who is not in the group must change nothing, or a typo in
// an address would silently shorten the user's group.
func TestRemovingSomebodyElseChangesNothing(t *testing.T) {
	in := []zones.Member{{DeviceID: "DEV-A", IP: "192.0.2.11"}}
	kept, removed := withoutMember(in, "192.0.2.99", "")
	if removed != nil {
		t.Fatalf("removed %v, want nothing", removed)
	}
	if len(kept) != 1 {
		t.Errorf("kept %d, want the group untouched", len(kept))
	}
}

// Only the named member goes, even when the same speaker somehow appears twice.
func TestOnlyOneMemberIsTakenPerCall(t *testing.T) {
	in := []zones.Member{
		{DeviceID: "DEV-A", IP: "192.0.2.11"},
		{DeviceID: "DEV-A", IP: "192.0.2.11"},
	}
	kept, removed := withoutMember(in, "192.0.2.11", "")
	if removed == nil {
		t.Fatal("nothing removed")
	}
	if len(kept) != 1 {
		t.Errorf("kept %d, want 1: a single call removes a single member", len(kept))
	}
}

// Matching must not depend on the casing or the padding a document happens to
// carry: these values are written by several different code paths.
func TestMatchingIgnoresCaseAndPadding(t *testing.T) {
	in := []zones.Member{{DeviceID: "dev-a", IP: " 192.0.2.11 "}}
	if _, removed := withoutMember(in, "192.0.2.11", ""); removed == nil {
		t.Error("padded address did not match")
	}
	if _, removed := withoutMember(in, "", "DEV-A"); removed == nil {
		t.Error("differently cased deviceID did not match")
	}
}

// An empty request must not be read as "remove the first one".
func TestNamingNobodyRemovesNobody(t *testing.T) {
	in := []zones.Member{{DeviceID: "DEV-A", IP: "192.0.2.11"}}
	if _, removed := withoutMember(in, "", ""); removed != nil {
		t.Errorf("removed %v on an empty request", removed)
	}
}
