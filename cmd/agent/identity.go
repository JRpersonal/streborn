package main

import (
	"strings"

	"github.com/JRpersonal/streborn/discovery"
	"github.com/JRpersonal/streborn/internal/boxsnapshot"
)

// confirmedDeviceIDPath is where marge stores the deviceID the box confirmed
// for itself (GET /info or its addDevice POST). Read once at startup here too,
// so every place the agent states its identity can use it before the firmware
// answers (see startupIdentity).
const confirmedDeviceIDPath = "/mnt/nv/streborn/deviceid"

// startupIdentity picks the two ids the agent states for itself before the
// Bose firmware has answered /info, from the interface MAC guess and the id the
// box confirmed on an earlier boot (empty when none is stored).
//
// deviceID stays the guess: it is the announced identity clients have always
// stored for this speaker (group-key templates, the peer roster's #697 self
// check), and replacing it would be a silent migration. Only a speaker where
// the guess failed outright starts from the confirmed id instead of nothing.
//
// boxDeviceID is the confirmed id. It is exactly the value the firmware's /info
// put there on every earlier boot, and it is what the desktop app keyed the
// speaker on (the app prefers the firmware id). Without it, a speaker whose
// firmware is slow to start, or does not start at all after an update, states
// only the guess; on a chassis where the guess names the other network
// interface the app then takes it for a different speaker and shows a new,
// nameless entry while the user's own one vanishes (field case 2026-11-05, a
// Wave whose firmware stayed down after an agent update). The box's own answer
// still replaces it the moment /info responds (pollBoxInfo).
func startupIdentity(guess, confirmed string) (deviceID, boxDeviceID string) {
	guess = strings.TrimSpace(guess)
	confirmed = strings.ToUpper(strings.TrimSpace(confirmed))
	deviceID = guess
	if deviceID == "" {
		deviceID = confirmed
	}
	return deviceID, confirmed
}

// agentIdentity is what /api/agent/version states: the announced ids while the
// announcer runs, the startup ones before it is up or while a field is still
// empty. The firmware id falls back to the confirmed one, so the very first
// answer after a boot already names the speaker the app knows.
func agentIdentity(announcedID, announcedBoxID, bootID, confirmedID string) (deviceID, boxDeviceID string) {
	deviceID = announcedID
	if deviceID == "" {
		deviceID = bootID
	}
	boxDeviceID = announcedBoxID
	if boxDeviceID == "" {
		boxDeviceID = confirmedID
	}
	return deviceID, boxDeviceID
}

// isOwnDeviceID reports whether id names this speaker under any of its own ids
// (the boot id and the confirmed one). Case-insensitive; empties never match.
func isOwnDeviceID(id string, own ...string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, o := range own {
		if o = strings.TrimSpace(o); o != "" && strings.EqualFold(id, o) {
			return true
		}
	}
	return false
}

// startupLabel is the name and model the first announcement carries: the ones
// the firmware reported on an earlier boot when they are stored, else the
// placeholders the agent always used (a name built from the id, the generic
// model). pollBoxInfo replaces both with the live answer.
func startupLabel(deviceID string, stored boxsnapshot.Label) (friendlyName, model string) {
	friendlyName, model = stored.Name, stored.Model
	if friendlyName == "" {
		friendlyName = "Bose SoundTouch " + discovery.LastN(deviceID, 6)
	}
	if model == "" {
		model = "SoundTouch"
	}
	return friendlyName, model
}
