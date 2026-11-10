// Forming a stereo pair (POST /api/box/zone with "stereo":true): the guards
// against pairing a grouped speaker, the /addGroup drive with its retries
// and read-backs, the firmware's error codes, and the capture of what the
// partner was playing. Split out of zones_stereo.go.

package webui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/zones"
)

// formStereoPair drives POST /addGroup to make a real left/right stereo pair
// and persists it so it is honored on dissolve. master becomes LEFT, the single
// partner becomes RIGHT. The firmware decides whether the box can pair (ST10
// only); its error is returned verbatim to the app so testers see the truth.
// dropStereoDocAfterFailure removes the pair document that was written before
// the firmware was asked.
//
// Writing it first is deliberate: a timed-out /addGroup can still have formed
// the pair, and the dissolve path needs to know it is a pair. What was missing
// is the other half. When the firmware refuses, or binds only one channel, the
// document stayed on NAND and the speaker then behaved as if it were half of a
// pair for good: a permanent pair card in the app, a volume scope that writes
// to a speaker in another room, and, silently, power-on resume disabled,
// because boxInZone reads the store alone. The user had been told the pairing
// failed, so they had no reason to press "undo".
func (s *Server) dropStereoDocAfterFailure(why string) {
	if s.zones == nil {
		return
	}
	z, ok := s.zones.Get()
	if !ok || !z.Stereo {
		return // nothing of ours to take back
	}
	if err := s.clearZoneDoc(); err != nil {
		s.logger.Warn("stereo: could not remove the pair document after a failed pairing", "err", err, "why", why)
		return
	}
	s.logger.Info("stereo: pairing failed, the pair document was removed again", "why", why)
}

// zonedPairCandidates returns the addresses of the speakers a pairing would
// involve that are in a live multiroom zone right now: this box first, then the
// partner. Empty means neither is grouped - or neither could be asked, which is
// deliberately the same answer: refusing a pairing because a speaker was busy
// for a moment would block a legitimate action on no evidence, the same
// best-effort policy the desktop's own boxInStereoPair guard follows.
//
// Reads through the Server's fetchZone seam, so tests need no :8090 server.
func (s *Server) zonedPairCandidates(ctx context.Context, partnerIP string) []string {
	hosts := make([]string, 0, 2)
	if s.boxHost != "" {
		hosts = append(hosts, s.boxHost)
	}
	if partnerIP != "" && !strings.EqualFold(partnerIP, s.boxHost) {
		hosts = append(hosts, partnerIP)
	}
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		zctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		z, err := s.fetchZone(zctx, h)
		cancel()
		if err != nil {
			s.logger.Info("stereo: could not read the live zone before pairing, going ahead", "host", h, "err", err)
			continue
		}
		if strings.TrimSpace(z.Master) == "" && len(z.Members) == 0 {
			continue
		}
		s.logger.Info("stereo: speaker reports a live multiroom zone", "host", h, "master", z.Master, "members", len(z.Members))
		out = append(out, h)
	}
	return out
}

// storedGroupBlocksPair reports whether this speaker keeps a saved multiroom
// group that a pairing would destroy, and returns it for the log line.
//
// It answers for the group the live zone read CANNOT see. A permanent group
// whose master is idle is STORED, not live: both speakers answer no master and
// no members, so the live guard waves the pairing through, and formStereoPair's
// persist then replaces the group document in this box's single-slot store with
// the pair. The group is gone, silently, and this document is its only copy -
// nothing re-creates it. That is the loss class this project has already paid
// for once (a wiped zones.json, 2026-09-03), so a group that is merely waiting
// for its master to play still counts as a group.
//
// PERMANENT only, and that is the whole point of the check. A temporary group
// lives exactly as long as the live zone does, so the firmware's own answer is
// the truth about it and zonedPairCandidates, which runs first, already refuses
// a pairing while that zone stands. What is left in the store afterwards is a
// document nobody can see any more: a temporary group ends with the next
// standby, the frontend picker does not show it, and refusing on it would tell
// a user their two speakers cannot be paired because of a group that no longer
// exists - forever, with no way to clear it from the app.
//
// A stored PAIR (z.Stereo) never blocks either: re-pairing the same two
// speakers and renaming an existing pair both come through formStereoPair and
// both must stay possible. Neither does a document with no members left in it.
func (s *Server) storedGroupBlocksPair() (bool, zones.Zone) {
	if s.zones == nil {
		return false, zones.Zone{}
	}
	z, ok := s.zones.Get()
	if !ok || z.Stereo || !z.Permanent || len(z.Slaves) == 0 {
		return false, z
	}
	return true, z
}

