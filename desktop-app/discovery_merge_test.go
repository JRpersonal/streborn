package main

import (
	"reflect"
	"strings"
	"testing"
)

// Per-field pins for the two record merges. Every BoxInfo field gets at least
// one row per merge so a change in either function's behaviour for that field
// fails here by name, not somewhere downstream as list flicker or a wrong
// badge. The rows were written against the hand-written merges and must stay
// green across any reimplementation.

// mergePin is one pinned outcome: merging prev (or a) with cur (or b) must
// yield want in field.
type mergePin struct {
	field string
	prev  BoxInfo // mergeBoxInfo: the cached record. mergeSameKind: a.
	cur   BoxInfo // mergeBoxInfo: this cycle's record. mergeSameKind: b.
	want  any
	why   string
}

// fieldValue reads one BoxInfo field by name.
func fieldValue(t *testing.T, b BoxInfo, field string) any {
	t.Helper()
	v := reflect.ValueOf(b).FieldByName(field)
	if !v.IsValid() {
		t.Fatalf("BoxInfo has no field %q", field)
	}
	return v.Interface()
}

func runMergePins(t *testing.T, merge func(BoxInfo, BoxInfo) BoxInfo, pins []mergePin) {
	t.Helper()
	for _, p := range pins {
		out := merge(p.prev, p.cur)
		if got := fieldValue(t, out, p.field); !reflect.DeepEqual(got, p.want) {
			t.Errorf("%s: got %v, want %v (%s)", p.field, got, p.want, p.why)
		}
	}
}

// TestBoxFieldPolicyCoversEveryField fails the moment BoxInfo has a field the
// policy table does not name, names twice, or names that does not exist, so a
// new field cannot be silently left out of either merge (which is how the
// agent hashes went missing from the same-cycle merge). A follower must come
// after the field it follows, or the merge would panic at runtime.
func TestBoxFieldPolicyCoversEveryField(t *testing.T) {
	typ := reflect.TypeOf(BoxInfo{})
	inStruct := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		inStruct[typ.Field(i).Name] = true
	}
	inTable := map[string]bool{}
	decided := map[string]bool{}
	for _, p := range boxFieldPolicy {
		if !inStruct[p.field] {
			t.Errorf("boxFieldPolicy names %q, which is not a BoxInfo field", p.field)
		}
		if inTable[p.field] {
			t.Errorf("boxFieldPolicy names %q twice", p.field)
		}
		inTable[p.field] = true
		if p.cache.pick == nil || p.sameKind.pick == nil {
			t.Errorf("boxFieldPolicy: %s has an empty rule", p.field)
		}
		for _, r := range []fieldRule{p.cache, p.sameKind} {
			if key, ok := strings.CutPrefix(r.name, "follows("); ok {
				key = strings.TrimSuffix(key, ")")
				if !decided[key] {
					t.Errorf("boxFieldPolicy: %s follows %s, which must come before it", p.field, key)
				}
			}
		}
		decided[p.field] = true
	}
	for name := range inStruct {
		if !inTable[name] {
			t.Errorf("BoxInfo.%s has no rule in boxFieldPolicy: say how both merges treat it", name)
		}
	}
	if len(boxFieldPolicy) != typ.NumField() {
		t.Errorf("boxFieldPolicy has %d rows, BoxInfo has %d fields", len(boxFieldPolicy), typ.NumField())
	}
	// The table, for the reader of a verbose run.
	for _, p := range boxFieldPolicy {
		t.Logf("%-22s cache=%-18s sameKind=%s", p.field, p.cache.name, p.sameKind.name)
	}
}

