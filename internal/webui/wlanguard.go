// The boot guard: after a power cycle, check which Wi-Fi the speaker actually
// came up on and move it back if it is the wrong one.
//
// Why this exists at all. Bose's NetManager keeps its OWN network profile
// store and starts wpa_supplicant from it before STR's agent is even running,
// so the firmware, not STR, decides which network the speaker joins at boot.
// STR's move only ever ADDED the new network to that store, never removed the
// old one, and then measured success as "did an IP address appear" rather than
// "is this the network the user chose". A speaker with both networks in range
// therefore came up on the old one and reported success (#461 / #479, proven
// across five diagnostic bundles and seven speakers, 2026-08-21).
//
// The guard runs once per box boot, after NetManager has made its choice, and
// corrects it. It is deliberately timid: it acts only when the speaker IS
// associated (an offline box belongs to run.sh's rescue watchdog, and two
// things pushing an offline box is how you strand it), only when the intended
// network is in the speaker's OWN site survey (so "old network switched off"
// and "moved house" keep working), only while the failure budget lasts, and
// never while a user-initiated switch is running.

package webui

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxwrites"
	"github.com/JRpersonal/streborn/internal/wlanlive"
)

const (
	// wlanGuardSettleBudget is how long NetManager is given to finish picking a
	// network before the guard judges the result. A cold rhino boot reaches
	// wpa_state=COMPLETED well inside this; the ceiling only bounds a box that
	// never associates at all, which is the stand-down case anyway.
	wlanGuardSettleBudget = 150 * time.Second
	wlanGuardPoll         = 5 * time.Second
	// wlanGuardAttempts bounds the correction. Three live switches is already
	// generous: the runtime switch itself escalates internally (reconfigure,
	// restart, add_network) before it reports failure.
	wlanGuardAttempts = 3
)

// guardAction is what the guard decided to do about this boot.
type guardAction int

const (
	guardNone      guardAction = iota // already on the intended network
	guardReapply                      // wrong network and fixable: switch it back
	guardStandDown                    // deliberately do nothing, for a named reason
	guardGiveUp                       // failure budget exhausted, stop for good
)

func (a guardAction) String() string {
	switch a {
	case guardNone:
		return "none"
	case guardReapply:
		return "reapply"
	case guardStandDown:
		return "stand-down"
	case guardGiveUp:
		return "give-up"
	default:
		return "unknown"
	}
}

// The five verdicts, as they appear in the log and in the intent record. Each
// stand-down names its reason so a bundle answers "why did nothing happen"
// without another round trip to the reporter.
const (
	guardReasonBudget     = "boots-budget-exhausted"
	guardReasonNoAssoc    = "no-association-rescue-owns-this"
	guardReasonOnTarget   = "on-target"
	guardReasonNotInRange = "target-not-in-range"
	guardReasonWrongNet   = "wrong-network"
)

// guardDecision is the whole policy, pure and off-box testable.
//
// The order of the cases is the safety order, not a stylistic one:
//
//   - the budget is checked first so an exhausted speaker can never be dragged
//     around again by any later condition;
//   - "not associated" outranks everything else because run.sh's rescue
//     watchdog owns an offline box;
//   - visibility is checked last, and it is the guard that makes the whole
//     feature safe: never move a speaker toward a network it cannot currently
//     see. The worst realistic outcome is then a speaker that stays on the
//     wrong Wi-Fi and says so, not one that is off the network entirely.
func guardDecision(assoc bool, cur, want string, targetVisible bool, bootsFailed int) (guardAction, string) {
	switch {
	case bootsFailed >= maxFailedBoots:
		return guardGiveUp, guardReasonBudget
	case !assoc:
		return guardStandDown, guardReasonNoAssoc
	case cur == want:
		return guardNone, guardReasonOnTarget
	case !targetVisible:
		return guardStandDown, guardReasonNotInRange
	default:
		return guardReapply, guardReasonWrongNet
	}
}

