// A Wi-Fi change that asks for what the speaker already has.

package webui

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
)

// The setup's last step sends the Wi-Fi the owner typed in to PUT
// /api/box/wlan, and so does anybody who saves the Wi-Fi form without
// changing it. When the speaker is ALREADY on that network with that
// password, the live switch has nothing to gain and a connection to lose.
// Measured on an ST10, 2026-11-10, same network and same password seven times
// in a row: every run re-associated the speaker (it sat on another access
// point for a while), every run made the firmware's NetManager stop its DHCP
// client with a DHCPRELEASE and start a new one, and in one run of the seven
// the router then handed out a different address, so everything that still
// held the old one saw the speaker as gone.
//
// So such a request is answered without touching the running connection: no
// conf rewrite, no reconfigure, no reassociate, no supplicant restart, no
// lease renewal. STR's own records are still brought to the state a confirmed
// switch leaves behind, because recording the network is what the setup step
// exists for.
//
// The hard part is the password. Being associated proves the NAME only: the
// speaker may hold the network through the firmware's own stored profile, and
// that says nothing about what the owner just typed. Accepting an unproven
// password would store a wrong one as known good, in wlan-creds (which the
// boot path replays) and in the intended-network record (which the boot guard
// corrects towards). What the agent can compare against, and why only one of
// them counts:
//
//   - the intended-network record with verify "live" (wlantarget.go). It is
//     only ever written after wpa_supplicant confirmed an association made
//     with exactly these credentials. THIS is the proof.
//   - the same record with verify "weak", and wlan-creds: both are written
//     before anything was observed (cabled store, reboot-to-apply), so a
//     mistyped password can sit in either while the speaker is on the network
//     through the firmware's profile. Not proof.
//   - the running supplicant: wpa_cli masks the psk of a network ("*").
//   - the firmware store (NetworkProfiles.xml): the passphrase is ciphertext
//     only NetManager can read (wlanpriority.go).
//
// Consequence, stated plainly: a FIRST install, on a speaker STR never
// switched before, has no such record and still takes the live switch. The
// no-op covers every repeat of a change STR itself confirmed earlier.

// Why a same-network request did or did not qualify, for the log.
const (
	wlanKeepProven        = "verified-record"
	wlanKeepNoRecord      = "no-record"
	wlanKeepOtherRecord   = "record-is-another-network"
	wlanKeepWeakRecord    = "record-never-verified"
	wlanKeepHiddenRecord  = "record-is-hidden"
	wlanKeepOtherPassword = "password-differs"
)

// wlanKeepEligible reports whether the request has the plain shape the no-op
// is defined for: a live-switch chassis off the cable, a visible, protected
// network, and no forget-the-others side job. Everything else keeps the old
// path without a second look.
func wlanKeepEligible(mode, password string, hidden, open, forgetOthers bool) bool {
	return mode == wlanSwitchLive && !hidden && !open && password != "" && !forgetOthers
}

// wlanPasswordProven decides whether password is provably the one this
// speaker's association to ssid was made with (see the file comment for what
// counts). Pure, so the whole rule is testable off the box.
func wlanPasswordProven(tgt wlanTarget, hasTarget bool, ssid, password string) (bool, string) {
	switch {
	case !hasTarget:
		return false, wlanKeepNoRecord
	case tgt.SSID != ssid:
		return false, wlanKeepOtherRecord
	case tgt.Verify != "live":
		return false, wlanKeepWeakRecord
	case tgt.Hidden:
		return false, wlanKeepHiddenRecord
	case subtle.ConstantTimeCompare([]byte(tgt.PSK), []byte(password)) != 1:
		return false, wlanKeepOtherPassword
	}
	return true, wlanKeepProven
}

// storeHoldsSSID reports whether the speaker's own stores already carry ssid.
func storeHoldsSSID(nets []storedNet, ssid string) bool {
	for _, n := range nets {
		if n.SSID == ssid {
			return true
		}
	}
	return false
}

// ensureWlanCredsAt makes the creds file hold exactly these credentials,
// without a write when it already does (the NAND is spared on every repeat),
// and without leaving a rollback backup: nothing here can need rolling back.
func ensureWlanCredsAt(path, ssid, password string) (wrote bool, err error) {
	want := fmt.Sprintf("SSID=%s\nPASS=%s\n", ssid, password)
	if have, rerr := os.ReadFile(path); rerr != nil || string(have) != want {
		if werr := writeWlanCredsFile(path, ssid, password, false); werr != nil {
			return false, werr
		}
		wrote = true
	}
	_ = os.Remove(path + ".bak")
	return wrote, nil
}