// A field rule must never read a field it was not asked about by accident:
// merging a record with itself is the identity for every rule.
func TestMergeWithSelfIsIdentity(t *testing.T) {
	full := BoxInfo{Name: "n", Host: "h", Port: 17008, DeviceID: "d", BoxDeviceID: "bd", FriendlyName: "Küche",
		Model: "SoundTouch 10", Version: "v1", Build: "b", AgentBinarySha256: "s1", AgentRunningSha256: "s2",
		Offline: true, OfflineSinceSec: 1, STRNotRunning: false, STRSilent: true, OTAPending: true, BoxHealth: "ok",
		Internet: "ok", InternetReason: "r", ArcadeAt: "1", ConflictingMod: "c", ForeignCloudURL: "f",
		GroupKeyError: "g", HoldRefusedSource: "SPOTIFY", HoldRefusedSlot: "3", Storm1036: true, Storm1036SinceSec: 2,
		RecallRefusal: true, RecallRefusalSinceSec: 3, WLANCredsMissing: true, SerialNumber: "sn", Kind: "str", PortVerified: true}
	if got := mergeBoxInfo(full, full); !reflect.DeepEqual(got, full) {
		t.Errorf("mergeBoxInfo(x, x) != x:\n got %+v\nwant %+v", got, full)
	}
	if got := mergeSameKind(full, full); !reflect.DeepEqual(got, full) {
		t.Errorf("mergeSameKind(x, x) != x:\n got %+v\nwant %+v", got, full)
	}
}