func (s *Server) formStereoPair(w http.ResponseWriter, ctx context.Context, c *boxapi.Client, master boxapi.ZoneMember, slaves []boxapi.ZoneMember, name string, woken map[string]bool) {
	if len(slaves) != 1 {
		http.Error(w, "a stereo pair needs exactly one partner speaker", http.StatusBadRequest)
		return
	}
	master.Role = "LEFT"
	partner := slaves[0]
	partner.Role = "RIGHT"
	// The partner's address arrives in the request body and is then dialled
	// several times over: its /info is read, the pair document is pushed to it,
	// and a stale pair is removed from it. A stereo partner is by definition
	// another speaker on this LAN, so anything else is either a mistake or an
	// attempt to aim the speaker at a host it has no business reaching. Rejecting
	// it here covers every one of those dials with a single check.
	if partner.IP != "" && !isLANPeer(partner.IP) {
		s.logger.Warn("stereo: refusing a partner address that is not on the local network", "partnerIP", partner.IP)
		http.Error(w, "the partner speaker must be on the local network", http.StatusBadRequest)
		return
	}
	if name == "" {
		name = "Stereo pair"
	}

	// Resolve the partner's REAL SoundTouch deviceID from its OWN firmware /info.
	// The app derives a member's deviceID from mDNS, where a two-chip chassis
	// announces its wlan0/SMSC MAC, not the deviceID the firmware keys /addGroup
	// on. localDeviceID already corrects this for the master; the partner (RIGHT)
	// needs the same, or AddGroup embeds the wrong chip's MAC and the firmware
	// silently drops the channel (live: an ST10+ST10 pair never formed, #70).
	if partner.IP != "" {
		if pinfo, perr := boxapi.New(partner.IP).GetInfo(ctx); perr == nil {
			if real := strings.TrimSpace(pinfo.DeviceID); real != "" {
				if !strings.EqualFold(real, partner.DeviceID) {
					s.logger.Info("stereo: corrected partner deviceID from its firmware /info (app sent the chassis MAC, not the SoundTouch ID)",
						"supplied", partner.DeviceID, "firmware", real, "partnerIP", partner.IP)
				}
				partner.DeviceID = real
			}
			// Bose stereo /addGroup needs both speakers set up on the SAME marge
			// account; an empty account is the usual silent reject (a tester's box-4).
			if strings.TrimSpace(pinfo.MargeAccountUUID) == "" {
				s.logger.Warn("stereo: partner has no marge account, /addGroup will likely be rejected (set the speaker up first)", "partnerIP", partner.IP)
			}
		} else {
			s.logger.Warn("stereo: could not read partner /info, using the app-supplied deviceID", "err", perr, "partnerIP", partner.IP)
		}
	}

	// A speaker that is in a live multiroom ZONE must not become half of a
	// stereo pair. STR guarded the reverse direction only: forming a zone is
	// refused when a member is half of a pair (#792), while forming a PAIR out
	// of two speakers that are already grouped was checked nowhere - not here,
	// not in the desktop, not in the frontend. The firmware then keeps the zone
	// standing behind the fresh pair: a third speaker goes on following the
	// pair's master and starts refusing stations, and this agent's single-slot
	// zone store overwrites the group document with the pair, so the group can
	// no longer even be taken apart from STR (field, 2026-09-07).
	//
	// BEFORE the wake below on purpose. Waking resumes the speaker's last
	// station, so a refusal that arrives after it has started music has already
	// changed the thing it refused to change.
	//
	// A healthy pair is NOT caught by this: a paired speaker's /getZone reports
	// no master and no members, exactly like a standalone one, because a pair is
	// a firmware GROUP (/getGroup), not a zone.
	if inZone := s.zonedPairCandidates(ctx, partner.IP); len(inZone) > 0 {
		s.logger.Warn("stereo: refusing to pair a speaker that is in a multiroom group (the group would stay standing behind the pair)",
			"inZone", strings.Join(inZone, ","), "left", master.DeviceID, "rightIP", partner.IP)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "stereo": true, "inZone": inZone,
			"error": "a speaker that is in a multiroom group cannot become half of a stereo pair; dissolve the group first, then create the pair",
		})
		return
	}

	// And the PERMANENT group that is stored but not live, which the read above
	// cannot see: a group waiting for its master to play answers exactly like a
	// standalone speaker (see storedGroupBlocksPair).
	if blocked, z := s.storedGroupBlocksPair(); blocked {
		s.logger.Warn("stereo: refusing to pair a speaker that holds a saved multiroom group (pairing would overwrite the group document)",
			"storedMaster", z.Master, "storedSlaves", len(z.Slaves), "permanent", z.Permanent,
			"left", master.DeviceID, "rightIP", partner.IP)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "stereo": true, "storedGroup": true, "inZone": []string{s.boxHost},
			"error": "this speaker keeps a saved multiroom group; pairing it would delete that group. Dissolve the group first, then create the pair",
		})
		return
	}

	// What was playing, captured from BOTH speakers BEFORE the firmware is
	// asked. Pairing had none of the wake and resume machinery the zone form
	// has, and issue #705 is the price: the user paired one second after a
	// dissolve had put the master into standby, the firmware synced the fresh
	// pair to the master's power state, and the partner, which was audibly
	// playing its preset, followed it into standby one second after pairing
	// (partner log 2026-08-24 18:50:39 source LOCAL_INTERNET_RADIO to STANDBY,
	// then 18:50:40.485 "re-push: box went to standby, not resuming"). Both
	// speakers silent, reported as "stereo werkt niet" although both
	// /addGroup calls had succeeded.
	//
	// The master's own capture mirrors handleZoneForm. The partner capture is
	// the #705 half: the master was in standby, so only the partner knows what
	// the user was listening to, and after pairing the pair can only be driven
	// through the master (LEFT). The partner's stream URL is loopback on the
	// PARTNER, so it is rewritten to the partner's LAN address the same way the
	// mirror path already lets one box pull another's stream proxy.
	//
	// The busy test is the group path's (masterPlaysUsersMusic), not a bare
	// boxPlayState: a plain "is it playing" read took a master STR had just
	// woken, and that was playing its own power-on resume, for the user's
	// music and restarted it on the pair. The same call holds the Spotify
	// auto-attach off when there is nothing of the user's to carry (#1074:
	// the engine pulled the box onto the album the user had played before
	// standby, 300 ms after the pairing woke it). A master that is genuinely
	// playing is left alone and its music still moves to the pair.
	//
	// Whether STR pulled each half out of standby for this pairing is tracked
	// alongside: a pair formed out of two sleeping speakers with nothing to
	// restart goes back to standby afterwards (afterSilentPair), instead of
	// leaving the master on with no source (#1074, solid amber). The master
	// counts as asleep when the app's quiet wake brought it up moments ago (the
	// group-wake episode is stamped only for a speaker that was in standby) or
	// when it is still in standby at the wake below.
	pairStart := time.Now()
	masterWasAsleep := s.QuietWakeEpisodeActive()
	partnerWasAsleep := partner.IP != "" && woken[strings.TrimSpace(partner.IP)]
	masterRef := s.captureMasterResume()
	var resume *lastPlayInfo
	masterBlocked := false
	quietPair := !s.masterPlaysUsersMusic(ctx, "before pairing", stereoPairAutoAttachHold)
	if !quietPair {
		np := s.readBoxSnapshot(ctx)
		var why string
		resume, masterBlocked, why = masterResumeForZone(np, masterRef)
		s.logger.Info("stereo: what to restart on the pair after pairing",
			"source", np.Source, "location", np.Location, "lastPlayed", lastPlayURL(masterRef),
			"restart", lastPlayURL(resume), "reason", why)
	}
	// A partner the app just woke for this pairing is playing its own power-on
	// resume, not the user's music, so it is not captured either (the rule
	// handleZoneForm applies to members through membersNotWokenForGroup).
	if resume == nil && !masterBlocked && partner.IP != "" && woken[strings.TrimSpace(partner.IP)] {
		s.logger.Info("stereo: not capturing the partner's stream, the app just woke it for this pairing", "partnerIP", partner.IP)
	} else if resume == nil && !masterBlocked && partner.IP != "" {
		pnp := fetchNowPlaying(ctx, partner.IP)
		if pnp.Source == "STANDBY" {
			partnerWasAsleep = true
		}
		if pr := partnerResumeForPair(pnp, partner.IP); pr != nil {
			s.logger.Info("stereo: captured the partner's stream to restart on the pair (the master is not playing)",
				"partnerIP", partner.IP, "url", pr.boxURL, "title", pr.title)
			resume = pr
		}
	}

	// Never pair against a standby master, for the same reason the zone form
	// wakes it (handleZoneForm): in #705 the standby master dragged the whole
	// fresh pair into standby. As there, a failed wake alone is not a reason to
	// stop; only a speaker that answers nothing at all is.
	//
	// And wake it QUIETLY, exactly as handleZoneForm does. This path mirrored
	// that one when it was written, but only the plain wake: the quiet one was
	// added to the sibling later and never copied here. A plain wake lets the
	// firmware resume the master's last source, so pairing two IDLE speakers
	// started music by itself, with whichever preset happened to be last
	// showing as selected (#1074, reported with a log on 2026-09-30: "wake
	// phase: STANDBY, sending sys power" and six seconds later the box was
	// playing LOCAL_INTERNET_RADIO). The resume this function wants was already
	// captured above, so muting and stopping the firmware's own resume here
	// costs nothing: the pair still restarts what the user was listening to.
	if np := s.readBoxSnapshot(ctx); np.Source == "STANDBY" {
		masterWasAsleep = true
		if err := s.quietWake(ctx); err != nil {
			s.logger.Warn("stereo: quiet wake of the sleeping master failed, trying the plain wake", "err", err)
		} else {
			s.logger.Info("stereo: woke the sleeping master quietly so pairing does not start its last source")
		}
	}
	if err := s.ensureBoxReadyErr(ctx); err != nil {
		perr := s.speakerStaysSilent(ctx, c)
		if perr != nil {
			s.logger.Warn("stereo: the speaker is not answering at all, not sending addGroup",
				"wakeErr", err, "probeErr", perr, "left", master.DeviceID)
			http.Error(w, "the speaker is not answering: "+perr.Error(), http.StatusBadGateway)
			return
		}
		s.logger.Info("stereo: the speaker did not report waking, but it is answering, so the pairing goes ahead",
			"wakeErr", err, "left", master.DeviceID)
	}
	// The wake above may have let the firmware resume the master's last
	// source after the quiet wake's own STOP check, exactly as on the group
	// path. With nothing to restart the pair forms silent, so that resume
	// goes. The auto-attach hold is renewed for the /addGroup round trip,
	// which alone can take the firmware's full ~20 s.
	if resume == nil {
		s.stopGroupWakeSelfResume(ctx, "after the wake")
	}
	if quietPair {
		s.holdSpotifyAutoAttach(stereoPairAutoAttachHold, "before addGroup")
	}

	// Persist before driving the firmware so the dissolve path knows it is a
	// stereo pair even after an agent restart. Stereo pairs are firmware-native,
	// so the reconcile loop leaves them alone (the box re-forms across reboots).
	if s.zones != nil {
		z := zones.Zone{
			Master: master.DeviceID, MasterIP: master.IP, Stereo: true, Name: name,
			Slaves: []zones.Member{{DeviceID: partner.DeviceID, IP: partner.IP, Role: partner.Role}},
		}
		if err := s.zones.Set(z); err != nil {
			s.logger.Warn("stereo: persist failed", "err", err)
		}
		s.forgetZoneDocDoubt()
	}

	s.logger.Info("stereo: pairing via /addGroup (beta)", "name", name,
		"left", master.DeviceID, "leftIP", master.IP, "right", partner.DeviceID, "rightIP", partner.IP)
	members := []boxapi.ZoneMember{master, partner}
	// Own budget, detached from the handler's. Pairing is the last step of the
	// form, so by the time it runs, a slow probe earlier in the same request
	// can have spent nearly the whole budget: live on two SoundTouch 10s the
	// partner's /info took its full 6 s, /addGroup started with 4 s left, and
	// the handler deadline killed it mid-call. The firmware went on to form the
	// pair 4 s later and the speakers announced it, but the user had already
	// been told the pairing failed.
	// 30 s, not 20: the firmware runs a pairing op for a fixed ~20 s before it
	// answers /addGroup at all (measured on a failing pair, eight identical
	// attempts, 2026-08-18 — every groupUpdated arrived exactly ~20 s after the
	// POST). Until the boxapi client cap was removed this budget silently died
	// at 6 s anyway ("Client.Timeout exceeded while awaiting headers" in every
	// bundle), so no field run has ever actually waited for the firmware's own
	// verdict; 30 s finally collects it, including the error XML that names the
	// reject reason on a refusing box.
	actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer acancel()
	ctx = actx
	if err := c.AddGroup(ctx, name, master.DeviceID, members); err != nil {
		// 5510 GROUP_ALREADY_EXISTS: a stale pair (half-dissolved, or left over
		// from a pre-shutdown Bose-app pairing) blocks every new /addGroup until
		// someone clears it. The user just asked for a NEW pair with exactly
		// these two speakers, so clear the stale pair on both firmwares and
		// retry once (field: Dirk's ST10 could never re-pair, 2026-07-31).
		// The heal runs on its own detached budget: the handler ctx may have
		// only a couple of seconds left by now, and aborting between the
		// removeGroup and the retry would destroy the old pair without
		// forming the new one.
		if isGroupExistsErr(err) {
			hctx, hcancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			s.healStaleStereoGroups(hctx, c, partner)
			err = c.AddGroup(hctx, name, master.DeviceID, members)
			hcancel()
		}
		// 5580 GROUP_CREATE_GROUP_ON_MARGE_ERROR: the firmware could not
		// register the pair with its marge because its session is stale - the
		// box REPORTS paired while it never re-onboarded with the local marge
		// (its request trail is empty). Field case 2026-08-19: the master's
		// boot-window account re-assert had timed out and every pairing
		// attempt for 20+ minutes answered 5580. Re-log the box in and retry
		// once; the fresh session is what the group create needs.
		if isMargeGroupErr(err) && s.autoPair != nil {
			s.logger.Warn("stereo: the speaker could not register the pair with its marge session (5580), re-logging it in and retrying once", "err", err)
			fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			s.autoPair.ForcePair(fctx)
			// The box re-onboards with marge in the seconds AFTER the POST
			// answers; give that a moment so the retry meets a live session.
			time.Sleep(8 * time.Second)
			err = c.AddGroup(fctx, name, master.DeviceID, members)
			fcancel()
		}
		if err != nil {
			// Before reporting a failure, ASK the speaker. A timed-out or reset
			// /addGroup does not mean the firmware did nothing: it kept going and
			// formed the pair after the call had already been abandoned (live,
			// two SoundTouch 10s, 2026-08-04). Reporting failure for a pair that
			// exists is worse than the timeout itself, because the user's next
			// move is to pair again, which the firmware then rejects with
			// GROUP_ALREADY_EXISTS. One immediate read is not enough: the
			// firmware's pairing op takes ~20 s, so a read 40 ms after a lost
			// reply always saw "no group" — poll for a while instead.
			var g boxapi.Group
			var gerr error
			for vDeadline := time.Now().Add(12 * time.Second); ; {
				cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Second)
				g, gerr = c.GetGroup(cctx)
				ccancel()
				if (gerr == nil && len(g.Members) == 2) || time.Now().After(vDeadline) {
					break
				}
				time.Sleep(2 * time.Second)
			}
			if gerr == nil && len(g.Members) == 2 {
				s.logger.Warn("stereo: addGroup reported an error but the speaker formed the pair anyway, treating it as paired",
					"err", err, "id", g.ID)
			} else {
				s.logger.Warn("stereo: addGroup failed (only the ST10 supports stereo pairs)", "err", err)
				s.dropStereoDocAfterFailure("addGroup refused")
				http.Error(w, "addGroup: "+err.Error(), http.StatusBadGateway)
				return
			}
		}
	}
	g, err := c.GetGroup(ctx)
	if err != nil {
		// Paired, but the read-back failed (slow box, expiring ctx). The
		// canonical document depends only on data known BEFORE the read-back,
		// so still install and relay it — skipping it here left the partner's
		// marge on its self-centered record exactly on the slowest boxes.
		s.logger.Warn("stereo: paired but getGroup read-back failed", "err", err)
		canonicalDoc := marge.CanonicalGroupXML(name, master.DeviceID, master.IP, partner.DeviceID, partner.IP)
		if s.margeGroupSet != nil {
			if serr := s.margeGroupSet(canonicalDoc); serr != nil {
				s.logger.Warn("stereo: could not install the canonical pair document on the local marge", "err", serr)
			}
		}
		pctx, pcancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		partnerSynced := s.pushGroupDocToPartner(pctx, partner.IP, canonicalDoc)
		pcancel()
		// Same restart as on the confirmed path below: a failed read-back
		// still very likely paired (that is why this path answers ok).
		if resume != nil {
			go s.resumeAfterZoneForm(zoneResume{push: *resume, ref: masterRef, survivorReachesMembers: true})
		} else {
			s.scheduleLateSelfResumeCheck("after pairing")
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "stereo": true,
			"canonicalGroup": canonicalDoc, "partnerIP": partner.IP,
			"partnerMargeSynced": partnerSynced,
		})
		return
	}
	// Assert the firmware actually bound BOTH channels. /addGroup can return 200
	// while silently dropping a member (wrong deviceID, account mismatch), which
	// the old code reported as ok=true — the user thought it worked but only one
	// speaker played. A real stereo pair must read back exactly two members.
	if len(g.Members) != 2 {
		s.logger.Warn("stereo: firmware formed an INCOMPLETE pair (a speaker was dropped)", "id", g.ID, "members", len(g.Members))
		s.dropStereoDocAfterFailure("the firmware bound only one channel")
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "stereo": true, "members": len(g.Members),
			"error": "the speaker did not accept the pair. Both speakers must be set up and on the same account, and only the SoundTouch 10 supports stereo pairs.",
		})
		return
	}
	s.logger.Info("stereo: paired", "id", g.ID, "members", len(g.Members))
	// And once more for the two-sided verify below, which can take its own
	// budget after a slow /addGroup has used up most of the previous hold.
	if quietPair {
		s.holdSpotifyAutoAttach(stereoPairAutoAttachHold, "verifying the pair")
	}

	// Install ONE canonical pair document on both members' marges. Left alone,
	// each firmware re-creates the record on its own marge from its own point
	// of view and the RIGHT box stores ITSELF as master/LEFT (field: Rolf's
	// pair, GroupService.xml id="str-grp-<rightID>"), which desyncs the pair
	// after standby and blocks re-pairing. The direct push to the partner's
	// agent fails between series-I boxes (their firewall drops agent-to-agent
	// HTTP), so the response also carries the document for the desktop app to
	// relay; partnerMargeSynced tells it whether the relay is still needed.
	canonicalDoc := marge.CanonicalGroupXML(name, master.DeviceID, master.IP, partner.DeviceID, partner.IP)
	if s.margeGroupSet != nil {
		if err := s.margeGroupSet(canonicalDoc); err != nil {
			s.logger.Warn("stereo: could not install the canonical pair document on the local marge", "err", err)
		}
	}
	partnerSynced := s.pushGroupDocToPartner(ctx, partner.IP, canonicalDoc)
	// Both halves must hold the pair before it is called formed (see
	// stereoverify.go): a master that read back two members while its partner
	// stored nothing was reported as paired and then refused to play alone.
	vctx, vcancel := context.WithTimeout(context.WithoutCancel(ctx), stereoVerifyTimingFor(s).budget+10*time.Second)
	verdict := verifyStereoPair(vctx, s.stereoGroupFetch(), s.boxHost, partner.IP, master.DeviceID, partner.DeviceID, stereoVerifyTimingFor(s))
	vcancel()
	if !verdict.OK {
		s.logger.Warn("stereo: the pair did not form on both speakers, undoing it",
			"reason", verdict.Reason, "masterStatus", verdict.MasterStatus, "partnerIP", partner.IP)
		cleared := s.undoUnconfirmedPair(ctx, c, partner)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "stereo": true, "reason": verdict.Reason,
			"masterStatus": verdict.MasterStatus, "partnerIP": partner.IP,
			"partnerMargeCleared": cleared,
			"error":               stereoFailureText(verdict.Reason),
		})
		return
	}
	if !verdict.PartnerVerified {
		s.logger.Info("stereo: paired; the partner's firmware could not be read back, the master reports the pair healthy",
			"partnerIP", partner.IP, "masterStatus", verdict.MasterStatus)
	}
	// Restart what was playing before the pairing (#705). A pair is one logical
	// device to the firmware, so a master stream that SURVIVED the pairing
	// already serves both channels (in the #705 bundle the partner flipped to
	// GROUP_SLAVE while the master kept its stream); resumeAfterZoneForm
	// therefore keeps its survived-stream skip here and only pushes when the
	// pair sits silent, which is exactly the #705 failure.
	if resume != nil {
		go s.resumeAfterZoneForm(zoneResume{push: *resume, ref: masterRef, survivorReachesMembers: true})
	} else {
		// A pair formed silent gets the same one late look as a silent group:
		// the firmware's resume can start after the pair formed. And a pair
		// STR pulled out of standby on both halves goes back to standby after
		// that look, or the master stays on with no source (#1074). Only here,
		// on the verified path: the unread path above cannot tell whether the
		// pair exists, and standing down a lone master there is a guess.
		s.afterSilentPair(masterWasAsleep && partnerWasAsleep, pairStart)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "stereo": true, "id": g.ID, "name": g.Name, "members": g.Members,
		"canonicalGroup": canonicalDoc, "partnerIP": partner.IP,
		"partnerMargeSynced": partnerSynced,
	})
}

