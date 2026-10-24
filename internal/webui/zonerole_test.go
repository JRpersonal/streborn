package webui

import "testing"

// The role decision is what keeps a group working. Getting it backwards would
// silence the master (the one speaker the whole group is listening to) while
// leaving the follower being fought, so it is worth pinning down directly.
func TestZoneRoleFromMaster(t *testing.T) {
	const self = "0000000000A1"

	tests := []struct {
		name         string
		master, id   string
		wantFollower bool
		wantKnown    bool
	}{
		{"master leads its own zone", self, self, false, true},
		{"master id in another case still leads", "0000000000a1", self, false, true},
		{"a different master means we follow", "0000000000C3", self, true, true},
		{"no zone at all", "", self, false, false},
		{"in a zone but our own id is unknown", "0000000000C3", "", false, false},
		{"whitespace is not an id", "  ", self, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			follower, known := zoneRoleFromMaster(tc.master, tc.id)
			if follower != tc.wantFollower || known != tc.wantKnown {
				t.Errorf("zoneRoleFromMaster(%q, %q) = (follower=%v, known=%v), want (%v, %v)",
					tc.master, tc.id, follower, known, tc.wantFollower, tc.wantKnown)
			}
		})
	}
}

// The firmware's own senderIsMaster attribute reads the opposite way round to
// its name on the chassis in the field report: the speaker that formed and
// leads the zone logs false, the follower logs true. This test exists to state
// why the role is derived from the deviceIDs instead, so nobody "simplifies"
// it back to the attribute.
func TestZoneRoleIgnoresTheFirmwareSenderFlag(t *testing.T) {
	const master, follower = "0000000003A1", "0000000003B2"

	if isFollower, known := zoneRoleFromMaster(master, master); !known || isFollower {
		t.Error("the speaker whose id IS the master must not be treated as a follower, whatever senderIsMaster says")
	}
	if isFollower, known := zoneRoleFromMaster(master, follower); !known || !isFollower {
		t.Error("a speaker whose id differs from the master must be treated as a follower")
	}
}