// StartWLANBootGuard runs the guard once, for a real box boot. Safe to call in
// a goroutine at agent start; it returns immediately when there is nothing to
// do.
func (s *Server) StartWLANBootGuard(ctx context.Context, bootReason string) {
	// An agent respawn (watchdog, OOM kill, OTA restart) is not a boot: the
	// firmware did not re-pick a network, so there is nothing to correct and no
	// reason to put the radio through a site survey.
	if !strings.HasPrefix(bootReason, "box-boot") {
		return
	}
	tgt, ok := readWlanTarget()
	if !ok {
		return
	}
	iface, mech := detectWlanMechanism()
	if mech != "wpa" {
		s.wlanGuardWeakVerify(tgt, iface)
		return
	}

	cur, assoc := waitAssociationSettled(ctx, iface, wlanGuardSettleBudget)
	visible, surveyOK := s.targetInSurvey(ctx, tgt.SSID)
	action, reason := guardDecision(assoc, cur, tgt.SSID, visible, tgt.BootsFailed)

	// One line that decides the next bundle. surveyOK separates "the speaker
	// looked and the network was not there" from "the speaker could not look",
	// which are the same decision but very different findings.
	running := runningNetworkTags(iface)
	stored := storedProfileTags()
	s.logger.Info("wlan guard: boot verdict",
		"match", assoc && cur == tgt.SSID, "assoc", assoc,
		"action", action.String(), "reason", reason,
		"wantTag", ssidTag(tgt.SSID), "gotTag", ssidTag(cur),
		"targetVisible", visible, "surveyOK", surveyOK,
		"gen", tgt.Gen, "bootsFailed", tgt.BootsFailed, "verify", tgt.Verify,
		"mech", mech, "iface", iface,
		"runningNets", len(running), "runningTags", running,
		"storeNets", len(stored), "storeTags", stored)

	switch action {
	case guardNone:
		noteWlanTargetVerdict(guardReasonOnTarget, budgetReset)
		return
	case guardGiveUp:
		s.logger.Warn("wlan guard: this speaker has failed to reach the intended network too often, no longer trying (change the network in the app to start over)",
			"wantTag", ssidTag(tgt.SSID), "bootsFailed", tgt.BootsFailed)
		noteWlanTargetVerdict("gave-up:"+reason, budgetHold)
		return
	case guardStandDown:
		// No attempt is burned here on purpose: a speaker that is simply out of
		// range of the intended network must not spend its budget waiting for
		// that network to come back.
		noteWlanTargetVerdict("stood-down:"+reason, budgetHold)
		return
	}

	s.correctWLAN(ctx, iface, tgt, cur)
}

// correctWLAN is the correction loop. It re-runs the same live switch a user
// PUT runs, under the same mutex, so the two can never interleave.
func (s *Server) correctWLAN(ctx context.Context, iface string, tgt wlanTarget, cameUpOn string) {
	// The conf the box actually BOOTED with, snapshotted once. applyWlanWPALive
	// backs up whatever conf is current each time it runs, so from the second
	// attempt onwards its own backup already describes the target and would be
	// useless as a rollback. Without this snapshot a speaker whose every
	// attempt failed would be left pointing at a network it cannot join, i.e.
	// off the LAN entirely — the one outcome worse than the bug.
	var bootConf []byte
	var haveBootConf bool

	attemptOnce := func(attempt int) bool {
		s.wlanMu.Lock()
		defer s.wlanMu.Unlock()
		if !haveBootConf {
			b, err := os.ReadFile(wpaConfPath)
			bootConf, haveBootConf = b, err == nil
		}
		res := s.applyWlanWPALive(iface, tgt.SSID, tgt.PSK, tgt.Hidden)
		s.logger.Info("wlan guard: correction attempt",
			"attempt", attempt, "result", res.String(), "wantTag", ssidTag(tgt.SSID))
		if res != wpaConfirmed {
			return false
		}
		s.finishCorrection(ctx, iface, tgt, cameUpOn, attempt)
		return true
	}

	for attempt := 1; attempt <= wlanGuardAttempts; attempt++ {
		if attemptOnce(attempt) {
			return
		}
		// Back off OUTSIDE the mutex so a user who reaches for the app during
		// the wait is not held up by a correction that has already failed once.
		time.Sleep(time.Duration(20*attempt) * time.Second)
		// A user PUT during the backoff wins. Every move bumps the generation,
		// so a changed (or removed) record means this correction is chasing a
		// network the user no longer wants.
		if cur, ok := readWlanTarget(); !ok || cur.Gen != tgt.Gen {
			s.logger.Info("wlan guard: standing down, the intended network changed while correcting",
				"gen", tgt.Gen)
			return
		}
	}

	// The final backoff is not idle time: wpa_supplicant can complete an
	// association after the switch has already reported failure, especially
	// while NetManager is still re-injecting its own profiles. Check once more
	// before writing this boot off.
	if wpaAssociatedTo(iface, tgt.SSID) {
		s.wlanMu.Lock()
		s.finishCorrection(ctx, iface, tgt, cameUpOn, wlanGuardAttempts)
		s.wlanMu.Unlock()
		return
	}

	// Give up for this boot, but put the speaker back on the network it CAN
	// reach first. The intent record stays: the user's choice has not changed,
	// only this boot's attempt failed.
	if haveBootConf {
		s.wlanMu.Lock()
		if _, werr := writeWPAConf(string(bootConf)); werr != nil {
			s.logger.Warn("wlan guard: could not restore the network the speaker booted with", "err", werr)
		} else {
			reloadWPA(iface)
		}
		s.wlanMu.Unlock()
	}
	s.logger.Warn("wlan guard: could not move the speaker to the intended network",
		"wantTag", ssidTag(tgt.SSID), "gotTag", ssidTag(cameUpOn), "bootsFailed", tgt.BootsFailed+1)
	noteWlanTargetVerdict("failed", budgetBurn)
}