// isGroupExistsErr reports whether an /addGroup error is the firmware's 5510
// GROUP_ALREADY_EXISTS rejection.
//
// The typed envelope is consulted first. The old substring match ran over the
// WHOLE error string, which embeds the reply body, and the body's errors
// element carries the box's raw 12-hex MAC as deviceID: a MAC containing the
// digit run 5510 satisfied the check on EVERY failed /addGroup of that box,
// forever, and this helper's hit triggers healStaleStereoGroups, which
// dissolves pairs on BOTH speakers. About one box in seven thousand per code,
// invisible until it is somebody's living room. The substring fallback stays
// only for the untyped path (a bare error body parses to Value ""), where the
// name match cannot collide with a MAC and the numeric match is the risk we
// have always carried there.
func isGroupExistsErr(err error) bool {
	return boxErrCodeIs(err, "5510", "GROUP_ALREADY_EXISTS")
}

// isMargeGroupErr reports whether an /addGroup error is the firmware's 5580
// GROUP_CREATE_GROUP_ON_MARGE_ERROR: the box could not register the group
// with its marge, which on an STR box means its marge session is stale.
// Same typed-first contract as isGroupExistsErr, and the same MAC trap: this
// helper's hit arms the ForcePair retry path.
func isMargeGroupErr(err error) bool {
	return boxErrCodeIs(err, "5580", "GROUP_CREATE_GROUP_ON_MARGE_ERROR")
}

