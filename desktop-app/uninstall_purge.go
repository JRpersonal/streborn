package main

// Purge of the app's own memory of a speaker after STR was removed from it.
//
// UninstallSTR puts the speaker back to stock, but the app kept remembering
// it as the STR speaker it used to be: the agent port it answered on, the
// post-OTA pin, an unfinished update intent (which toasted "update unfinished"
// for two weeks on a speaker that had no STR left), the stereo-pair name it
// was half of, the world-map "already invited for this box" flag, and its
// line in known-speakers.json. None of those records is true any more once the
// speaker is stock, and each one made the speaker come back looking like
// something other than the plain installable speaker it now is.
//
// purgeSpeakerState drops every one of them. It is deliberately the ONLY
// place that knows the full list, so a new per-speaker record only has to be
// added here. History files (ota-history.log, str.log) are kept on purpose:
// they are a record of what happened, not a claim about the speaker's state.
// The preset stash (preset_stash.go) is kept for the same reason: it records
// what was on the speaker's keys, and the next install reads it (#882).
//
// What is NOT purged, because it lives off this PC: the removed speaker stays
// in its former peers' sticky roster (POST /api/peers/seed is additive; each
// agent ages it out on its own) and as a remembered member of a permanent
// group on its master. Both are a decision for the fleet, not for this PC.

import "strings"

// worldMapProvisionedFlagPrefix mirrors the frontend's
// WORLD_MAP_PROVISIONED_PREFIX (main.js): the per-box "pin invite already
// shown after install" flag, keyed by deviceID, else host.
const worldMapProvisionedFlagPrefix = "str.worldMapProvisioned."

// purgeSpeakerState forgets everything the app keeps about one speaker, in
// memory and on disk, so it comes back as a plain installable speaker with
// nothing cached against it. deviceID may be empty: it is then resolved from
// the discovery memory, which is why this must run BEFORE that memory is
// dropped by forgetSTRDeviceByHost (it drops that memory itself, so calling
// forgetSTRDeviceByHost again afterwards is a harmless no-op). Best-effort
// throughout: an uninstall that succeeded on the speaker must never be
// reported as failed because a cache file on the PC could not be rewritten.
func (a *App) purgeSpeakerState(host, deviceID string) {
	host = strings.TrimSpace(host)
	deviceID = strings.TrimSpace(deviceID)
	if host == "" && deviceID == "" {
		return
	}
	if deviceID == "" {
		deviceID = a.deviceIDForHost(host)
	}

	// In-memory records.
	a.forgetSTRDeviceByHost(host)
	a.forgetSTRDevice(deviceID)
	a.forgetPort(host)
	a.clearPostOTA(host)
	boxSSHClients.forgetHost(host)
	// TODO(#866): call a.forgetOTAVerify(host) here once App.otaVerify lands on main.

	// On-disk records under the OS user-config dir.
	a.ClearUpdateIntent(host, 0)
	names, nerr := removeStereoNamesFor(deviceID)
	if nerr != nil {
		a.logger.Warn("uninstall_str: purge could not drop the speaker's stereo-pair names", "host", host, "err", nerr)
	}
	if err := deleteAppFlags(worldMapProvisionedFlags(host, deviceID)...); err != nil {
		a.logger.Warn("uninstall_str: purge could not drop the speaker's app flags", "host", host, "err", err)
	}
	// Unconditional rewrite: with the speaker gone from the discovery cache the
	// snapshot no longer lists it, and the last speaker's removal must write an
	// empty list rather than leave the stale file behind forever.
	a.persistKnownSpeakersWith(true)

	a.logger.Info("uninstall_str: purged the app's memory of the speaker",
		"host", host, "deviceID", deviceID, "stereoNamesDropped", names)
}

// deviceIDForHost resolves the speaker's stable deviceID from the discovery
// memory: the per-IP cache first, then the confirmed-STR identity map in case
// the box had moved IPs. Empty when the app never learned it.
func (a *App) deviceIDForHost(host string) string {
	if host == "" {
		return ""
	}
	a.discMu.Lock()
	defer a.discMu.Unlock()
	if e, ok := a.discCache[host]; ok && e.box.DeviceID != "" {
		return e.box.DeviceID
	}
	for id, e := range a.strKnown {
		if e.box.Host == host {
			if e.box.DeviceID != "" {
				return e.box.DeviceID
			}
			return id
		}
	}
	for _, e := range a.discCache {
		if e.box.Host == host && e.box.DeviceID != "" {
			return e.box.DeviceID
		}
	}
	return ""
}

// forgetSTRDevice is forgetSTRDeviceByHost's deviceID-keyed twin: it drops the
// confirmed-STR identity memory and every cache entry carrying this deviceID,
// including ones under an IP the speaker no longer holds (a DHCP move between
// the last discovery and the uninstall would otherwise leave the old record
// re-promoting a stock sighting to STR).
func (a *App) forgetSTRDevice(deviceID string) {
	if deviceID == "" {
		return
	}
	a.discMu.Lock()
	defer a.discMu.Unlock()
	for id, e := range a.strKnown {
		if strings.EqualFold(id, deviceID) || strings.EqualFold(e.box.DeviceID, deviceID) {
			delete(a.strKnown, id)
		}
	}
	for key, e := range a.discCache {
		if strings.EqualFold(e.box.DeviceID, deviceID) {
			delete(a.discCache, key)
		}
	}
}

// worldMapProvisionedFlags lists the per-box world-map flags the frontend may
// have set for this speaker: it keys them by deviceID when known and by host
// before STR ever ran (the deviceID only becomes visible once the agent is
// up), so both spellings are dropped.
func worldMapProvisionedFlags(host, deviceID string) []string {
	var out []string
	for _, id := range []string{deviceID, host} {
		if id == "" {
			continue
		}
		out = append(out, worldMapProvisionedFlagPrefix+id)
	}
	return out
}