// wlanMechanism is detectWlanMechanism behind the per-server test seam.
func (s *Server) wlanMechanism() (iface, mech string) {
	if s.wlanMechFn != nil {
		return s.wlanMechFn()
	}
	return detectWlanMechanism()
}

// wlanAssociated is wpaAssociatedTo behind the per-server test seam.
func (s *Server) wlanAssociated(iface, ssid string) bool {
	if s.wlanAssociatedFn != nil {
		return s.wlanAssociatedFn(iface, ssid)
	}
	return wpaAssociatedTo(iface, ssid)
}

// keepCurrentWLAN answers a change request for the network the speaker is
// already on with the password already in use, and reports whether it did.
// false means nothing was written and nothing was answered: the caller
// carries on with the ordinary switch.
func (s *Server) keepCurrentWLAN(w http.ResponseWriter, iface, mech, mode, ssid, password string, hidden, open, forgetOthers bool) bool {
	if !wlanKeepEligible(mode, password, hidden, open, forgetOthers) {
		return false
	}
	if !s.wlanAssociated(iface, ssid) {
		return false
	}
	// A switch in flight owns the records and the radio; the request then
	// queues behind it on the ordinary path, exactly as before. The lock is
	// taken BEFORE the password is compared on purpose: a wrong password runs
	// a live switch under this lock, so while one is being tried no second
	// guess gets an answer from here. This endpoint must not become a quick
	// yes/no on the Wi-Fi password for anyone on the LAN.
	if !s.wlanMu.TryLock() {
		return false
	}
	tgt, hasTarget := readWlanTarget()
	proven, why := wlanPasswordProven(tgt, hasTarget, ssid, password)
	if !proven {
		s.wlanMu.Unlock()
		s.logger.Info("WLAN: the speaker is already on the requested network, but the password is not proven to be the one in use; switching as before",
			"why", why, "wantTag", ssidTag(ssid), "iface", iface)
		return false
	}
	wroteCreds, err := ensureWlanCredsAt(wlanCredsLivePath, ssid, password)
	if err != nil {
		s.wlanMu.Unlock()
		s.logger.Warn("WLAN: could not record the credentials of the network the speaker is already on; switching as before",
			"err", err, "wantTag", ssidTag(ssid))
		return false
	}
	rebaselineWlanKnown(ssid)
	// Re-arm rather than leave the record as it is: a confirmed switch resets
	// the boot guard's failure budget and bumps the generation, and the owner
	// stating the same network again must do the same.
	s.armWlanTarget(ssid, password, false, "live")
	storeHas := storeHoldsSSID(speakerStoredNets(), ssid)

	resp := wlanSwitchResponse(mode, mech, ssid, false)
	resp["status"] = "unchanged"
	resp["unchanged"] = true
	writeJSON(w, http.StatusOK, resp)
	s.logger.Info("WLAN: nothing to change, the speaker is already on this network with this password; the running connection is left alone",
		"why", why, "wantTag", ssidTag(ssid), "iface", iface, "mechanism", mech,
		"credsWritten", wroteCreds, "storeHasProfile", storeHas)
	go func() {
		defer s.wlanMu.Unlock()
		s.finishKeptWLAN(ssid, password, storeHas)
	}()
	return true
}

// finishKeptWLAN is the part of a confirmed switch that concerns the
// firmware's own store and never the running connection. Caller holds wlanMu.
//
// Left out on purpose, because each of them acts on the live session:
// pruneRunningNetworks (talks to the running supplicant), renewDHCPLease (the
// DHCPRELEASE this whole file exists to avoid) and the network-changed
// refresh (there is no change).
func (s *Server) finishKeptWLAN(ssid, password string, storeHas bool) {
	if !storeHas {
		// The speaker holds the network through STR's conf alone, so the
		// firmware would not find it after a power cycle: teach its store, and
		// let the next boot program the network once, as after a real switch.
		s.commitFirmwareProfile(context.Background(), ssid, password)
		touchWLANApplyMarker()
	}
	// File only, and no write at all when the ranking is already right.
	s.raiseFirmwareProfilePriority(ssid)
}