// boxErrCodeIs is the shared decision behind the two helpers above: the typed
// envelope decides when the reply carried one, the substring fallback only
// covers untyped errors (transport failures, bare error bodies), where a MAC
// cannot leak into the match via the envelope's deviceID.
func boxErrCodeIs(err error, value, name string) bool {
	if err == nil {
		return false
	}
	var be *boxapi.BoxError
	if errors.As(err, &be) && be.Value != "" {
		return be.Value == value || strings.EqualFold(be.Name, name)
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, value) || strings.Contains(msg, name)
}

// partnerResumeForPair derives, from the PARTNER speaker's live now_playing,
// the stream the freshly formed pair should play when the master itself has
// nothing to resume. This is the #705 half of the pairing resume: the master
// sat in standby after a dissolve while the partner was audibly playing its
// preset, and after pairing the pair can only be driven through the master
// (LEFT), so what the partner was playing must be captured before /addGroup
// takes it down. Returns nil when the partner is not audibly playing or its
// selection carries no URL this box could push (Bluetooth, AUX, native
// Spotify); forming the pair without a resume is still better than refusing.
func partnerResumeForPair(np nowPlayingSnapshot, partnerIP string) *lastPlayInfo {
	if partnerIP == "" || !isPlayingStatus(np.PlayStatus) {
		return nil
	}
	stream, title, art := "", np.ItemName, ""
	if su, name, img, ok := decodeOrionStationLocation(np.Location); ok {
		// The native radio shape, the one in the #705 bundle: the location
		// wraps the stream URL, station name and artwork in base64 JSON.
		stream, art = su, img
		if name != "" {
			title = name
		}
	} else if strings.HasPrefix(np.Location, "http://") || strings.HasPrefix(np.Location, "https://") {
		// A UPnP push carries the stream URL directly in the location.
		stream = np.Location
	}
	stream = lanURLForPeer(stream, partnerIP)
	if stream == "" {
		return nil
	}
	return &lastPlayInfo{boxURL: stream, title: title, art: lanURLForPeer(art, partnerIP), ts: time.Now()}
}

