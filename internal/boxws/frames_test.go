package boxws

import (
	"context"
	"encoding/xml"
	"testing"
)

// A bass change on the speaker or its remote must be recognised: it is a person
// at the speaker, and until 2026-07-29 each one was logged as an unrecognized
// frame, seven in 400 ms, which buried the signal it actually carried.
func TestBassUpdatedIsRecognised(t *testing.T) {
	var f gabboFrame
	raw := []byte(`<updates deviceID='DEV#03b2d6b9'><bassUpdated></bassUpdated></updates>`)
	if err := xml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.BassUpdated == nil {
		t.Fatal("a bassUpdated frame must parse into BassUpdated, or it falls through as unknown")
	}
	if f.VolumeUpdated != nil {
		t.Error("bass must not be mistaken for a volume change")
	}
}

// A stereo pair is reported by groupUpdated, never by zoneUpdated: while two
// speakers are paired, /getZone still says {"members":[]} on both. These frames
// were arriving all along and were discarded as "unrecognized", which is why
// STR could not notice a pair being torn down anywhere but through its own
// dissolve. A speaker left believing it is still half of a pair is not offered
// for pairing again (field, 2026-08-04, three SoundTouch 10s).
//
// The frames below are verbatim from that report's agent log, with the
// identifiers replaced.
func TestGroupUpdatedFrames(t *testing.T) {
	const paired = `<updates deviceID='AABBCCDDEEFF'><groupUpdated><group id="str-grp-AABBCCDDEEFF">` +
		`<name>Stereo pair</name><masterDeviceId>AABBCCDDEEFF</masterDeviceId><roles>` +
		`<groupRole><deviceId>AABBCCDDEEFF</deviceId><role>LEFT</role><ipAddress>192.0.2.19</ipAddress></groupRole>` +
		`<groupRole><deviceId>112233445566</deviceId><role>RIGHT</role><ipAddress>192.0.2.32</ipAddress></groupRole>` +
		`</roles><senderIPAddress>192.0.2.19</senderIPAddress><status>GROUP_OK</status></group></groupUpdated></updates>`
	// The teardown frame the box sends: an empty, self-closing <group />.
	const dissolved = `<updates deviceID='AABBCCDDEEFF'><groupUpdated><group /></groupUpdated></updates>`

	t.Run("a formed pair is reported with both roles", func(t *testing.T) {
		h := &recHandler{}
		newTestClient(h).handleMessage(context.Background(), []byte(paired))
		if len(h.groups) != 1 {
			t.Fatalf("groups = %d, want 1", len(h.groups))
		}
		g := h.groups[0]
		if !g.Paired() {
			t.Error("Paired() = false for a frame carrying two group roles")
		}
		if g.ID != "str-grp-AABBCCDDEEFF" || g.Master != "AABBCCDDEEFF" {
			t.Errorf("id/master = %q/%q", g.ID, g.Master)
		}
		if len(g.Members) != 2 || g.Members[0].Role != "LEFT" || g.Members[1].Role != "RIGHT" {
			t.Errorf("members = %+v", g.Members)
		}
		if g.Members[1].IP != "192.0.2.32" {
			t.Errorf("member IP = %q, want 192.0.2.32", g.Members[1].IP)
		}
	})

	t.Run("an empty group is a teardown, not a missing frame", func(t *testing.T) {
		h := &recHandler{}
		newTestClient(h).handleMessage(context.Background(), []byte(dissolved))
		if len(h.groups) != 1 {
			t.Fatalf("groups = %d, want 1 (the teardown must be dispatched, not swallowed)", len(h.groups))
		}
		if h.groups[0].Paired() {
			t.Error("Paired() = true for an empty <group />")
		}
	})

	t.Run("a stereo pair is not mistaken for a zone", func(t *testing.T) {
		h := &recHandler{}
		c := newTestClient(h)
		c.handleMessage(context.Background(), []byte(paired))
		c.handleMessage(context.Background(), []byte(dissolved))
		if len(h.zones) != 0 {
			t.Errorf("zones = %+v, want none: a pair is a group, not a zone", h.zones)
		}
	})
}
