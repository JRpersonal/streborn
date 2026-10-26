package main

import (
	"fmt"
	"reflect"
	"strings"
)

// Pure record-merge rules for two sightings of the same speaker. Split out of app_discovery.go.
//
// Both merges (the cache merge in mergeBoxInfo and the same-cycle merge in
// mergeSameKind) are driven by ONE table, boxFieldPolicy, with a rule per
// BoxInfo field and per merge. The table is the specification: a field that
// is not in it is not merged, and TestBoxFieldPolicyCoversEveryField fails the
// build the moment BoxInfo grows a field without a rule. That is what the two
// hand-written merges lacked: the agent hashes were added to BoxInfo and to
// the cache merge but never to the same-cycle merge, and the speaker list
// showed the pre-update hash right after an update.

// mergeSide names which of the two records supplies a field.
type mergeSide uint8

const (
	fromBase  mergeSide = iota // keep the base record's value
	fromOther                  // take the other record's value
)

// mergeInput is what a field rule sees: the two records, their ORIGINAL
// PortVerified flags (the Port rule may flip the merged record's flag, and a
// rule after it must not mistake that for a verified source), and the side
// each earlier field was taken from, for the rules that follow another field
// (a reason follows its verdict).
type mergeInput struct {
	base, other   BoxInfo
	baseV, otherV bool
	picked        map[string]mergeSide
}

// fieldRule decides which record supplies one field. name is for the test
// report and the policy table; pick does the work.
type fieldRule struct {
	name string
	pick func(in mergeInput, field string) mergeSide
}

// boxFieldIndex maps a BoxInfo field name to its index, resolved once.
var boxFieldIndex = func() map[string][]int {
	t := reflect.TypeOf(BoxInfo{})
	m := make(map[string][]int, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		m[t.Field(i).Name] = t.Field(i).Index
	}
	return m
}()

// boxField reads one field of b by name. The name comes from the policy
// table, which the tests check against the struct, so a miss is a programming
// error and panics like any other wrong field access would.
func boxField(b BoxInfo, field string) reflect.Value {
	idx, ok := boxFieldIndex[field]
	if !ok {
		panic(fmt.Sprintf("boxFieldPolicy names no such BoxInfo field: %s", field))
	}
	return reflect.ValueOf(b).FieldByIndex(idx)
}

// The rules. Each returns the side a field is taken from.

// keepBase: the base record's value, always. Per-cycle annotations (Offline,
// OTAPending, STRSilent) and the agent-only readings the CACHE must not
// resurrect (warnings, key refusals) use it on the merge where the other
// side has nothing to say.
var keepBase = fieldRule{"keepBase", func(mergeInput, string) mergeSide { return fromBase }}

// preferNonEmpty: the base's value unless it is empty (zero), then the
// other's. The classic "a thin cycle must not blank what the user saw" rule.
var preferNonEmpty = fieldRule{"preferNonEmpty", func(in mergeInput, f string) mergeSide {
	if boxField(in.base, f).IsZero() {
		return fromOther
	}
	return fromBase
}}

// preferVerified: the live-probed (PortVerified) side's value when it has
// one; the other side's stays when the probe's is empty. With both or
// neither verified, preferNonEmpty. For what a live probe knows better than
// an announcement: the version, the build stamp, the agent hashes (an
// announced version can lag a re-announce after an OTA restart, #108).
var preferVerified = fieldRule{"preferVerified", func(in mergeInput, f string) mergeSide {
	switch {
	case in.otherV && !in.baseV:
		if boxField(in.other, f).IsZero() {
			return fromBase
		}
		return fromOther
	case in.baseV && !in.otherV:
		return fromBase
	}
	return preferNonEmpty.pick(in, f)
}}

// verifiedReading: the live-probed side's value VERBATIM, including its
// empty or false reading, which must clear a stale warning (before v0.9.7
// these fields were never merged, so a single bad snapshot could stick in
// the banner, #270). With both verified the base's; with neither,
// preferNonEmpty. For readings only a live agent probe carries.
var verifiedReading = fieldRule{"verifiedReading", func(in mergeInput, f string) mergeSide {
	switch {
	case in.baseV:
		return fromBase
	case in.otherV:
		return fromOther
	}
	return preferNonEmpty.pick(in, f)
}}

