package webui

import (
	"context"
	"errors"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// #1752: a pair renamed with "Save name" showed in the Spotify app as
// "Kitchen pair (STR)" next to "Stereo Pair (L+R)". The rename reached the
// marge record (STR's engine) but never the firmware, which advertises the
// native "(L+R)" entry from its own group state. These tests pin who gets the
// /updateGroup and with what.

type fakePairFirmware struct {
	group     boxapi.Group
	groupErr  error
	selfID    string
	infoErr   error
	updateErr error
	updates   []boxapi.Group
}

func (f *fakePairFirmware) GetGroup(context.Context) (boxapi.Group, error) {
	return f.group, f.groupErr
}

func (f *fakePairFirmware) GetInfo(context.Context) (boxapi.Info, error) {
	return boxapi.Info{DeviceID: f.selfID}, f.infoErr
}

func (f *fakePairFirmware) UpdateGroup(_ context.Context, g boxapi.Group) error {
	f.updates = append(f.updates, g)
	return f.updateErr
}

func livePair() boxapi.Group {
	return boxapi.Group{
		ID: "str-grp-AAAA", Name: "Stereo Pair", MasterDeviceID: "AAAA", Status: "GROUP_OK",
		Members: []boxapi.ZoneMember{
			{DeviceID: "AAAA", IP: "10.0.0.11", Role: "LEFT"},
			{DeviceID: "BBBB", IP: "10.0.0.12", Role: "RIGHT"},
		},
	}
}

func noDial(t *testing.T) func(string) pairFirmware {
	return func(ip string) pairFirmware {
		t.Errorf("dialled %s, but the master's own firmware should have taken the rename", ip)
		return nil
	}
}

// The master renames its own firmware with the whole pair document: same id,
// same roles, new name.
func TestPairRenameOnTheMasterUpdatesItsOwnFirmware(t *testing.T) {
	fw := &fakePairFirmware{group: livePair(), selfID: "aaaa"}
	got, err := renamePairOnFirmware(context.Background(), fw, noDial(t), "  Kitchen pair ")
	if err != nil || got != pairFirmwareRenamed {
		t.Fatalf("outcome = %q, %v; want renamed", got, err)
	}
	if len(fw.updates) != 1 {
		t.Fatalf("updates = %d, want exactly one /updateGroup", len(fw.updates))
	}
	u := fw.updates[0]
	if u.Name != "Kitchen pair" || u.ID != "str-grp-AAAA" || u.MasterDeviceID != "AAAA" || len(u.Members) != 2 {
		t.Errorf("update carried %+v, want the live pair with only the name changed", u)
	}
}

// The reporter's master already reported the new name from /getGroup (it
// re-reads marge at boot) while its native Spotify entry still had the old one.
// A matching name must therefore not short-circuit the update.
func TestPairRenameIsSentEvenWhenTheFirmwareAlreadyShowsTheName(t *testing.T) {
	g := livePair()
	g.Name = "Kitchen pair"
	fw := &fakePairFirmware{group: g, selfID: "AAAA"}
	got, _ := renamePairOnFirmware(context.Background(), fw, noDial(t), "Kitchen pair")
	if got != pairFirmwareRenamed || len(fw.updates) != 1 {
		t.Fatalf("outcome = %q with %d updates; the native entry only follows an /updateGroup", got, len(fw.updates))
	}
}

// A client that renames through the following half alone still reaches the
// master, at the address the firmware's own pair document gives for it.
func TestPairRenameOnTheFollowerGoesToTheMaster(t *testing.T) {
	local := &fakePairFirmware{group: livePair(), selfID: "BBBB"}
	master := &fakePairFirmware{}
	var dialled string
	dial := func(ip string) pairFirmware { dialled = ip; return master }

	got, err := renamePairOnFirmware(context.Background(), local, dial, "Kitchen pair")
	if err != nil || got != pairFirmwareRenamed {
		t.Fatalf("outcome = %q, %v; want renamed", got, err)
	}
	if dialled != "10.0.0.11" {
		t.Errorf("dialled %q, want the master's address from the pair document", dialled)
	}
	if len(local.updates) != 0 || len(master.updates) != 1 || master.updates[0].Name != "Kitchen pair" {
		t.Errorf("local updates %d, master updates %+v; only the master may get the /updateGroup", len(local.updates), master.updates)
	}
}

// The master's address comes from the firmware, but it is still a dial, so it
// is held to the LAN like every other peer address.
func TestPairRenameRefusesAMasterAddressOffTheLAN(t *testing.T) {
	g := livePair()
	g.Members[0].IP = "127.0.0.1"
	local := &fakePairFirmware{group: g, selfID: "BBBB"}
	got, _ := renamePairOnFirmware(context.Background(), local, noDial(t), "Kitchen pair")
	if got != pairFirmwareBadMaster {
		t.Fatalf("outcome = %q, want %q", got, pairFirmwareBadMaster)
	}
}

// Nothing is sent when there is nothing to rename or nothing to rename it to.
func TestPairRenameLeavesTheFirmwareAloneWithoutAPairOrAName(t *testing.T) {
	cases := []struct {
		name string
		fw   *fakePairFirmware
		give string
		want string
	}{
		{"empty name", &fakePairFirmware{group: livePair(), selfID: "AAAA"}, "   ", pairFirmwareNoName},
		{"standalone speaker", &fakePairFirmware{selfID: "AAAA"}, "Kitchen pair", pairFirmwareNoPair},
		{"pair of one", &fakePairFirmware{group: boxapi.Group{ID: "x", MasterDeviceID: "AAAA",
			Members: []boxapi.ZoneMember{{DeviceID: "AAAA"}}}, selfID: "AAAA"}, "Kitchen pair", pairFirmwareNoPair},
		{"group unreadable", &fakePairFirmware{groupErr: errors.New("timeout")}, "Kitchen pair", pairFirmwareUnread},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := renamePairOnFirmware(context.Background(), c.fw, noDial(t), c.give)
			if got != c.want {
				t.Errorf("outcome = %q, want %q", got, c.want)
			}
			if len(c.fw.updates) != 0 {
				t.Errorf("sent %d /updateGroup calls, want none", len(c.fw.updates))
			}
		})
	}
}

// A refused update is reported, not swallowed, so the log says why the native
// entry kept its old name.
func TestPairRenameReportsARefusedUpdate(t *testing.T) {
	fw := &fakePairFirmware{group: livePair(), selfID: "AAAA", updateErr: errors.New("500")}
	got, err := renamePairOnFirmware(context.Background(), fw, noDial(t), "Kitchen pair")
	if got != pairFirmwareRefused || err == nil {
		t.Fatalf("outcome = %q, %v; want update-failed with the error", got, err)
	}
}