// finishCorrection is everything that has to happen once the speaker is back on
// the intended network. Caller holds wlanMu.
func (s *Server) finishCorrection(ctx context.Context, iface string, tgt wlanTarget, cameUpOn string, attempt int) {
	// Stop the old network from being a candidate in the RUNNING supplicant.
	// NetManager injected it there at boot; leaving it means the next roam or
	// the next reconnect can pick it again within this same uptime.
	s.pruneRunningNetworks(iface, tgt.SSID)
	// Association alone keeps the OLD lease. That goes unnoticed while both
	// networks share a subnet and strands the box the moment they do not (the
	// VLAN-separated UniFi report of 2026-08-21).
	renewDHCPLease()
	// And teach the firmware's own store, so the NEXT boot does not need the
	// guard at all.
	s.commitFirmwareProfile(ctx, tgt.SSID, tgt.PSK)
	// The rollback copy describes the network we just moved away from; a later
	// switch must never restore it.
	_ = os.Remove(wpaBackupPath)
	s.logger.Warn("wlan guard: the speaker came up on another network and was moved back",
		"wantTag", ssidTag(tgt.SSID), "gotTag", ssidTag(cameUpOn), "attempt", attempt)
	noteWlanTargetVerdict("moved-back", budgetReset)
	// Same refresh as the user-initiated live switch (#697): the guard's move
	// changes the address just as much, and hooking only the wpaConfirmed arm
	// would leave the boot-guard path with the stale mDNS/peers/REDIRECT state.
	if s.networkChangedFn != nil {
		go s.networkChangedFn("wlan guard correction")
	}
}

// wlanGuardWeakVerify is the BCO/scm chassis (taigan Portable, mojo ST30).
//
// There is no wpa_supplicant, no wpa_cli, no runtime lever at all, and
// /networkInfo reports ETHERNET_INTERFACE eth0 with no SSID (see
// docs/WIFI-MODELS.md). The only readable fact is which profile the firmware
// considers ACTIVE, and that is a stored-profile check, not an association
// check. It is labelled as such rather than rendered as a green tick that
// means nothing, and the correction loop never runs here: there is nothing on
// this chassis to run it with.
func (s *Server) wlanGuardWeakVerify(tgt wlanTarget, iface string) {
	if s.boxHost == "" {
		// Without the box's own HTTP endpoint there is not even a stored-profile
		// check to make. Say so rather than reporting an unverified speaker as
		// being on the wrong network.
		s.logger.Info("wlan guard: no box host configured, cannot check this chassis at all",
			"wantTag", ssidTag(tgt.SSID), "iface", iface)
		noteWlanTargetVerdict("stood-down:no-box-host", budgetHold)
		return
	}
	active := activeWifiProfile(s.boxHost)
	match := active != "" && active == tgt.SSID
	stored := storedProfileTags()
	s.logger.Info("wlan guard: boot verdict (stored-profile check only; this chassis cannot report what it is associated to)",
		"match", match, "assoc", "unknown",
		"action", guardStandDown.String(), "reason", "no-runtime-channel-on-this-chassis",
		"wantTag", ssidTag(tgt.SSID), "gotTag", ssidTag(active),
		"gen", tgt.Gen, "bootsFailed", tgt.BootsFailed, "verify", "weak",
		"mech", "bco", "iface", iface,
		"storeNets", len(stored), "storeTags", stored)
	if match {
		noteWlanTargetVerdict("on-target (stored-profile check)", budgetReset)
		return
	}
	s.logger.Warn("wlan guard: could not confirm this speaker is on the intended network; this model has no live Wi-Fi channel to check or correct",
		"wantTag", ssidTag(tgt.SSID))
	noteWlanTargetVerdict("unverified (stored-profile check)", budgetHold)
}

