package discovery

import (
	"strings"
	"testing"
)

// txtValue returns the value announced under key, or "" when the key is absent.
func txtValue(txt []string, key string) (string, bool) {
	for _, kv := range txt {
		k, v, _ := strings.Cut(kv, "=")
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

// The speaker announces TWO identities and they have different jobs: deviceID is
// the agent's own, which clients store and must keep matching, and boxDeviceID
// is what the firmware calls this speaker, which a client needs to match a zone
// document. Publishing the firmware id must never move the stored one.
func TestTXTCarriesBothIdentities(t *testing.T) {
	a := &Announcer{cfg: Config{DeviceID: "AABBCCDDEEFF", BoxDeviceID: "112233445566"}}
	txt := a.txtRecord()

	if got, ok := txtValue(txt, "deviceID"); !ok || got != "AABBCCDDEEFF" {
		t.Errorf("deviceID = %q (present=%v), want the agent's own id", got, ok)
	}
	if got, ok := txtValue(txt, "boxDeviceID"); !ok || got != "112233445566" {
		t.Errorf("boxDeviceID = %q (present=%v), want the firmware id", got, ok)
	}
}

// An agent that has not read the firmware yet still announces the key, empty:
// the field is part of the record from the first announce, so a client never
// has to tell "this agent is too old to know" from "this box has not answered
// yet" by the key's absence.
func TestTXTBoxDeviceIDPresentBeforeTheFirmwareAnswers(t *testing.T) {
	a := &Announcer{cfg: Config{DeviceID: "AABBCCDDEEFF"}}
	got, ok := txtValue(a.txtRecord(), "boxDeviceID")
	if !ok {
		t.Fatal("boxDeviceID is not announced at all before the firmware answers")
	}
	if got != "" {
		t.Errorf("boxDeviceID = %q, want empty until the firmware answers", got)
	}
}

// The accessors are what the agent compares against before it re-announces, and
// what the peer roster builds its self-identity list from. Both must be nil-safe:
// the announcer is created a few seconds after the roster's seams are wired.
func TestIdentityAccessorsAreNilSafe(t *testing.T) {
	var a *Announcer
	if got := a.DeviceID(); got != "" {
		t.Errorf("nil DeviceID() = %q, want empty", got)
	}
	if got := a.BoxDeviceID(); got != "" {
		t.Errorf("nil BoxDeviceID() = %q, want empty", got)
	}
	if err := a.UpdateBoxDeviceID("112233445566"); err != nil {
		t.Errorf("nil UpdateBoxDeviceID: %v", err)
	}
}

// The one invariant the peer roster's #697 backstop rests on: whatever else is
// corrected from the firmware, the id this speaker announces as deviceID is the
// id it booted with. A stale self-announcement is recognised by nothing else
// when both the address and the name are useless.
func TestUpdateBoxDeviceIDLeavesTheAnnouncedDeviceIDAlone(t *testing.T) {
	a := &Announcer{cfg: Config{DeviceID: "AABBCCDDEEFF"}}
	// No re-announce: the value already matches, so this returns before it
	// would touch the mDNS servers (which a unit test must not start).
	if err := a.UpdateBoxDeviceID(""); err != nil {
		t.Fatalf("UpdateBoxDeviceID(same value): %v", err)
	}
	if got := a.DeviceID(); got != "AABBCCDDEEFF" {
		t.Errorf("deviceID = %q, want the boot id untouched", got)
	}

	a.cfg.BoxDeviceID = "112233445566" // as a successful update leaves it
	if got := a.DeviceID(); got != "AABBCCDDEEFF" {
		t.Errorf("deviceID = %q after the firmware id landed, want the boot id untouched", got)
	}
	if got, _ := txtValue(a.txtRecord(), "deviceID"); got != "AABBCCDDEEFF" {
		t.Errorf("announced deviceID = %q after the firmware id landed, want the boot id", got)
	}
}

// A re-announce is a withdrawal: both mDNS servers go down and register again,
// and for that moment the speaker is not on the network at all. The first
// successful box poll after a boot usually learns the model, the firmware id
// and the display name from ONE answer, and applying them one at a time
// withdrew the service three times in a row. So the poll gathers first and this
// applies everything in a single change set.
func TestUpdateBoxInfoCollectsEveryChangeIntoOneReannounce(t *testing.T) {
	a := &Announcer{cfg: Config{DeviceID: "AABBCCDDEEFF", Model: "SoundTouch", FriendlyName: "str-192.0.2.7"}}

	was, now := a.applyBoxInfoLocked("SoundTouch 10", "112233445566", "Kitchen")

	if len(now) != 3 || len(was) != 3 {
		t.Fatalf("one change set with all three fields expected, got was=%v now=%v", was, now)
	}
	if a.cfg.Model != "SoundTouch 10" || a.cfg.BoxDeviceID != "112233445566" || a.cfg.FriendlyName != "Kitchen" {
		t.Errorf("fields not applied: %+v", a.cfg)
	}
	if got, _ := txtValue(a.txtRecord(), "deviceID"); got != "AABBCCDDEEFF" {
		t.Errorf("announced deviceID = %q, want the boot id untouched", got)
	}
}

// An empty argument means "the firmware did not answer for this field", never
// "clear it": the poll leaves out what it could not read, and a blanked model
// or name would be worse than a stale one.
func TestUpdateBoxInfoLeavesUnreportedFieldsAlone(t *testing.T) {
	a := &Announcer{cfg: Config{DeviceID: "AABBCCDDEEFF", Model: "SoundTouch 10", FriendlyName: "Kitchen"}}

	_, now := a.applyBoxInfoLocked("", "112233445566", "")

	if len(now) != 1 {
		t.Fatalf("only the firmware id changed, got %v", now)
	}
	if a.cfg.Model != "SoundTouch 10" || a.cfg.FriendlyName != "Kitchen" {
		t.Errorf("an unreported field was cleared: %+v", a.cfg)
	}
}

// Nothing new means no re-announce at all: a poll round that learns the same
// values it already announces must not withdraw the service for it.
func TestUpdateBoxInfoIsANoOpWhenNothingChanged(t *testing.T) {
	a := &Announcer{cfg: Config{DeviceID: "AABBCCDDEEFF", Model: "SoundTouch 10", BoxDeviceID: "112233445566", FriendlyName: "Kitchen"}}

	// The real entry point, not the helper: with no change it must return
	// before it would touch the mDNS servers (which a unit test must not start).
	if err := a.UpdateBoxInfo("SoundTouch 10", "112233445566", "Kitchen"); err != nil {
		t.Fatalf("UpdateBoxInfo(unchanged): %v", err)
	}
}
