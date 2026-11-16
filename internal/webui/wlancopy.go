// A copy of the Wi-Fi the speaker is already on, kept without touching it.

package webui

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"
)

// PUT /api/box/wlan/copy {"ssid": "...", "password": "..."}
//
// STR keeps its own copy of a speaker's Wi-Fi in wlan-creds. On the
// coprocessor chassis (wlan-mode "bco", the scm SoundTouch 20 and 30, and a
// Portable whose firmware keeps no profile) that copy is what run.sh's offline
// rescue pushes back at the radio chip when the speaker comes up from a cold
// boot without a lease. A speaker set up through the Bose app never gives STR
// a copy, and until now the app turned that into a banner asking the owner to
// "save the Wi-Fi", which ran the full Wi-Fi change: on these chassis that
// writes the network AND restarts the speaker to apply it. The owner of a
// working speaker could not tell what the banner was for (maintainer's own
// fresh SoundTouch 20, 2026-11-15).
//
// This endpoint is the other half of letting the app keep that copy by
// itself: it writes wlan-creds and the supersede baseline, and NOTHING else.
// No wpa_supplicant.conf, no firmware profile store, no goform, no apply
// marker (run.sh's active-provision trigger), no intended-network record (the
// boot guard's steering target), no reboot. The radio and the running
// connection are never touched, so the request cannot move the speaker.
//
// When the copy is used afterwards, exactly as a copy written by any other
// path always was:
//
//   - run.sh's hands-off replay boot: if the speaker gets no lease within 90 s
//     and is not in setup, the offline rescue re-pushes the copy through goform,
//     re-checking before every push that the copy is still current (it stops
//     the moment the agent retires it). That is the purpose.
//   - wifi_failover_seed, only on a speaker that is online AND whose every
//     store positively reports zero Wi-Fi profiles (a cable-only box). The
//     copy is refused on exactly such a speaker (no-firmware-profile), so a
//     copy written here never opens that path.
//   - Never as an active provision: that needs the stick's wlan.conf or the
//     one-shot apply marker, and this endpoint writes neither.
//
// A wrong copy is the risk, because the rescue push programs the chip. So the
// copy is only accepted where it cannot contradict what the speaker itself
// knows (wlanCopyVerdict), and the app only offers one it has evidence for:
// the computer is on that Wi-Fi right now, with that saved password, in the
// subnet the speaker answers on. The owner-setup and supersede checks
// (wlanownersetup.go, wlansupersede.go) retire it the same way as any other
// copy once the owner moves the speaker elsewhere, which is why the baseline
// is retaken here.
//
// An agent older than this endpoint answers the path with its phone page, so
// the app requires the JSON status below and treats anything else as "not
// supported".

// The answer's status for a written copy. The app keys on it.
const wlanCopyStored = "stored-copy"

// Why a copy was not written, as the "code" of the refusal.
const (
	wlanCopyRefusedHasCopy  = "already-has-copy" // STR already holds a copy: never overwritten here
	wlanCopyRefusedRetired  = "retired"          // the owner moved the speaker outside STR
	wlanCopyRefusedNotNeed  = "not-needed"       // this chassis keeps its Wi-Fi by itself
	wlanCopyRefusedInSetup  = "speaker-in-setup" // a setup episode is open or settling
	wlanCopyRefusedOtherNet = "other-network"    // the speaker names or stores a different network
	// the firmware reports no stored Wi-Fi profile at all: a copy would arm
	// run.sh's failover seed, which programs the chip on an online speaker
	wlanCopyRefusedNoProfile = "no-firmware-profile"
	wlanCopyRefusedBusy      = "busy"    // a Wi-Fi change holds the records right now
	wlanCopyRefusedInvalid   = "invalid" // the request itself is unusable
)

// The evidence the verdict accepted the network on, for the log.
const (
	wlanCopyByLive  = "live-association" // the firmware names the network it is on
	wlanCopyByStore = "speaker-store"    // the speaker's own store holds the network
	wlanCopyByNone  = "no-contradiction" // the speaker names nothing; the app's evidence stands alone
)

// wlanCopyFacts is everything the verdict reads off the speaker.
type wlanCopyFacts struct {
	Mode         string      // run.sh's wlan-mode
	HaveCreds    bool        // wlan-creds exists and is not empty
	Retired      bool        // wlan-creds.superseded exists
	SetupOpen    bool        // an owner-setup snapshot exists (wlanownersetup.go)
	FirmwareSSID string      // the firmware's own profile (asked on a Portable)
	Live         string      // the network /networkInfo names, "" when it names none
	Stored       []storedNet // the speaker's own stores
	// NoFirmwareProfile: /networkInfo said wifiProfileCount="0" in so many
	// words (not unreadable). That is the gate of run.sh's wifi_failover_seed,
	// which pushes STR's copy through goform on an ONLINE speaker; a copy
	// written here must never be what opens that path.
	NoFirmwareProfile bool
}

