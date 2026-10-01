package webui

import "testing"

// While a speaker is a FOLLOWER in a group the firmware refuses every display
// write with UPnP 501 "Can't control member of group", so the track-title
// setting is stored but dormant and only the group master's setting has any
// effect. The app used to answer plain success either way.
//
// A reporter proved it in the right order, six speakers, 2026-09-29: switching
// it OFF on the Wave changed nothing; switching it ON at the master put artist
// and title on the Wave. He only got there by trying the other speaker on a
// hunch, because the screen presents the setting per speaker while its effect is
// per group.
func TestTheRoleDecidesWhetherTheSettingCanDoAnything(t *testing.T) {
	const self = "DEV#2bce1a92"
	const other = "DEV#f910b818"

	cases := []struct {
		name     string
		master   string
		follower bool
		known    bool
	}{
		{"no zone at all: the setting is live", "", false, false},
		{"this speaker leads: the setting is live", self, false, true},
		{"this speaker follows: the setting is dormant", other, true, true},
		{"in a zone but our own id is unknown", other, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			selfID := self
			if !c.known && c.master != "" {
				selfID = "" // in a zone, but we cannot tell which end
			}
			follower, known := zoneRoleFromMaster(c.master, selfID)
			if follower != c.follower || known != c.known {
				t.Errorf("follower=%v known=%v, want %v/%v", follower, known, c.follower, c.known)
			}
		})
	}
}

// An unreadable zone must count as ACTIVE. The setting usually is live, and
// claiming otherwise on a failed read would replace one wrong answer with
// another, this time on the speakers that are working fine.
func TestAnUnknownRoleLeavesTheSettingActive(t *testing.T) {
	follower, known := zoneRoleFromMaster("", "")
	if known {
		t.Fatal("an empty zone must not read as a known role")
	}
	if follower {
		t.Error("an unknown role must not read as follower, or every speaker with an unreadable zone would be told its setting does nothing")
	}
}