// verifiedID: the deviceID from the live :8090 /info probe (the PortVerified
// record), base first. That is the Bose SoundTouch deviceID (the SCM MAC),
// which the firmware's zone protocol (/setZone, /addGroup) keys on. The mDNS
// TXT instead carries the agent's wlan0 MAC, which on a two-chip chassis
// (ST20 spotty/BCO, Portable) is the SMSC MAC, NOT the SoundTouch ID, so a
// zone formed with it never forms (the master never recognizes itself, a
// slave is never matched). Falls back to whichever side actually has a value.
var verifiedID = fieldRule{"verifiedID", func(in mergeInput, f string) mergeSide {
	switch {
	case in.baseV && !boxField(in.base, f).IsZero():
		return fromBase
	case in.otherV && !boxField(in.other, f).IsZero():
		return fromOther
	}
	return preferNonEmpty.pick(in, f)
}}

// verifiedPort: a verified port beats an unverified one (BCO boxes announce
// :8888 via mDNS but only the REDIRECTed :17008 actually answers; agent calls
// must not hit the firewalled :8888). The base's port stands otherwise, so a
// box that answers on both :8888 and :17008 keeps the port it has instead of
// flipping between refreshes (#1190). PortVerified follows this pick.
var verifiedPort = fieldRule{"verifiedPort", func(in mergeInput, _ string) mergeSide {
	if in.otherV && !in.baseV && in.other.Port != 0 {
		return fromOther
	}
	return fromBase
}}

// follows: the same side as an earlier field, for a value that only means
// something next to its key (a reason next to its verdict, an age next to
// its flag). The key must come before the follower in the table; the test
// checks that.
func follows(key string) fieldRule {
	return fieldRule{"follows(" + key + ")", func(in mergeInput, _ string) mergeSide {
		side, ok := in.picked[key]
		if !ok {
			panic("boxFieldPolicy: follows(" + key + ") before " + key + " was decided")
		}
		return side
	}}
}

// realName: the base's name unless it is generic (empty or Bose's factory
// "Bose SoundTouch <id>") and the other's is real. The cache side of "a thin
// cycle that lost the friendly name must not blank a name the user already
// saw" (the "str-<ip>" flicker, #108).
var realName = fieldRule{"realName", func(in mergeInput, _ string) mergeSide {
	if isGenericBoxName(in.base.FriendlyName) && !isGenericBoxName(in.other.FriendlyName) {
		return fromOther
	}
	return fromBase
}}

// richerName: a real name beats a generic or empty one, and between two real
// names the longer (richer) wins. The same-cycle side of #108, where the mDNS
// record carries the name and the probe does not, in either order.
var richerName = fieldRule{"richerName", func(in mergeInput, _ string) mergeSide {
	if pickBoxName(in.base.FriendlyName, in.other.FriendlyName) == in.other.FriendlyName &&
		in.base.FriendlyName != in.other.FriendlyName {
		return fromOther
	}
	return fromBase
}}

// specificModel: a specific model string beats the generic "SoundTouch"
// fallback (or empty) the agent announces before /info resolves the real type.
var specificModel = fieldRule{"specificModel", func(in mergeInput, _ string) mergeSide {
	if pickModelName(in.base.Model, in.other.Model) == in.other.Model && in.base.Model != in.other.Model {
		return fromOther
	}
	return fromBase
}}

// stickySTR: an STR agent, once seen, outranks a stock-only sighting: a missed
// probeSTR must not relabel a flashed speaker as "needs install".
var stickySTR = fieldRule{"stickySTR", func(in mergeInput, _ string) mergeSide {
	if in.other.Kind == "str" {
		return fromOther
	}
	return fromBase
}}

// fieldPolicy is one row of the table: the rule each merge applies to field.
type fieldPolicy struct {
	field string
	// cache is mergeBoxInfo's rule. Base is this cycle's record, other the
	// cached one: a thinner cycle never downgrades the display, but the
	// cache never resurrects a per-moment reading.
	cache fieldRule
	// sameKind is mergeSameKind's rule. Base is the first sighting of the
	// cycle, other the second; the live-probed side is authoritative for
	// what only a live probe knows.
	sameKind fieldRule
}