// decodeOrionStationLocation unpacks the ContentItem location a native
// LOCAL_INTERNET_RADIO selection carries ("/station?data=<base64 JSON>", the
// shape OrionStationLocation writes) into the stream URL, station name and
// artwork it encodes. ok is false for any other location shape.
func decodeOrionStationLocation(loc string) (streamURL, name, imageURL string, ok bool) {
	const p = "/station?data="
	i := strings.Index(loc, p)
	if i < 0 {
		return "", "", "", false
	}
	raw := strings.TrimSpace(loc[i+len(p):])
	// STR writes the unpadded URL safe alphabet, but older builds used others,
	// so accept every alphabet exactly as handleOrionStation does.
	var payload []byte
	for _, dec := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding, base64.StdEncoding, base64.RawStdEncoding,
	} {
		if b, err := dec.DecodeString(raw); err == nil {
			payload = b
			break
		}
	}
	if payload == nil {
		return "", "", "", false
	}
	var st struct {
		StreamURL string `json:"streamUrl"`
		Name      string `json:"name"`
		ImageURL  string `json:"imageUrl"`
	}
	if err := json.Unmarshal(payload, &st); err != nil || st.StreamURL == "" {
		return "", "", "", false
	}
	return st.StreamURL, st.Name, st.ImageURL, true
}

// lanURLForPeer rewrites a URL that addresses the PEER speaker's own loopback
// (its agent serves streams and artwork on http://127.0.0.1:8888) into one
// this box can fetch across the LAN: the peer's IP on the :17008 redirect,
// the same route mirror slaves already use to pull a master's stream proxy
// (see mirrorStreamPort). A URL that is already externally reachable is
// returned unchanged; anything unfetchable yields "".
func lanURLForPeer(raw, peerIP string) string {
	if raw == "" || peerIP == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if (ip != nil && ip.IsLoopback()) || strings.EqualFold(host, "localhost") {
		u.Host = net.JoinHostPort(peerIP, mirrorStreamPort)
		return u.String()
	}
	return raw
}