// wlanCopyVerdict decides whether a copy of ssid may be written. Pure, so the
// whole rule is testable off the box. code is "" when it may, and evidence
// says on what.
//
// The order matters. A copy that exists is never touched (this is a
// fill-in, not a change). The chassis rule is wlanCredsWarningFor's, so the
// copy is written exactly where the agent says a missing one matters, and
// nowhere else. Then the network has to agree with whatever the speaker
// itself says: the live name first (strongest), then its stores. A store
// counts only with networks that could be the owner's choice; open ones, the
// setup access point and public hotspots are skipped the way the supersede
// check skips them.
func wlanCopyVerdict(f wlanCopyFacts, ssid string) (code, evidence string) {
	switch {
	case f.HaveCreds:
		return wlanCopyRefusedHasCopy, ""
	case f.Retired:
		return wlanCopyRefusedRetired, ""
	case !wlanCredsWarningFor(f.Mode, func() string { return f.FirmwareSSID }):
		return wlanCopyRefusedNotNeed, ""
	case f.SetupOpen:
		return wlanCopyRefusedInSetup, ""
	case f.NoFirmwareProfile:
		return wlanCopyRefusedNoProfile, ""
	}
	if live := strings.TrimSpace(f.Live); live != "" {
		if live != ssid {
			return wlanCopyRefusedOtherNet, ""
		}
		return "", wlanCopyByLive
	}
	candidates := 0
	for _, n := range f.Stored {
		name := strings.TrimSpace(n.SSID)
		if name == "" || n.Open || isBoseSetupAPName(name) || isPublicHotspot(name) {
			continue
		}
		if name == ssid {
			return "", wlanCopyByStore
		}
		candidates++
	}
	if candidates > 0 {
		return wlanCopyRefusedOtherNet, ""
	}
	return "", wlanCopyByNone
}

// wlanCopyRequestProblem validates the request body, "" when it is usable.
// The creds file is line based (SSID=, PASS=, HIDDEN=), so a line break or a
// NUL in either value would let the request write a line of its own; WPA
// needs 8 to 63 characters (64 for a raw hex key).
func wlanCopyRequestProblem(ssid, password string) string {
	switch {
	case ssid == "":
		return "ssid must not be empty"
	case len(ssid) > 32:
		return "ssid too long (at most 32 bytes)"
	case len(password) < 8:
		return "password too short (at least 8 characters)"
	case len(password) > 64:
		return "password too long (at most 64 characters)"
	case strings.ContainsAny(ssid, "\r\n\x00") || strings.ContainsAny(password, "\r\n\x00"):
		return "ssid and password must not contain line breaks"
	}
	return ""
}

// wlanCopyFactsLive reads the facts on the speaker. File reads plus two
// /networkInfo reads; nothing here writes.
func (s *Server) wlanCopyFactsLive(ctx context.Context) wlanCopyFacts {
	f := wlanCopyFacts{
		Mode:    readWlanMode(),
		Retired: wlanCredsRetired(),
		Stored:  speakerStoredNets(),
		Live:    liveSSIDFrom(s.liveWLANInterfaces(ctx)),
	}
	if s.boxHost != "" {
		f.NoFirmwareProfile = wifiProfileCount(s.boxHost) == 0
	}
	if fi, err := os.Stat(wlanCredsLivePath); err == nil && fi.Size() > 0 {
		f.HaveCreds = true
	}
	if _, err := os.Stat(wlanSetupSnapshotPath); err == nil {
		f.SetupOpen = true
	}
	if f.Mode == "taigan-bco" {
		f.FirmwareSSID = firmwareWifiProfileSSID()
	}
	return f
}

func (s *Server) handleBoxWLANCopy(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPut) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "wlan copy only allowed from LAN", http.StatusForbidden)
		return
	}
	var req struct {
		SSID     string `json:"ssid"`
		Password string `json:"password"`
		// Source is what the app based the copy on ("pc-wifi" for the
		// computer's own connection, "typed" for the owner's entry), for the
		// log only.
		Source string `json:"source"`
	}
	if !decodeJSONRequest(w, r, 2048, &req) {
		return
	}
	req.SSID = strings.TrimSpace(req.SSID)
	if why := wlanCopyRequestProblem(req.SSID, req.Password); why != "" {
		s.logger.Info("WLAN copy: request refused", "why", why)
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": why, "code": wlanCopyRefusedInvalid})
		return
	}
	// A Wi-Fi change in flight owns these files. Never wait for it: the app
	// asks again later, and a change that is running writes its own copy.
	if !s.wlanMu.TryLock() {
		s.logger.Info("WLAN copy: a Wi-Fi change is running, not writing a copy now", "wantTag", ssidTag(req.SSID))
		writeJSON(w, http.StatusConflict, map[string]any{"error": "a Wi-Fi change is running on the speaker", "code": wlanCopyRefusedBusy})
		return
	}
	defer s.wlanMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var f wlanCopyFacts
	if s.wlanCopyFactsFn != nil {
		f = s.wlanCopyFactsFn()
	} else {
		f = s.wlanCopyFactsLive(ctx)
	}
	code, evidence := wlanCopyVerdict(f, req.SSID)
	if code != "" {
		s.logger.Info("WLAN copy: not written", "code", code, "wantTag", ssidTag(req.SSID),
			"mode", f.Mode, "liveTag", ssidTag(f.Live), "storedNets", len(f.Stored), "source", req.Source)
		writeJSON(w, http.StatusConflict, map[string]any{"error": "no copy written: " + code, "code": code})
		return
	}
	if err := writeWlanCredsFile(wlanCredsLivePath, req.SSID, req.Password, false); err != nil {
		s.logger.Warn("WLAN copy: could not write the copy", "err", err, "wantTag", ssidTag(req.SSID))
		http.Error(w, "write wlan copy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// The networks the speaker knows right now are not the owner moving it
	// elsewhere later (wlansupersede.go). Without this the first settings read
	// takes the same baseline; doing it here keeps the two writes together.
	rebaselineWlanKnown(req.SSID)
	s.logger.Info("WLAN copy: STR now keeps a copy of the speaker's Wi-Fi; nothing on the speaker's own Wi-Fi was changed",
		"wantTag", ssidTag(req.SSID), "evidence", evidence, "mode", f.Mode, "source", req.Source)
	writeJSON(w, http.StatusOK, map[string]any{"status": wlanCopyStored, "stored": true, "evidence": evidence})
}