// boxFieldPolicy covers every BoxInfo field, in the order the rules depend on
// each other (a follower after its key). TestBoxFieldPolicyCoversEveryField
// keeps it complete; the per-field pins in discovery_merge_test.go keep each
// row honest.
var boxFieldPolicy = []fieldPolicy{
	{"Name", keepBase, preferNonEmpty},
	{"Host", keepBase, keepBase},
	{"Port", verifiedPort, verifiedPort},
	{"PortVerified", follows("Port"), follows("Port")},
	{"DeviceID", preferNonEmpty, verifiedID},
	// A speaker cannot stop being what the firmware calls it, so the last known
	// box id is kept across a cycle in which only mDNS answered (an older agent
	// announces no boxDeviceID) rather than blanked. In one cycle both sides
	// that set it (the firmware's own /info and the agent's TXT copy of it)
	// carry the same value, so the only question is which side has one at all.
	{"BoxDeviceID", preferNonEmpty, preferNonEmpty},
	{"FriendlyName", realName, richerName},
	{"Model", specificModel, specificModel},
	{"Version", preferNonEmpty, preferVerified},
	{"Build", preferNonEmpty, preferVerified},
	// The hash of the agent the speaker runs is what the update badge keys on.
	// It travels with the version: only the live probe reports it, and the
	// cache fills it for a cycle in which only the announcement answered.
	{"AgentBinarySha256", preferNonEmpty, preferVerified},
	{"AgentRunningSha256", preferNonEmpty, preferVerified},
	// Per-cycle annotations: the caller sets them on the served copy, the
	// merge never carries them from one record to the other.
	{"Offline", keepBase, keepBase},
	{"OfflineSinceSec", keepBase, keepBase},
	// STRNotRunning is remembered across stock sightings by mergeBoxInfo's
	// post-step (it rewrites the whole record), not by a field rule.
	{"STRNotRunning", keepBase, keepBase},
	{"STRSilent", keepBase, keepBase},
	{"OTAPending", keepBase, keepBase},
	// BoxHealth: a fresh verdict wins; an empty one (stock sighting or an
	// older agent) keeps the last known state so the pull-the-plug hint does
	// not flicker between discovery cycles.
	{"BoxHealth", preferNonEmpty, verifiedReading},
	// Same for the internet verdict: an mDNS-only sighting carries none, and
	// the "check the router" hint must not flicker between cycles. The reason
	// goes with its verdict.
	{"Internet", preferNonEmpty, verifiedReading},
	{"InternetReason", follows("Internet"), follows("Internet")},
	{"ArcadeAt", preferNonEmpty, verifiedReading},
	// Warning flags (ConflictingMod / ForeignCloudURL / WLANCredsMissing, the
	// 1036 storm and the recall refusal) and the key-refusal readings: only a
	// live agent probe carries them, so within a cycle the PortVerified side is
	// authoritative, INCLUDING its no-warning reading. The cache never fills
	// them: while the agent is not answering, a cached warning may be a stale
	// mid-boot snapshot (a "no Wi-Fi saved" banner stuck on a box that was
	// down, #270 2026-07-12).
	{"ConflictingMod", keepBase, verifiedReading},
	{"ForeignCloudURL", keepBase, verifiedReading},
	{"GroupKeyError", keepBase, verifiedReading},
	{"HoldRefusedSource", keepBase, verifiedReading},
	{"HoldRefusedSlot", keepBase, follows("HoldRefusedSource")},
	{"StaleFolderKeySlot", keepBase, verifiedReading},
	{"Storm1036", keepBase, verifiedReading},
	{"Storm1036SinceSec", keepBase, follows("Storm1036")},
	{"RecallRefusal", keepBase, verifiedReading},
	{"RecallRefusalSinceSec", keepBase, follows("RecallRefusal")},
	{"WLANCredsMissing", keepBase, verifiedReading},
	{"SerialNumber", preferNonEmpty, preferNonEmpty},
	{"Kind", stickySTR, keepBase},
}

// mergeByPolicy applies one column of boxFieldPolicy: out starts as base and
// takes the other record's value for every field whose rule says so.
func mergeByPolicy(base, other BoxInfo, rule func(fieldPolicy) fieldRule) BoxInfo {
	out := base
	in := mergeInput{
		base: base, other: other,
		baseV: base.PortVerified, otherV: other.PortVerified,
		picked: make(map[string]mergeSide, len(boxFieldPolicy)),
	}
	dst := reflect.ValueOf(&out).Elem()
	src := reflect.ValueOf(other)
	for _, p := range boxFieldPolicy {
		side := rule(p).pick(in, p.field)
		in.picked[p.field] = side
		if side == fromOther {
			idx := boxFieldIndex[p.field]
			dst.FieldByIndex(idx).Set(src.FieldByIndex(idx))
		}
	}
	return out
}

