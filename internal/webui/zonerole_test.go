package webui

import "testing"

// The role decision is what keeps a group working. Getting it backwards would
// silence the master (the one speaker the whole group is listening to) while
// leaving the follower being fought, so it is worth pinning down directly.
func TestZoneRoleFromMaster(t *testing.T) {
	const self = "EC24B8B790CC"

	tests := []struct {
		name         string
		master, id   string
		wantFollower bool
		wantKnown    bool
	}{
		{"master leads its own zone", self, self, false, true},
		{"master id in another case still leads", "ec24b8b790cc", self, false, true},
		{"a different master means we follow", "000C8A96488D", self, true, true},
		{"no zone at all", "", self, false, false},
		{"in a zone but our own id is unknown", "000C8A96488D", "", false, false},
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
	const master, follower = "852F08E8AAAA", "C4FC5074BBBB"

	if isFollower, known := zoneRoleFromMaster(master, master); !known || isFollower {
		t.Error("the speaker whose id IS the master must not be treated as a follower, whatever senderIsMaster says")
	}
	if isFollower, known := zoneRoleFromMaster(master, follower); !known || !isFollower {
		t.Error("a speaker whose id differs from the master must be treated as a follower")
	}
}