// TestMergeBoxInfoFieldPins pins mergeBoxInfo(prev, cur): this cycle's record
// is the base, the cached one only fills what the cycle came back without.
func TestMergeBoxInfoFieldPins(t *testing.T) {
	str := func(b BoxInfo) BoxInfo { b.Kind = "str"; return b }
	runMergePins(t, mergeBoxInfo, []mergePin{
		{"Name", str(BoxInfo{Name: "STR-AAAAAA"}), str(BoxInfo{Name: "str-192.0.2.1"}), "str-192.0.2.1", "this cycle's mDNS name wins"},
		{"Name", str(BoxInfo{Name: "STR-AAAAAA"}), str(BoxInfo{}), "", "the cache never fills the technical name"},
		{"Host", str(BoxInfo{Host: "192.0.2.1"}), str(BoxInfo{Host: "192.0.2.2"}), "192.0.2.2", "the live host wins"},

		{"Port", str(BoxInfo{Port: 17008, PortVerified: true}), str(BoxInfo{Port: 8888}), 17008, "a verified cached port beats an announced one"},
		{"PortVerified", str(BoxInfo{Port: 17008, PortVerified: true}), str(BoxInfo{Port: 8888}), true, "the restored port is verified"},
		{"Port", str(BoxInfo{Port: 17008, PortVerified: true}), str(BoxInfo{Port: 8888, PortVerified: true}), 8888, "a verified live port wins over a verified cached one"},
		{"Port", str(BoxInfo{Port: 8090}), str(BoxInfo{Port: 8888}), 8888, "with nothing verified the live port stands"},
		{"PortVerified", str(BoxInfo{Port: 8090}), str(BoxInfo{Port: 8888}), false, "nothing verified stays unverified"},
		{"Port", str(BoxInfo{Port: 0, PortVerified: true}), str(BoxInfo{Port: 8888}), 8888, "a verified cached record without a port cannot restore one"},
		{"PortVerified", str(BoxInfo{Port: 0, PortVerified: true}), str(BoxInfo{Port: 8888}), false, "and does not mark the live port verified"},

		{"DeviceID", str(BoxInfo{DeviceID: "OLD"}), str(BoxInfo{DeviceID: "NEW"}), "NEW", "the live deviceID wins"},
		{"DeviceID", str(BoxInfo{DeviceID: "OLD"}), str(BoxInfo{}), "OLD", "an empty live deviceID is filled from the cache"},
		{"BoxDeviceID", str(BoxInfo{BoxDeviceID: "OLD"}), str(BoxInfo{BoxDeviceID: "NEW"}), "NEW", "the live box id wins"},
		{"BoxDeviceID", str(BoxInfo{BoxDeviceID: "OLD"}), str(BoxInfo{}), "OLD", "a speaker cannot stop being what the firmware calls it"},

		{"FriendlyName", str(BoxInfo{FriendlyName: "Küche"}), str(BoxInfo{FriendlyName: "Bose SoundTouch 1234"}), "Küche", "a real cached name beats the factory default"},
		{"FriendlyName", str(BoxInfo{FriendlyName: "Küche"}), str(BoxInfo{}), "Küche", "a thin cycle must not blank the name (#108)"},
		{"FriendlyName", str(BoxInfo{FriendlyName: "Küche unten"}), str(BoxInfo{FriendlyName: "Küche"}), "Küche", "between two real names the live one wins, there is no length contest here"},
		{"FriendlyName", str(BoxInfo{FriendlyName: "Bose SoundTouch 1234"}), str(BoxInfo{FriendlyName: "Bose SoundTouch 9999"}), "Bose SoundTouch 9999", "two factory names: the live one"},

		{"Model", str(BoxInfo{Model: "SoundTouch 10"}), str(BoxInfo{Model: "SoundTouch"}), "SoundTouch 10", "the generic model gives way to the specific cached one"},
		{"Model", str(BoxInfo{Model: "SoundTouch 10"}), str(BoxInfo{}), "SoundTouch 10", "an empty model is filled"},
		{"Model", str(BoxInfo{Model: "SoundTouch 10"}), str(BoxInfo{Model: "SoundTouch 20"}), "SoundTouch 20", "two specific models: the live one"},

		{"Version", str(BoxInfo{Version: "v1.0.17"}), str(BoxInfo{Version: "v1.0.18"}), "v1.0.18", "the live version wins"},
		{"Version", str(BoxInfo{Version: "v1.0.17"}), str(BoxInfo{}), "v1.0.17", "an empty live version is filled from the cache"},
		{"Build", str(BoxInfo{Build: "2026-10-01-1200"}), str(BoxInfo{Build: "2026-10-20-1200"}), "2026-10-20-1200", "the live build wins"},
		{"Build", str(BoxInfo{Build: "2026-10-01-1200"}), str(BoxInfo{}), "2026-10-01-1200", "an empty live build is filled"},
		{"AgentBinarySha256", str(BoxInfo{AgentBinarySha256: "old"}), str(BoxInfo{AgentBinarySha256: "new"}), "new", "the live hash wins"},
		{"AgentBinarySha256", str(BoxInfo{AgentBinarySha256: "old"}), str(BoxInfo{}), "old", "an empty live hash is filled (older agent, or mDNS only)"},
		{"AgentRunningSha256", str(BoxInfo{AgentRunningSha256: "old"}), str(BoxInfo{AgentRunningSha256: "new"}), "new", "the live running hash wins"},
		{"AgentRunningSha256", str(BoxInfo{AgentRunningSha256: "old"}), str(BoxInfo{}), "old", "an empty live running hash is filled"},

		{"Offline", str(BoxInfo{Offline: true, OfflineSinceSec: 90}), str(BoxInfo{}), false, "a cached offline marker never survives a sighting"},
		{"OfflineSinceSec", str(BoxInfo{Offline: true, OfflineSinceSec: 90}), str(BoxInfo{}), 0, "nor does its age"},
		{"STRSilent", str(BoxInfo{STRSilent: true}), str(BoxInfo{}), false, "STRSilent is per cycle, never carried"},
		{"OTAPending", str(BoxInfo{OTAPending: true}), str(BoxInfo{}), false, "OTAPending is per cycle, never carried"},

		{"STRNotRunning", BoxInfo{Kind: "stock", STRNotRunning: true}, BoxInfo{Kind: "stock"}, true, "a plain stock sighting keeps the 'STR not running' memory"},
		{"STRNotRunning", BoxInfo{Kind: "stock", STRNotRunning: true}, BoxInfo{Kind: "str", Version: "v1.0.17"}, true, "an unverified STR label (a stale announcement) does not lift it"},
		{"Kind", BoxInfo{Kind: "stock", STRNotRunning: true}, BoxInfo{Kind: "str", Version: "v1.0.17"}, "stock", "and the record is degraded to stock"},
		{"Version", BoxInfo{Kind: "stock", STRNotRunning: true}, BoxInfo{Kind: "str", Version: "v1.0.17"}, "", "without the stale version badge"},
		{"STRNotRunning", BoxInfo{Kind: "stock", STRNotRunning: true}, BoxInfo{Kind: "str", Port: 8888, PortVerified: true}, false, "only a live agent probe lifts it"},
		{"Kind", BoxInfo{Kind: "stock", STRNotRunning: true}, BoxInfo{Kind: "str", Port: 8888, PortVerified: true}, "str", "and the box is STR again"},
		{"STRNotRunning", BoxInfo{Kind: "stock"}, BoxInfo{Kind: "stock", STRNotRunning: true}, true, "a live degrade stands"},

		{"BoxHealth", str(BoxInfo{BoxHealth: "wedged"}), str(BoxInfo{BoxHealth: "ok"}), "ok", "a fresh verdict wins"},
		{"BoxHealth", str(BoxInfo{BoxHealth: "wedged"}), str(BoxInfo{}), "wedged", "an empty verdict keeps the last one so the hint does not flicker"},
		{"Internet", str(BoxInfo{Internet: "unreachable", InternetReason: "dns"}), str(BoxInfo{Internet: "ok"}), "ok", "a fresh verdict wins"},
		{"InternetReason", str(BoxInfo{Internet: "unreachable", InternetReason: "dns"}), str(BoxInfo{Internet: "ok"}), "", "and takes its own (empty) reason with it"},
		{"Internet", str(BoxInfo{Internet: "unreachable", InternetReason: "dns"}), str(BoxInfo{}), "unreachable", "an mDNS-only sighting keeps the last verdict"},
		{"InternetReason", str(BoxInfo{Internet: "unreachable", InternetReason: "dns"}), str(BoxInfo{}), "dns", "with its reason"},
		{"ArcadeAt", str(BoxInfo{ArcadeAt: "100"}), str(BoxInfo{ArcadeAt: "200"}), "200", "fresh wins"},
		{"ArcadeAt", str(BoxInfo{ArcadeAt: "100"}), str(BoxInfo{}), "100", "empty is filled"},

		{"ConflictingMod", str(BoxInfo{ConflictingMod: "AfterTouch"}), str(BoxInfo{}), "", "warnings are not carried from the cache (a live probe sets them)"},
		{"ConflictingMod", str(BoxInfo{}), str(BoxInfo{ConflictingMod: "AfterTouch"}), "AfterTouch", "a live warning stands"},
		{"ForeignCloudURL", str(BoxInfo{ForeignCloudURL: "https://example.invalid"}), str(BoxInfo{}), "", "not carried"},
		{"GroupKeyError", str(BoxInfo{GroupKeyError: "no partner"}), str(BoxInfo{}), "", "not carried"},
		{"GroupKeyError", str(BoxInfo{}), str(BoxInfo{GroupKeyError: "no partner"}), "no partner", "a live reason stands"},
		{"HoldRefusedSource", str(BoxInfo{HoldRefusedSource: "SPOTIFY"}), str(BoxInfo{}), "", "not carried"},
		{"HoldRefusedSlot", str(BoxInfo{HoldRefusedSlot: "3"}), str(BoxInfo{}), "", "not carried"},
		{"Storm1036", str(BoxInfo{Storm1036: true, Storm1036SinceSec: 30}), str(BoxInfo{}), false, "not carried"},
		{"Storm1036SinceSec", str(BoxInfo{Storm1036: true, Storm1036SinceSec: 30}), str(BoxInfo{}), 0, "not carried"},
		{"RecallRefusal", str(BoxInfo{RecallRefusal: true, RecallRefusalSinceSec: 30}), str(BoxInfo{}), false, "not carried"},
		{"RecallRefusalSinceSec", str(BoxInfo{RecallRefusal: true, RecallRefusalSinceSec: 30}), str(BoxInfo{}), 0, "not carried"},
		{"WLANCredsMissing", str(BoxInfo{WLANCredsMissing: true}), str(BoxInfo{}), false, "not carried (#270: a stale mid-boot snapshot must not stick)"},
		{"WLANCredsMissing", str(BoxInfo{}), str(BoxInfo{WLANCredsMissing: true}), true, "a live warning stands"},

		{"SerialNumber", str(BoxInfo{SerialNumber: "069236P60560580AE"}), str(BoxInfo{}), "069236P60560580AE", "an empty serial is filled"},
		{"SerialNumber", str(BoxInfo{SerialNumber: "OLD"}), str(BoxInfo{SerialNumber: "NEW"}), "NEW", "a live serial wins"},

		{"Kind", BoxInfo{Kind: "str"}, BoxInfo{Kind: "stock"}, "str", "an STR agent, once seen, outranks a stock-only sighting"},
		{"Kind", BoxInfo{Kind: "stock"}, BoxInfo{Kind: "str"}, "str", "a live STR sighting stands"},
		{"Kind", BoxInfo{Kind: "stock"}, BoxInfo{Kind: "stock"}, "stock", "two stock sightings stay stock"},
	})
}