// waitAssociationSettled polls until wpa_supplicant reports an association or
// the budget runs out, and returns the SSID it settled on.
func waitAssociationSettled(ctx context.Context, iface string, budget time.Duration) (ssid string, associated bool) {
	deadline := time.Now().Add(budget)
	for {
		if st := wlanlive.Read(ctx, iface); st.Associated {
			return st.SSID, true
		}
		if !time.Now().Before(deadline) {
			return "", false
		}
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(wlanGuardPoll):
		}
	}
}

// targetInSurvey asks the SPEAKER what it can see. visible says the target is
// in the list; ok says the list could be read at all. An unreadable or empty
// survey is absence of evidence, and the guard treats it as "do not act": a
// correction toward a network nobody can confirm is in range is exactly the
// move that strands a speaker.
func (s *Server) targetInSurvey(ctx context.Context, want string) (visible, ok bool) {
	if s.boxHost == "" {
		return false, false
	}
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ssids, err := boxapi.New(s.boxHost).SiteSurvey(sctx)
	if err != nil || len(ssids) == 0 {
		return false, false
	}
	for _, sid := range ssids {
		if sid == want {
			return true, true
		}
	}
	return false, true
}

// pruneRunningNetworks removes every network except the target from the
// RUNNING supplicant, so the old one stops being a roam candidate for the rest
// of this uptime. It touches only what wpa_supplicant holds in memory (plus
// its own save_config); the firmware's profile file on NAND is a separate
// question and is not edited here.
func (s *Server) pruneRunningNetworks(iface, keep string) {
	if _, err := exec.LookPath("wpa_cli"); err != nil {
		return
	}
	nets := wpaListNetworks(iface)
	ids := wpaNetworksToRemove(nets, keep)
	if len(ids) == 0 {
		return
	}
	for _, id := range ids {
		_ = exec.Command("wpa_cli", "-i", iface, "remove_network", id).Run()
	}
	// save_config needs update_config=1, which both the vendor template and
	// buildWPAConfig set. Where it is absent this FAILs harmlessly and the
	// pruning still holds for this uptime.
	_ = exec.Command("wpa_cli", "-i", iface, "save_config").Run()
	boxwrites.Note("wlan-profile", "prune-running")
	s.logger.Info("wlan guard: removed the other networks from the running supplicant",
		"removed", len(ids), "before", len(nets), "keptTag", ssidTag(keep))
}

// wpaListNetworks reads what the running supplicant is configured for. Empty
// when wpa_cli is absent or the control interface does not answer.
func wpaListNetworks(iface string) []wlanNetwork {
	out, err := exec.Command("wpa_cli", "-i", iface, "list_networks").Output()
	if err != nil {
		return nil
	}
	return parseWPANetworkList(string(out))
}

// wpaNetworksToRemove picks the network ids to drop. Pure, so the one decision
// that can lock a speaker out of every network it knows is testable without a
// live supplicant.
//
// It refuses to return anything unless the network to keep is actually in the
// list: pruning toward an SSID the supplicant does not carry would leave zero
// networks, and a speaker with zero networks falls back to the Bose setup AP
// and disappears from the LAN.
func wpaNetworksToRemove(nets []wlanNetwork, keep string) []string {
	if keep == "" {
		return nil
	}
	found := false
	for _, n := range nets {
		if n.SSID == keep {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	var ids []string
	for _, n := range nets {
		if n.SSID != keep {
			ids = append(ids, strconv.Itoa(n.ID))
		}
	}
	return ids
}

// runningNetworkTags is the diagnostic view of the running supplicant: how
// many networks it carries and which, as scrub-proof tags.
func runningNetworkTags(iface string) []string {
	return ssidTagsOf(wpaListNetworks(iface))
}

// storedProfileTags is the same for the FIRMWARE's own profile store, the list
// that actually decides the boot association.
func storedProfileTags() []string {
	return ssidTagsOf(bcoStoredProfiles())
}
