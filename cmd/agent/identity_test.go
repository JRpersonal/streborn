package main

import (
	"testing"

	"github.com/JRpersonal/streborn/internal/boxsnapshot"
)

// Field case 2026-11-05: after an agent update a Wave's Bose firmware did not
// start, so /info never answered. The agent stated only its interface guess,
// which on that chassis names the other network interface, and the desktop app
// took the speaker for a new one. The id the box confirmed on an earlier boot
// has to be stated as the firmware id from the start.
func TestStartupIdentity(t *testing.T) {
	tests := []struct {
		name              string
		guess, confirmed  string
		wantID, wantBoxID string
	}{
		{
			name:  "first boot, nothing confirmed yet: only the guess",
			guess: "0000000001B2", confirmed: "",
			wantID: "0000000001B2", wantBoxID: "",
		},
		{
			name:  "a guess naming the other interface keeps the announced id and adds the confirmed one",
			guess: "0000000001B2", confirmed: "0000000001A1",
			wantID: "0000000001B2", wantBoxID: "0000000001A1",
		},
		{
			name:  "a guess that already matches changes nothing",
			guess: "0000000001A1", confirmed: "0000000001A1",
			wantID: "0000000001A1", wantBoxID: "0000000001A1",
		},
		{
			name:  "no MAC found: start from the confirmed id instead of nothing",
			guess: "", confirmed: "0000000001A1",
			wantID: "0000000001A1", wantBoxID: "0000000001A1",
		},
		{
			name:  "neither: nothing to state",
			guess: "", confirmed: "",
			wantID: "", wantBoxID: "",
		},
		{
			name:  "the confirmed id is stated upper case, like the firmware does",
			guess: "0000000001B2", confirmed: " 0000000001a1 ",
			wantID: "0000000001B2", wantBoxID: "0000000001A1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, boxID := startupIdentity(tc.guess, tc.confirmed)
			if id != tc.wantID || boxID != tc.wantBoxID {
				t.Fatalf("startupIdentity(%q, %q) = (%q, %q), want (%q, %q)",
					tc.guess, tc.confirmed, id, boxID, tc.wantID, tc.wantBoxID)
			}
		})
	}
}

// What /api/agent/version states: the live announcement wins field by field,
// the startup values fill in before the announcer is up or while the firmware
// has not answered.
func TestAgentIdentity(t *testing.T) {
	tests := []struct {
		name                                 string
		annID, annBoxID, bootID, confirmedID string
		wantID, wantBoxID                    string
	}{
		{
			name:   "announcer not up yet: boot id and confirmed id",
			bootID: "0000000001B2", confirmedID: "0000000001A1",
			wantID: "0000000001B2", wantBoxID: "0000000001A1",
		},
		{
			name:  "firmware down: the announcer has no firmware id, the confirmed one stands in",
			annID: "0000000001B2", bootID: "0000000001B2", confirmedID: "0000000001A1",
			wantID: "0000000001B2", wantBoxID: "0000000001A1",
		},
		{
			name:  "the firmware's own answer wins over the stored one",
			annID: "0000000001B2", annBoxID: "0000000001C3", bootID: "0000000001B2", confirmedID: "0000000001A1",
			wantID: "0000000001B2", wantBoxID: "0000000001C3",
		},
		{
			name:  "nothing confirmed and firmware down: just the announced id, as before",
			annID: "0000000001B2", bootID: "0000000001B2",
			wantID: "0000000001B2", wantBoxID: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, boxID := agentIdentity(tc.annID, tc.annBoxID, tc.bootID, tc.confirmedID)
			if id != tc.wantID || boxID != tc.wantBoxID {
				t.Fatalf("agentIdentity() = (%q, %q), want (%q, %q)", id, boxID, tc.wantID, tc.wantBoxID)
			}
		})
	}
}

func TestIsOwnDeviceID(t *testing.T) {
	if !isOwnDeviceID("0000000001a1", "0000000001B2", "0000000001A1") {
		t.Fatal("the confirmed id must count as this speaker's own, case-insensitively")
	}
	if !isOwnDeviceID("0000000001B2", "0000000001B2", "") {
		t.Fatal("the boot id must count as this speaker's own")
	}
	if isOwnDeviceID("", "", "") {
		t.Fatal("an empty id must never match, not even an empty own id")
	}
	if isOwnDeviceID("0000000001C3", "0000000001B2", "0000000001A1") {
		t.Fatal("another speaker's id must not match")
	}
}

// The first announcement carries the name and model the firmware reported on
// an earlier boot, and the old placeholders only when nothing is stored.
func TestStartupLabel(t *testing.T) {
	name, model := startupLabel("0000000001B2", boxsnapshot.Label{Name: "Kitchen", Model: "SoundTouch Wave"})
	if name != "Kitchen" || model != "SoundTouch Wave" {
		t.Fatalf("stored label: got (%q, %q)", name, model)
	}
	name, model = startupLabel("0000000001B2", boxsnapshot.Label{})
	if name != "Bose SoundTouch 0001B2" || model != "SoundTouch" {
		t.Fatalf("nothing stored: got (%q, %q), want the placeholders", name, model)
	}
	name, model = startupLabel("0000000001B2", boxsnapshot.Label{Model: "SoundTouch 10"})
	if name != "Bose SoundTouch 0001B2" || model != "SoundTouch 10" {
		t.Fatalf("model only: got (%q, %q)", name, model)
	}
}