// TestMergeSameKindFieldPins pins mergeSameKind(a, b): two sightings of the
// same box in one cycle, the first the base, the live-probed side (PortVerified)
// authoritative for what only a live probe can know.
func TestMergeSameKindFieldPins(t *testing.T) {
	// v marks a record as the live probe; m is the mDNS announcement.
	v := func(b BoxInfo) BoxInfo { b.Kind = "str"; b.PortVerified = true; return b }
	m := func(b BoxInfo) BoxInfo { b.Kind = "str"; return b }
	runMergePins(t, mergeSameKind, []mergePin{
		{"Name", m(BoxInfo{Name: "STR-AAAAAA"}), v(BoxInfo{Name: "str-192.0.2.1"}), "STR-AAAAAA", "the first name stands"},
		{"Name", m(BoxInfo{}), v(BoxInfo{Name: "str-192.0.2.1"}), "str-192.0.2.1", "an empty first name is filled"},
		{"Host", m(BoxInfo{Host: "192.0.2.1"}), v(BoxInfo{Host: "192.0.2.1"}), "192.0.2.1", "same host by construction"},

		{"Port", m(BoxInfo{Port: 8888}), v(BoxInfo{Port: 17008}), 17008, "a verified port beats an announced one (BCO boxes announce :8888 but answer :17008)"},
		{"PortVerified", m(BoxInfo{Port: 8888}), v(BoxInfo{Port: 17008}), true, "and the merged record is verified"},
		{"Port", v(BoxInfo{Port: 17008}), m(BoxInfo{Port: 8888}), 17008, "the same with the probe first"},
		{"PortVerified", v(BoxInfo{Port: 17008}), m(BoxInfo{Port: 8888}), true, "still verified"},
		{"Port", v(BoxInfo{Port: 8888}), v(BoxInfo{Port: 17008}), 8888, "two verified ports: the first (#1190, no flip between refreshes)"},
		{"Port", m(BoxInfo{Port: 8888}), m(BoxInfo{Port: 17008}), 8888, "two announced ports: the first"},
		{"PortVerified", m(BoxInfo{Port: 8888}), m(BoxInfo{Port: 17008}), false, "two announced ports: still unverified"},
		{"Port", m(BoxInfo{Port: 8888}), v(BoxInfo{Port: 0}), 8888, "a verified record without a port cannot win"},
		{"PortVerified", m(BoxInfo{Port: 8888}), v(BoxInfo{Port: 0}), false, "and does not verify the announced one"},

		{"DeviceID", m(BoxInfo{DeviceID: "0000000001D4"}), v(BoxInfo{DeviceID: "0000000001C3"}), "0000000001C3", "the /info deviceID wins over the mDNS wlan0 MAC (#70)"},
		{"DeviceID", v(BoxInfo{DeviceID: "0000000001C3"}), m(BoxInfo{DeviceID: "0000000001D4"}), "0000000001C3", "regardless of order"},
		{"DeviceID", m(BoxInfo{DeviceID: "0000000001D4"}), v(BoxInfo{}), "0000000001D4", "a blank verified deviceID falls back to the announced one"},
		{"DeviceID", m(BoxInfo{DeviceID: "A"}), m(BoxInfo{DeviceID: "B"}), "A", "two announcements: the first"},
		{"DeviceID", m(BoxInfo{}), m(BoxInfo{DeviceID: "B"}), "B", "an empty first is filled"},
		{"BoxDeviceID", m(BoxInfo{BoxDeviceID: "A"}), v(BoxInfo{BoxDeviceID: "B"}), "A", "no contest: both sides carry the same value when set"},
		{"BoxDeviceID", m(BoxInfo{}), v(BoxInfo{BoxDeviceID: "B"}), "B", "whichever side has one"},

		{"FriendlyName", m(BoxInfo{FriendlyName: "Wohnzimmer"}), v(BoxInfo{}), "Wohnzimmer", "the real name from mDNS survives a nameless probe (#108)"},
		{"FriendlyName", m(BoxInfo{FriendlyName: "Bose SoundTouch 1234"}), v(BoxInfo{FriendlyName: "Küche"}), "Küche", "a real name beats the factory default"},
		{"FriendlyName", m(BoxInfo{FriendlyName: "Küche"}), v(BoxInfo{FriendlyName: "Küche unten"}), "Küche unten", "between two real names the longer wins"},
		{"FriendlyName", m(BoxInfo{FriendlyName: "Küche unten"}), v(BoxInfo{FriendlyName: "Küche"}), "Küche unten", "in either order"},
		{"FriendlyName", m(BoxInfo{FriendlyName: "Bad"}), v(BoxInfo{FriendlyName: "Flu"}), "Bad", "equal length: the first"},
		{"Model", m(BoxInfo{Model: "SoundTouch"}), v(BoxInfo{Model: "SoundTouch 10"}), "SoundTouch 10", "the specific model beats the generic one"},
		{"Model", m(BoxInfo{Model: "SoundTouch 10"}), v(BoxInfo{Model: "SoundTouch 20"}), "SoundTouch 10", "two specific models: the first"},

		{"Version", m(BoxInfo{Version: "v1.0.17"}), v(BoxInfo{Version: "v1.0.18"}), "v1.0.18", "the live probe's version beats a lagging announcement (#108)"},
		{"Version", v(BoxInfo{Version: "v1.0.18"}), m(BoxInfo{Version: "v1.0.17"}), "v1.0.18", "in either order"},
		{"Version", m(BoxInfo{Version: "v1.0.17"}), v(BoxInfo{}), "v1.0.17", "a probe without a version leaves the announced one"},
		{"Version", m(BoxInfo{Version: "v1.0.17"}), m(BoxInfo{Version: "v1.0.18"}), "v1.0.17", "two announcements: the first"},
		{"Version", m(BoxInfo{}), m(BoxInfo{Version: "v1.0.18"}), "v1.0.18", "an empty first is filled"},
		{"Build", m(BoxInfo{Build: "old"}), v(BoxInfo{Build: "new"}), "new", "same for the build stamp"},
		{"Build", m(BoxInfo{Build: "old"}), v(BoxInfo{}), "old", "a probe without a build leaves the announced one"},
		{"AgentBinarySha256", m(BoxInfo{}), v(BoxInfo{AgentBinarySha256: "new"}), "new", "the probe's hash reaches the merged record (announcements carry none)"},
		{"AgentBinarySha256", v(BoxInfo{AgentBinarySha256: "new"}), m(BoxInfo{}), "new", "in either order"},
		{"AgentBinarySha256", m(BoxInfo{AgentBinarySha256: "stale"}), v(BoxInfo{}), "stale", "a probe from an agent too old to report one leaves what there is"},
		{"AgentRunningSha256", m(BoxInfo{}), v(BoxInfo{AgentRunningSha256: "new"}), "new", "the running hash is what the update badge keys on"},
		{"AgentRunningSha256", v(BoxInfo{AgentRunningSha256: "new"}), m(BoxInfo{}), "new", "in either order"},
		{"AgentRunningSha256", m(BoxInfo{}), m(BoxInfo{AgentRunningSha256: "x"}), "x", "no probe: whichever has one"},

		{"Offline", m(BoxInfo{Offline: true}), v(BoxInfo{}), true, "per-cycle annotation: the first"},
		{"OfflineSinceSec", m(BoxInfo{OfflineSinceSec: 9}), v(BoxInfo{}), 9, "the first"},
		{"STRNotRunning", m(BoxInfo{STRNotRunning: true}), v(BoxInfo{}), true, "the first"},
		{"STRSilent", m(BoxInfo{STRSilent: true}), v(BoxInfo{}), true, "the first"},
		{"OTAPending", m(BoxInfo{OTAPending: true}), v(BoxInfo{}), true, "the first"},
		{"Kind", m(BoxInfo{}), v(BoxInfo{}), "str", "same kind by construction"},

		{"BoxHealth", m(BoxInfo{BoxHealth: "wedged"}), v(BoxInfo{}), "", "the live probe's reading wins, including its no-warning reading (#270)"},
		{"BoxHealth", v(BoxInfo{}), m(BoxInfo{BoxHealth: "wedged"}), "", "in either order"},
		{"BoxHealth", m(BoxInfo{}), m(BoxInfo{BoxHealth: "wedged"}), "wedged", "with no probe, whichever side has a value"},
		{"Internet", m(BoxInfo{Internet: "unreachable", InternetReason: "dns"}), v(BoxInfo{Internet: "ok"}), "ok", "the live verdict"},
		{"InternetReason", m(BoxInfo{Internet: "unreachable", InternetReason: "dns"}), v(BoxInfo{Internet: "ok"}), "", "with its own reason"},
		{"Internet", m(BoxInfo{}), m(BoxInfo{Internet: "unreachable", InternetReason: "dns"}), "unreachable", "no probe: the side with a verdict"},
		{"InternetReason", m(BoxInfo{}), m(BoxInfo{Internet: "unreachable", InternetReason: "dns"}), "dns", "and its reason"},
		{"ArcadeAt", m(BoxInfo{ArcadeAt: "100"}), v(BoxInfo{}), "", "the live reading, even empty"},
		{"ArcadeAt", m(BoxInfo{}), m(BoxInfo{ArcadeAt: "100"}), "100", "no probe: whichever has one"},
		{"ConflictingMod", m(BoxInfo{ConflictingMod: "AfterTouch"}), v(BoxInfo{}), "", "the live no-warning reading clears a stale one (#270)"},
		{"ConflictingMod", m(BoxInfo{}), v(BoxInfo{ConflictingMod: "AfterTouch"}), "AfterTouch", "a live warning stands"},
		{"ConflictingMod", m(BoxInfo{}), m(BoxInfo{ConflictingMod: "AfterTouch"}), "AfterTouch", "no probe: whichever has one"},
		{"ForeignCloudURL", m(BoxInfo{ForeignCloudURL: "x"}), v(BoxInfo{}), "", "the live reading"},
		{"ForeignCloudURL", m(BoxInfo{}), m(BoxInfo{ForeignCloudURL: "x"}), "x", "no probe: whichever has one"},
		{"GroupKeyError", m(BoxInfo{}), v(BoxInfo{GroupKeyError: "no partner"}), "no partner", "the probe's refusal reason reaches the list"},
		{"GroupKeyError", m(BoxInfo{GroupKeyError: "stale"}), v(BoxInfo{}), "", "the live no-refusal reading clears a stale one"},
		{"GroupKeyError", m(BoxInfo{}), m(BoxInfo{GroupKeyError: "x"}), "x", "no probe: whichever has one"},
		{"HoldRefusedSource", m(BoxInfo{}), v(BoxInfo{HoldRefusedSource: "SPOTIFY", HoldRefusedSlot: "3"}), "SPOTIFY", "the probe's reading"},
		{"HoldRefusedSlot", m(BoxInfo{}), v(BoxInfo{HoldRefusedSource: "SPOTIFY", HoldRefusedSlot: "3"}), "3", "with its slot"},
		{"HoldRefusedSource", m(BoxInfo{HoldRefusedSource: "SPOTIFY", HoldRefusedSlot: "3"}), v(BoxInfo{}), "", "the live reading clears it"},
		{"HoldRefusedSlot", m(BoxInfo{HoldRefusedSource: "SPOTIFY", HoldRefusedSlot: "3"}), v(BoxInfo{}), "", "slot too"},
		{"HoldRefusedSlot", m(BoxInfo{}), m(BoxInfo{HoldRefusedSource: "SPOTIFY", HoldRefusedSlot: "3"}), "3", "no probe: the slot follows the source"},
		{"Storm1036", m(BoxInfo{Storm1036: true, Storm1036SinceSec: 30}), v(BoxInfo{}), false, "the live reading"},
		{"Storm1036SinceSec", m(BoxInfo{Storm1036: true, Storm1036SinceSec: 30}), v(BoxInfo{}), 0, "with its age"},
		{"Storm1036", m(BoxInfo{}), m(BoxInfo{Storm1036: true, Storm1036SinceSec: 30}), true, "no probe: a storm on either side"},
		{"Storm1036SinceSec", m(BoxInfo{}), m(BoxInfo{Storm1036: true, Storm1036SinceSec: 30}), 30, "with that side's age"},
		{"Storm1036SinceSec", m(BoxInfo{Storm1036: true, Storm1036SinceSec: 3}), m(BoxInfo{Storm1036: true, Storm1036SinceSec: 30}), 3, "both sides: the first's age"},
		{"RecallRefusal", m(BoxInfo{RecallRefusal: true, RecallRefusalSinceSec: 30}), v(BoxInfo{}), false, "the live reading"},
		{"RecallRefusalSinceSec", m(BoxInfo{RecallRefusal: true, RecallRefusalSinceSec: 30}), v(BoxInfo{}), 0, "with its age"},
		{"RecallRefusal", m(BoxInfo{}), m(BoxInfo{RecallRefusal: true, RecallRefusalSinceSec: 30}), true, "no probe: a refusal on either side"},
		{"RecallRefusalSinceSec", m(BoxInfo{}), m(BoxInfo{RecallRefusal: true, RecallRefusalSinceSec: 30}), 30, "with that side's age"},
		{"WLANCredsMissing", m(BoxInfo{WLANCredsMissing: true}), v(BoxInfo{}), false, "the live reading clears a stale warning"},
		{"WLANCredsMissing", m(BoxInfo{}), m(BoxInfo{WLANCredsMissing: true}), true, "no probe: a warning on either side"},

		{"SerialNumber", m(BoxInfo{SerialNumber: "A"}), v(BoxInfo{SerialNumber: "B"}), "A", "the first"},
		{"SerialNumber", m(BoxInfo{}), v(BoxInfo{SerialNumber: "B"}), "B", "an empty first is filled"},
	})
}