// mergeBoxInfo keeps the richer of two records for the same physical box
// so a thinner discovery cycle never downgrades the display. cur is this
// cycle, prev the cached record. This is a safety net; the real fix is to
// query reliably enough (see probe timeouts) that cur is rarely thin.
func mergeBoxInfo(prev, cur BoxInfo) BoxInfo {
	out := mergeByPolicy(cur, prev, func(p fieldPolicy) fieldRule { return p.cache })
	// "STR not running" is remembered across stock sightings of the same box
	// (a later /24 sweep hands in a plain stock record): the tile keeps saying
	// STR is missing rather than flipping to a first-time "Ready for STR". Only
	// an agent that answered a live probe (a verified STR record) lifts it; an
	// STR label backed by nothing but an announcement (the box's responder can
	// keep serving the agent's mDNS record after the agent died) must not
	// resurrect the old version badge on a box whose agent is known to be gone.
	if prev.STRNotRunning {
		if out.Kind == "str" && !out.PortVerified {
			out = degradeToSTRNotRunning(out)
		}
		if out.Kind == "stock" {
			out.STRNotRunning = true
		}
	}
	return out
}

// mergeSameKind combines two discovery records for the same physical box
// (same Host, same Kind) field by field. The mDNS and live-probe sources are
// each authoritative for different fields, so picking one whole record drops
// good data from the other (#108):
//
//   - Version/Build: a PortVerified record is a live HTTP probe of the running
//     agent, so its version is current; an mDNS-announced version can lag a
//     re-announce after an OTA restart. The verified value wins.
//   - FriendlyName / Model: a real (non-generic, non-empty) label beats a
//     generic or empty one, then the longer string wins.
//   - Port: a verified port beats an mDNS-announced one (BCO boxes announce
//     :8888 but only the REDIRECTed :17008 actually answers).
//
// Rules are applied per field, so it does not matter which argument is the
// mDNS record and which is the probe.
func mergeSameKind(a, b BoxInfo) BoxInfo {
	return mergeByPolicy(a, b, func(p fieldPolicy) fieldRule { return p.sameKind })
}

// degradeToSTRNotRunning turns a record for a box whose STR agent no longer
// answers into the stock-speaker record it now is: no agent version, build or
// port, none of the agent-only warning flags, Kind "stock" so the app offers
// the install, plus the STRNotRunning marker so the UI can say why. The box's
// identity (name, model, deviceID, serial, host) is kept.
func degradeToSTRNotRunning(b BoxInfo) BoxInfo {
	b.Kind = "stock"
	b.Version = ""
	b.Build = ""
	b.Port = 8090
	b.PortVerified = false
	b.OTAPending = false
	b.BoxHealth = ""
	b.ConflictingMod = ""
	b.ForeignCloudURL = ""
	b.Internet = ""
	b.InternetReason = ""
	b.WLANCredsMissing = false
	b.Storm1036 = false
	b.Storm1036SinceSec = 0
	b.RecallRefusal = false
	b.RecallRefusalSinceSec = 0
	b.STRSilent = false
	b.STRNotRunning = true
	return b
}

// isGenericBoxName reports whether name is empty or Bose's factory
// default ("Bose SoundTouch <id>"), i.e. a name a real user-assigned one
// should win over.
func isGenericBoxName(name string) bool {
	n := strings.TrimSpace(name)
	return n == "" || strings.HasPrefix(n, "Bose SoundTouch ")
}

// pickBoxName returns the better of two friendly names: a real one beats a
// generic or empty one, and between two real names the longer (richer) wins.
func pickBoxName(a, b string) string {
	ag, bg := isGenericBoxName(a), isGenericBoxName(b)
	if ag && !bg {
		return b
	}
	if bg && !ag {
		return a
	}
	if len(b) > len(a) {
		return b
	}
	return a
}

// pickModelName prefers a specific model string over the generic "SoundTouch"
// fallback (or empty) the agent announces before /info resolves the real type.
func pickModelName(a, b string) string {
	ag := a == "" || a == "SoundTouch"
	bg := b == "" || b == "SoundTouch"
	if ag && !bg {
		return b
	}
	return a
}
