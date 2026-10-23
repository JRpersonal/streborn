package main

import "strings"

// Pure record-merge rules for two sightings of the same speaker. Split out of app_discovery.go.

// mergeBoxInfo keeps the richer of two records for the same physical box
// so a thinner discovery cycle never downgrades the display. cur is this
// cycle, prev the cached record. This is a safety net; the real fix is to
// query reliably enough (see probe timeouts) that cur is rarely thin.
func mergeBoxInfo(prev, cur BoxInfo) BoxInfo {
	out := cur
	// An STR agent, once seen, outranks a stock-only sighting: a missed
	// probeSTR must not relabel a flashed speaker as "needs install".
	if prev.Kind == "str" && out.Kind != "str" {
		out.Kind = "str"
		if out.Version == "" {
			out.Version = prev.Version
		}
		if out.Build == "" {
			out.Build = prev.Build
		}
		if out.AgentBinarySha256 == "" {
			out.AgentBinarySha256 = prev.AgentBinarySha256
		}
		if out.AgentRunningSha256 == "" {
			out.AgentRunningSha256 = prev.AgentRunningSha256
		}
		if prev.PortVerified && !out.PortVerified && prev.Port != 0 {
			out.Port = prev.Port
			out.PortVerified = true
		}
	}
	if isGenericBoxName(out.FriendlyName) && !isGenericBoxName(prev.FriendlyName) {
		out.FriendlyName = prev.FriendlyName
	}
	if out.Version == "" {
		out.Version = prev.Version
	}
	if (out.Model == "" || out.Model == "SoundTouch") && prev.Model != "" && prev.Model != "SoundTouch" {
		out.Model = prev.Model
	}
	if out.DeviceID == "" {
		out.DeviceID = prev.DeviceID
	}
	// A speaker cannot stop being what the firmware calls it, so the last known
	// box id is kept across a cycle in which only mDNS answered (an older agent
	// announces no boxDeviceID) rather than blanked.
	if out.BoxDeviceID == "" {
		out.BoxDeviceID = prev.BoxDeviceID
	}
	if out.SerialNumber == "" {
		out.SerialNumber = prev.SerialNumber
	}
	if out.Build == "" {
		out.Build = prev.Build
	}
	if out.AgentBinarySha256 == "" {
		out.AgentBinarySha256 = prev.AgentBinarySha256
	}
	if out.AgentRunningSha256 == "" {
		out.AgentRunningSha256 = prev.AgentRunningSha256
	}
	// BoxHealth: a fresh verdict wins; an empty one (stock sighting or an
	// older agent) keeps the last known state so the pull-the-plug hint does
	// not flicker between discovery cycles.
	if out.BoxHealth == "" {
		out.BoxHealth = prev.BoxHealth
	}
	// Same for the internet verdict: an mDNS-only sighting carries none, and
	// the "check the router" hint must not flicker between cycles.
	if out.Internet == "" {
		out.Internet = prev.Internet
		out.InternetReason = prev.InternetReason
	}
	if out.ArcadeAt == "" {
		out.ArcadeAt = prev.ArcadeAt
	}
	if prev.PortVerified && !out.PortVerified && prev.Port != 0 {
		out.Port = prev.Port
		out.PortVerified = true
	}
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
	out := a
	out.FriendlyName = pickBoxName(a.FriendlyName, b.FriendlyName)
	out.Model = pickModelName(a.Model, b.Model)

	// Version/Build: the live-probed record is the source of truth.
	switch {
	case b.PortVerified && !a.PortVerified:
		if b.Version != "" {
			out.Version = b.Version
		}
		if b.Build != "" {
			out.Build = b.Build
		}
	case a.PortVerified && !b.PortVerified:
		// keep a's version/build
	default:
		if out.Version == "" {
			out.Version = b.Version
		}
		if out.Build == "" {
			out.Build = b.Build
		}
	}

	// Port: prefer a verified one.
	if b.PortVerified && !a.PortVerified && b.Port != 0 {
		out.Port = b.Port
		out.PortVerified = true
	}

	// DeviceID: prefer the value from the live :8090 /info probe (the
	// PortVerified record). That is the Bose SoundTouch deviceID (the SCM MAC),
	// which the firmware's zone protocol (/setZone, /addGroup) keys on. The mDNS
	// TXT instead carries the agent's wlan0 MAC, which on a two-chip chassis
	// (ST20 spotty/BCO, Portable) is the SMSC MAC, NOT the SoundTouch ID, so a
	// zone formed with it never forms (the master never recognizes itself, a
	// slave is never matched). Fall back to whichever side actually has a value.
	// Test against the ORIGINAL verified flags: the port-merge above may have
	// already flipped out.PortVerified to b's, which would otherwise make the
	// stale mDNS deviceID look verified.
	switch {
	case a.PortVerified && a.DeviceID != "":
		out.DeviceID = a.DeviceID
	case b.PortVerified && b.DeviceID != "":
		out.DeviceID = b.DeviceID
	case out.DeviceID == "":
		out.DeviceID = b.DeviceID
	}
	// BoxDeviceID has no such contest: both sides that set it (the firmware's
	// own /info and the agent's TXT copy of it) carry the same value, so the
	// only question is which side has one at all.
	if out.BoxDeviceID == "" {
		out.BoxDeviceID = b.BoxDeviceID
	}
	if out.SerialNumber == "" {
		out.SerialNumber = b.SerialNumber
	}
	if out.Name == "" {
		out.Name = b.Name
	}

	// Warning flags (ConflictingMod / WLANCredsMissing / BoxHealth): only a
	// live agent probe carries them, so the PortVerified side is
	// authoritative — INCLUDING its no-warning reading, which must clear a
	// stale cached warning (before v0.9.7 these fields were never merged, so
	// a single bad snapshot could stick in the banner, #270). Test against
	// the ORIGINAL flags, same as DeviceID above. With no verified side, keep
	// whichever record has a value.
	switch {
	case a.PortVerified:
		// out already carries a's values verbatim
	case b.PortVerified:
		out.ConflictingMod = b.ConflictingMod
		out.ForeignCloudURL = b.ForeignCloudURL
		out.WLANCredsMissing = b.WLANCredsMissing
		out.BoxHealth = b.BoxHealth
		out.Internet = b.Internet
		out.InternetReason = b.InternetReason
		out.ArcadeAt = b.ArcadeAt
		out.Storm1036 = b.Storm1036
		out.Storm1036SinceSec = b.Storm1036SinceSec
		out.RecallRefusal = b.RecallRefusal
		out.RecallRefusalSinceSec = b.RecallRefusalSinceSec
	default:
		if out.ConflictingMod == "" {
			out.ConflictingMod = b.ConflictingMod
		}
		if out.ForeignCloudURL == "" {
			out.ForeignCloudURL = b.ForeignCloudURL
		}
		if !out.Storm1036 {
			out.Storm1036 = b.Storm1036
			out.Storm1036SinceSec = b.Storm1036SinceSec
		}
		if !out.RecallRefusal {
			out.RecallRefusal = b.RecallRefusal
			out.RecallRefusalSinceSec = b.RecallRefusalSinceSec
		}
		if !out.WLANCredsMissing {
			out.WLANCredsMissing = b.WLANCredsMissing
		}
		if out.BoxHealth == "" {
			out.BoxHealth = b.BoxHealth
		}
		if out.Internet == "" {
			out.Internet = b.Internet
			out.InternetReason = b.InternetReason
		}
		if out.ArcadeAt == "" {
			out.ArcadeAt = b.ArcadeAt
		}
	}
	return out
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
