// Forming a group, the native half: what to restart afterwards, the wake of
// the master, the /setZone drive, the verdict and the read-back, and the
// helpers around the leader (its own zone, whether it answers at all, and
// putting the previous group back after a failed form). Split out of
// zones_stereo.go.

package webui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// zoneFormPrepareResume captures what to restart on the master once the
// native zone is formed, because /setZone tears the playing session down.
func (s *Server) zoneFormPrepareResume(run *zoneFormRun) {
	ctx, req, slaves := run.ctx, run.req, run.slaves
	// Native: drive the firmware zone and read back what it actually formed.
	//
	// /setZone tears down the master's in-flight UPnP session (#70): the
	// firmware cannot adopt an externally pushed session into a fresh zone, so
	// forming a group while music plays deselects the source (INVALID_SOURCE,
	// errorUpdate 1036 UpnpRcvdContentItemInWrongState) and the room goes
	// silent even though the zone reports formed, with "Select a preset..." on
	// the display. Capture whether STR's stream was playing BEFORE the form and
	// re-push it to the now-grouped master afterwards; the master distributes
	// it to the followers (verified live: a play pushed to the master after
	// forming reaches every member).
	//
	// masterRef is this box's OWN entry and stays the staleness reference even
	// when the stream to push comes from somewhere else. See captureMasterResume.
	masterRef := s.captureMasterResume()
	var resume *lastPlayInfo
	masterBlocked := false
	// A master STR woke for this group is playing its own power-on resume, not
	// the user's music: stop it instead of carrying it into every room. A
	// master with nothing of the user's to carry also holds the Spotify
	// auto-attach off for the whole form, or the engine can fill the fresh
	// group with a track nobody started (#1074).
	busy := s.masterPlaysUsersMusic(ctx, "before forming", zoneFormBudget(len(slaves))+lateSelfResumeHold)
	if busy {
		// WHAT the box is playing, not just that it is: a Spotify session runs
		// on a URL STR never recorded, and re-pushing the recorded one replaced
		// the user's live playlist with an old station in every room. See
		// masterResumeForZone.
		np := s.readBoxSnapshot(ctx)
		var why string
		resume, masterBlocked, why = masterResumeForZone(np, masterRef)
		s.logger.Info("zone: what to restart on the master after forming",
			"source", np.Source, "location", np.Location, "lastPlayed", lastPlayURL(masterRef),
			"restart", lastPlayURL(resume), "reason", why)
	}
	// The master may have nothing while a MEMBER is playing: forming the group
	// then took that member's station down and left the whole group silent
	// (#954). See memberResumeForZone. Not when the master is audibly on a
	// source STR cannot push: it is still playing, and moving the whole group
	// onto a member's station would be the same theft from the other side.
	if resume == nil && !masterBlocked {
		resume = s.memberResumeForZone(ctx, membersNotWokenForGroup(slaves, wokenMemberSet(req.WokenFromStandby)))
	}
	run.masterRef, run.resume, run.masterBlocked = masterRef, resume, masterBlocked
}

// zoneFormWakeMaster wakes a sleeping master quietly and refuses, with the
// previous group restored, when the speaker does not answer at all.
func (s *Server) zoneFormWakeMaster(run *zoneFormRun) *zoneFormAnswer {
	ctx, c, master, resume := run.ctx, run.c, run.master, run.resume
	// Never form against a standby master: the firmware then wakes INTO its
	// stale UPnP item, throws the 1036 wrong-state error and self-dissolves
	// the fresh zone ~300ms after reporting ok (#70, observed live).
	//
	// When the wake fails, stop here. Driving /setZone into a speaker that just
	// refused to answer buys nothing: measured on an eleven-speaker fleet
	// 2026-08-16, the wake failed at 8 s, /setZone was sent anyway and died of
	// the same silence 25 s after the user pressed the button. The user waits
	// half a minute for an error the first eight seconds already knew about,
	// and by then the group that WAS working is gone.
	// A failed wake alone is NOT a reason to stop. A speaker can be slow out of
	// standby and still form the group perfectly well once /setZone reaches it,
	// and refusing there would break grouping from standby, which works today.
	// The condition worth stopping on is the speaker not answering AT ALL,
	// which is what the field log showed: two reads of /now_playing timed out,
	// the wake had no source to report, and everything after that was doomed.
	// So the wake failing is only the prompt to ask one cheap question.
	// A master asleep at form time is woken QUIETLY: the plain wake let the
	// firmware resume the master's last station and the fresh zone carried
	// it into every room, so "create a group" meant "start playing" (Jens,
	// 2026-09-06, after #805 had fixed the same for permanent groups on the
	// app side). The quiet wake mutes, stops the resumed station and gives
	// the level back on the join; the group then forms silent and plays
	// what the user starts next.
	if np := s.readBoxSnapshot(ctx); np.Source == "STANDBY" {
		if err := s.quietWake(ctx); err != nil {
			s.logger.Warn("zone: quiet wake of the sleeping master failed, trying the plain wake", "err", err)
		} else {
			s.logger.Info("zone: woke the sleeping master quietly so forming the group does not start its last station")
		}
	}
	if err := s.ensureBoxReadyErr(ctx); err != nil {
		perr := s.speakerStaysSilent(ctx, c)
		if perr != nil {
			s.logger.Warn("zone: the speaker leading the group is not answering at all, not sending setZone",
				"wakeErr", err, "probeErr", perr, "master", master.DeviceID, "prevMembers", len(run.prevLive.Members))
			s.restorePreviousZone(ctx, c, master, run.prevDoc, run.hadPrevDoc, run.prevLive)
			return zoneFormRefusal(http.StatusBadGateway, "the speaker leading the group is not answering: "+perr.Error())
		}
		s.logger.Info("zone: the speaker did not report waking, but it is answering, so the group is formed anyway",
			"wakeErr", err, "master", master.DeviceID)
	}
	// The wake above (or the app's, moments ago) may have let the firmware
	// resume the master's last source after the quiet wake's own STOP check.
	// With nothing to restart the group forms silent, so that resume goes.
	if resume == nil {
		s.stopGroupWakeSelfResume(ctx, "after the wake")
	}
	return nil
}

// zoneFormDriveNative drives the firmware zone, as an incremental join over
// a live zone led by this master or as a full /setZone, reads it back, asks
// each follower, and re-forms once when no added member confirmed. A
// /setZone the firmware refused restores the previous group and refuses.
func (s *Server) zoneFormDriveNative(run *zoneFormRun) *zoneFormAnswer {
	ctx, c, master, slaves := run.ctx, run.c, run.master, run.slaves
	// Read the live zone ONCE: it carries both the members the user dropped
	// (which /setZone alone never removes - "briefly leaves then comes back",
	// Albrecht, 7-box fleet, 2026-07-14) and the decision between the
	// incremental join path and a full re-form. Matching is IP-or-deviceID,
	// with IP as the chassis-stable key: a two-chip box (Portable, ST20 BCO)
	// announces its wlan0 MAC over discovery, which is NOT the SCM deviceID
	// the firmware lists for it, so a deviceID-only match would wrongly treat
	// a live member as new (or keep a dropped one).
	live, liveErr := c.GetZone(ctx)
	zoneExists := liveErr == nil && live.Master != "" && len(live.Members) > 0 &&
		strings.EqualFold(strings.TrimSpace(live.Master), strings.TrimSpace(master.DeviceID))
	toAdd, toRemove := zoneDiff(live, slaves)
	if liveErr == nil && live.Master != "" && len(live.Members) > 0 && len(toRemove) > 0 {
		s.logger.Info("zone: dropping members no longer in the group", "count", len(toRemove), "master", master.DeviceID)
		if err := c.RemoveZoneSlave(ctx, master, toRemove); err != nil {
			s.logger.Warn("zone: removeZoneSlave failed", "err", err)
		}
	}

	// When this master already leads a live zone, join new members with
	// /addZoneSlave instead of re-forming the whole zone: the firmware keeps
	// the master's source running through an incremental join (the original
	// Bose app added members this way, without interrupting the music), while
	// a full /setZone re-form ended in the stream restart below on every tap.
	// Any error falls back to the proven full re-form, so the worst case is
	// exactly the old behavior.
	usedIncremental := false
	if zoneExists {
		switch {
		case len(toAdd) == 0:
			s.logger.Info("zone: requested group already live, nothing to drive", "master", master.DeviceID, "members", len(live.Members)-len(toRemove))
			usedIncremental = true
		default:
			if err := c.AddZoneSlave(ctx, master, toAdd); err != nil {
				s.logger.Warn("zone: addZoneSlave failed, falling back to a full setZone", "err", err, "adding", len(toAdd))
			} else {
				s.logger.Info("zone: added members to the live group without re-forming", "adding", len(toAdd), "master", master.DeviceID)
				usedIncremental = true
			}
		}
	}
	readBackFailed := false
	if !usedIncremental {
		if err := c.SetZone(ctx, master, slaves); err != nil {
			s.logger.Warn("zone: setZone failed", "err", err, "master", master.DeviceID)
			s.restorePreviousZone(ctx, c, master, run.prevDoc, run.hadPrevDoc, run.prevLive)
			return zoneFormRefusal(http.StatusBadGateway, "setZone: "+err.Error())
		}
	}
	z2, err := c.GetZone(ctx)
	if err != nil {
		// A failed read-back says nothing about whether the group formed, and
		// returning here skipped the two things that make it audible: the
		// follower verification and, worse, the post-form resume. The group was
		// then live with nobody pushing the stream to it, and the app was told
		// everything was fine. The firmware's own zone read is exactly what
		// hangs on some chassis, so this is not a rare path.
		//
		// Carry on with what was ASKED for instead. The members the caller
		// requested are the right stand-in: everyMemberCarriesTheGroup is
		// pessimistic, so a member that turns out not to be carrying the stream
		// gets a push, which is the safe direction. The answer says the
		// read-back did not happen rather than claiming a verified group.
		s.logger.Warn("zone: formed but getZone read-back failed, continuing with the requested members",
			"err", err, "master", master.DeviceID, "requestedSlaves", len(slaves))
		z2 = boxapi.Zone{Master: master.DeviceID, Members: slaves}
		readBackFailed = true
	}
	// The master's optimistic member list is not proof a slave joined (#70): the
	// firmware lists a member it announced to before the slave's own zone reflects
	// enrolment, so a 3-box group reported success while one box silently never
	// joined. The authoritative "missing" set therefore comes from each FOLLOWER's
	// own /getZone (verifyFollowersJoined), polled with a short retry because a
	// slave's self-report lags forming by ~100ms to several seconds. The master's
	// read-back is kept only as supplementary diagnostics (masterMissing).
	fetchFollower := func(fctx context.Context, ip string) (boxapi.Zone, error) {
		return boxapi.New(ip).GetZone(fctx)
	}
	// On the incremental path only the members that were actually ADDED need
	// the follower poll: the pre-existing ones are in the live zone already,
	// and polling them again burned the form budget for nothing (the very
	// bug the twelve-speaker fleet hit on 2026-08-09).
	verifyTargets := slaves
	if usedIncremental {
		verifyTargets = toAdd
	}
	missing, unverifiable := []string{}, []string{}
	if len(verifyTargets) > 0 {
		missing, unverifiable = verifyFollowersJoined(ctx, s.logger, z2.Master, verifyTargets, fetchFollower)
	}
	// Whether the change HELD as an incremental join over the live zone. Starts
	// from usedIncremental and is withdrawn by the fallback below: after the
	// full re-form the members were rebuilt by /setZone, so the resume decision
	// must treat it like a fresh form, not like a join that kept the stream
	// flowing to everyone.
	heldIncremental := usedIncremental
	// Incremental join where NOT ONE added member confirmed: distrust
	// /addZoneSlave on this firmware and run the proven full re-form once.
	if usedIncremental && len(toAdd) > 0 && len(missing) == len(toAdd) {
		heldIncremental = false
		s.logger.Warn("zone: no added member confirmed the incremental join, re-forming the whole zone once", "adding", len(toAdd))
		if err := c.SetZone(ctx, master, slaves); err != nil {
			s.logger.Warn("zone: fallback setZone failed", "err", err, "master", master.DeviceID)
			s.restorePreviousZone(ctx, c, master, run.prevDoc, run.hadPrevDoc, run.prevLive)
			return zoneFormRefusal(http.StatusBadGateway, "setZone: "+err.Error())
		}
		if z2, err = c.GetZone(ctx); err != nil {
			// Same as the first read-back: carry on with what was asked for
			// rather than returning past the resume and leaving the group
			// silent while reporting success.
			s.logger.Warn("zone: re-formed but getZone read-back failed, continuing with the requested members",
				"err", err, "master", master.DeviceID, "requestedSlaves", len(slaves))
			z2 = boxapi.Zone{Master: master.DeviceID, Members: slaves}
			readBackFailed = true
		}
		missing, unverifiable = verifyFollowersJoined(ctx, s.logger, z2.Master, slaves, fetchFollower)
	}
	run.toAdd, run.toRemove = toAdd, toRemove
	run.usedIncremental, run.heldIncremental, run.readBackFailed = usedIncremental, heldIncremental, readBackFailed
	run.z2, run.missing, run.unverifiable = z2, missing, unverifiable
	return nil
}

// zoneFormed is the verdict of a native drive, computed from the master's
// read-back and the followers' own answers. Pure, so it can be tested
// without a speaker; answer renders it as the form's final 200 body.
type zoneFormed struct {
	Live      boxapi.Zone
	Requested int
	// Verified counts the requested members that confirmed; pre-existing
	// members of an incremental join count as verified, since only the added
	// ones were polled and "missing" can only name those.
	Verified      int
	Missing       []string
	Unverifiable  []string
	MasterMissing []string
	// MasterFormed is false when the master's own read-back shows no zone at
	// all after /setZone: the firmware never actually formed one.
	MasterFormed   bool
	ReadBackFailed bool
}

// zoneVerdict compares what the master reads back with what was asked for.
func zoneVerdict(z2 boxapi.Zone, slaves []boxapi.ZoneMember, missing, unverifiable []string, readBackFailed bool) zoneFormed {
	masterLive := make(map[string]bool, len(z2.Members))
	for _, m := range z2.Members {
		masterLive[strings.ToLower(m.DeviceID)] = true
	}
	masterMissing := make([]string, 0)
	for _, sl := range slaves {
		if !masterLive[strings.ToLower(sl.DeviceID)] {
			masterMissing = append(masterMissing, sl.DeviceID)
		}
	}
	return zoneFormed{
		Live: z2, Requested: len(slaves), Verified: len(slaves) - len(missing),
		Missing: missing, Unverifiable: unverifiable, MasterMissing: masterMissing,
		MasterFormed:   len(z2.Members) > 0 && z2.Master != "",
		ReadBackFailed: readBackFailed,
	}
}

// ok is the form's verdict for the caller: at least one member confirmed.
func (f zoneFormed) ok() bool { return f.Verified > 0 }

// answer is the final 200 body of a native form. ok:false carries no error
// field; the desktop app and the phone remote read ok, missing and
// unverifiable, and that shape is pinned.
func (f zoneFormed) answer() *zoneFormAnswer {
	out := map[string]any{
		"ok": f.ok(), "mode": "native", "master": f.Live.Master, "senderIP": f.Live.SenderIP,
		"members": f.Live.Members, "requested": f.Requested,
		"verified": f.Verified, "missing": f.Missing, "unverifiable": f.Unverifiable,
		"masterMissing": f.MasterMissing,
	}
	if f.ReadBackFailed {
		// Say it plainly: the group was formed and the stream was pushed, but
		// the speaker never confirmed its own zone, so none of the numbers
		// above were read back from it.
		out["readBack"] = "failed"
	}
	return zoneFormOK(out)
}

// zoneFormVerify turns the drive's read-back into the verdict, gives the
// joining members the group's volume, restarts the music, and answers.
func (s *Server) zoneFormVerify(run *zoneFormRun) *zoneFormAnswer {
	slaves, z2, missing, unverifiable := run.slaves, run.z2, run.missing, run.unverifiable
	resume, masterRef, newlyJoined, mySeq := run.resume, run.masterRef, run.newlyJoined, run.mySeq
	f := zoneVerdict(z2, slaves, missing, unverifiable, run.readBackFailed)
	// Regression guard (#70 / Albrecht 0.8.x): if the master's own read-back shows
	// no members and no master after SetZone, the firmware never actually formed a
	// zone (it worked in 0.7.29, broke in 0.8.0x). Report that honestly as ok=false
	// so the app stops claiming success when nothing joined, instead of leaning on
	// the optimistic "ok=true" the old code always returned.
	if !f.MasterFormed {
		s.logger.Warn("zone: master read-back empty after setZone (slaves did not join — possible 0.8.x regression)",
			"liveMaster", z2.Master, "liveMembers", len(z2.Members), "requestedSlaves", len(slaves))
	}
	s.logger.Info("zone: formed", "mode", "native", "ok", f.ok(), "liveMaster", z2.Master,
		"requestedSlaves", len(slaves), "liveMembers", len(z2.Members),
		"masterMissing", strings.Join(f.MasterMissing, ","),
		"verified", f.Verified, "missing", strings.Join(missing, ","),
		"unverifiable", strings.Join(unverifiable, ","))
	// Verify-first. A member that never confirmed the join (missing) must not be
	// given the group's level: that is a speaker in another room, in no group at
	// all, jumping to the living room's volume. Same for a zone the firmware
	// never actually formed.
	if f.MasterFormed && f.Verified > 0 {
		if joining := dropMembers(newlyJoined, missing); len(joining) > 0 {
			go s.matchNewMembersToMasterVolume(joining, mySeq)
		}
	}
	if resume != nil && f.MasterFormed {
		// The staleness reference is the MASTER's own entry, never the stream
		// being pushed: a member-derived takeover (#954) compared against the
		// master's live entry always mismatches. A real user play on the master
		// between capture and push still moves s.lastPlay away from masterRef
		// and still cancels the push, which is what the check is for. Only a
		// change that held as an incremental join may skip the push when the
		// master's stream survived; on a fresh (or re-formed) zone the members
		// have nothing yet (Martin, 2026-08-24).
		go s.resumeAfterZoneForm(zoneResume{push: *resume, ref: masterRef, survivorReachesMembers: run.heldIncremental, members: z2.Members})
	} else if resume == nil {
		// A group formed silent out of a group wake gets one more look: the
		// firmware's resume can start after the zone formed, and the zone then
		// carries it into every room. One read, no repeating timer.
		//
		// Unlike a stereo pair (afterSilentPair), a group formed out of
		// standby is NOT put back to standby: a native zone does not survive
		// its master dropping to STANDBY (measured 2026-08-18, see boxcli), so
		// that would dissolve the group the user just created.
		s.scheduleLateSelfResumeCheck("after forming")
	}
	return f.answer()
}

// leaderZone returns the full member list of the group described by z, when z
// is a FOLLOWER's view of it.
//
// A follower's /getZone names the master and carries its address in
// senderIPAddress, but lists only the follower itself as a member. Anything
// reading that alone sees a one-speaker group, which is what made the desktop
// app show a different set of speakers depending on which one it asked.
//
// Returns false when this speaker leads the group, when it is in none, or when
// the leader cannot be reached: in all three the caller keeps the speaker's own
// answer, which is either already right or the best that is available.
func (s *Server) leaderZone(ctx context.Context, z boxapi.Zone) ([]boxapi.ZoneMember, bool) {
	master := strings.TrimSpace(z.Master)
	senderIP := strings.TrimSpace(z.SenderIP)
	if master == "" || senderIP == "" {
		return nil, false
	}
	ownID := s.fetchDeviceID(ctx, s.boxHost)
	if ownID == "" || strings.EqualFold(ownID, master) {
		return nil, false // we lead it, or cannot tell who we are
	}
	lz, err := s.fetchZone(ctx, senderIP)
	if err != nil || len(lz.Members) == 0 {
		return nil, false
	}
	// Both speakers have to name the same leader. senderIPAddress is whoever
	// sent the last zone message, and after a group is handed over or torn down
	// and rebuilt, that speaker is in a DIFFERENT group than the one this
	// speaker still believes it is in. Adopting that list puts strangers on the
	// page. liveGroupView has refused this since yesterday; this path answers
	// the same question for the phone and did not, so the two could draw two
	// different groups from one speaker.
	if !strings.EqualFold(strings.TrimSpace(lz.Master), master) {
		return nil, false
	}
	// The firmware promises nothing about repeats, and a speaker listed twice
	// becomes two rows and two volume calls from one press of the group slider.
	// The leader seeds both sets, because a leader that does list itself would
	// otherwise contradict its own first row about who leads.
	seenIP := map[string]bool{senderIP: true}
	seenID := map[string]bool{strings.ToUpper(master): true}
	out := make([]boxapi.ZoneMember, 0, len(lz.Members))
	var selfSeen bool
	for _, m := range lz.Members {
		ip := strings.TrimSpace(m.IP)
		id := strings.ToUpper(strings.TrimSpace(m.DeviceID))
		if (ip != "" && seenIP[ip]) || (id != "" && seenID[id]) {
			continue
		}
		if ip != "" {
			seenIP[ip] = true
		}
		if id != "" {
			seenID[id] = true
		}
		if strings.EqualFold(id, ownID) || ip == s.boxHost {
			selfSeen = true
		}
		out = append(out, m)
	}
	// A leader that does not list us is not proof we left: our own firmware is
	// what named that leader. Dropping the list here would collapse a five
	// speaker group to nothing on the phone whenever a leader answers with an
	// empty identifier for one member, which the two chip chassis have done.
	if !selfSeen {
		out = append(out, boxapi.ZoneMember{DeviceID: ownID, IP: s.boxHost})
		s.logger.Info("zone: the leader did not list this speaker, adding it from its own report",
			"master", master, "leaderIP", senderIP, "members", len(out))
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// speakerStaysSilent reports whether the speaker really has stopped answering,
// as opposed to being slow for a moment. It returns the last error when every
// attempt failed, and nil as soon as one succeeds.
//
// The first version of this asked ONCE with a two second budget, and that was
// wrong in a way that reached users: a speaker waking up, or busy starting a
// stream, misses two seconds easily, and the group was then refused with "the
// speaker leading the group is not answering" while it was answering fine a
// moment later. Reported the day it shipped, by the same person whose log the
// guard was built from.
//
// The evidence it was built for looked nothing like two seconds: a speaker
// that answered nothing at all for twenty five seconds, across several reads,
// while the firmware tore the group down around it. So the question has to be
// asked the way that fault presents itself, over a span rather than once.
func (s *Server) speakerStaysSilent(ctx context.Context, c *boxapi.Client) error {
	return s.staysSilentVia(ctx, func(pctx context.Context) error {
		_, err := c.GetInfo(pctx)
		return err
	}, 3, time.Second)
}

// staysSilentVia is speakerStaysSilent with the read and the timings passed in,
// so the decision can be tested without a speaker and without waiting.
func (s *Server) staysSilentVia(ctx context.Context, probe func(context.Context) error, attempts int, gap time.Duration) error {
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 && gap > 0 {
			select {
			case <-ctx.Done():
				return last
			case <-time.After(gap):
			}
		}
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := probe(pctx)
		cancel()
		if err == nil {
			if i > 0 {
				s.logger.Info("zone: the speaker was slow to answer, not silent, continuing",
					"attempt", i+1)
			}
			return nil
		}
		last = err
	}
	return last
}

// restorePreviousZone puts back what the user had before a form that failed.
//
// The case it exists for, from an eleven-speaker fleet on 2026-08-16: a pair
// was playing, a third speaker was added, the master's own :8090 stopped
// answering during the drive, and the firmware dissolved the pair. The form
// returned an error, which is honest, but the user was left with silence in two
// rooms and a stored document describing a three-speaker group that had never
// existed. Adding a speaker has to be able to fail without costing the group
// that was already working.
//
// Two separate repairs, because they can fail independently:
//
//   - The stored document goes back to what it was. Leaving the new one in
//     place would have the reconcile loop, whenever a user turns it on, keep
//     driving toward a group the firmware refused.
//   - If this speaker led a live group before the attempt and no longer leads
//     one, form that old group again.
//
// Nothing here is retried in a loop. If the speaker is genuinely wedged, one
// attempt says so in the log and the user is told to try again; hammering a box
// that stopped answering is what turns one bad minute into several.
func (s *Server) restorePreviousZone(ctx context.Context, c *boxapi.Client, master boxapi.ZoneMember,
	prevDoc zones.Zone, hadPrevDoc bool, prevLive boxapi.Zone) {
	s.restorePreviousZoneVia(ctx, c.GetZone, c.SetZone, master, prevDoc, hadPrevDoc, prevLive)
}

// restorePreviousZoneVia is the body of restorePreviousZone with the two
// firmware calls passed in, so the decision can be tested without a speaker.
// The client hard-codes port 8090, which a test listener cannot claim.
func (s *Server) restorePreviousZoneVia(ctx context.Context,
	getZone func(context.Context) (boxapi.Zone, error),
	setZone func(context.Context, boxapi.ZoneMember, []boxapi.ZoneMember) error,
	master boxapi.ZoneMember, prevDoc zones.Zone, hadPrevDoc bool, prevLive boxapi.Zone) {
	if s.zones != nil {
		var err error
		if hadPrevDoc {
			err = s.zones.Set(prevDoc)
		} else {
			err = s.zones.Clear()
		}
		if err != nil {
			s.logger.Warn("zone: could not put the previous group document back", "err", err)
		}
		s.forgetZoneDocDoubt()
	}

	if len(prevLive.Members) == 0 {
		return // there was no live group to lose
	}
	// Only the speaker that led the old group can rebuild it. A follower asking
	// for this would be forming a second group around itself, which is a
	// different group than the one the user lost.
	if !strings.EqualFold(strings.TrimSpace(prevLive.Master), strings.TrimSpace(master.DeviceID)) {
		return
	}

	// The handler's budget is usually spent by the time we get here, which is
	// exactly why the restore needs its own.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()

	if live, err := getZone(rctx); err == nil && len(live.Members) > 0 {
		return // the firmware still has a group, nothing was lost
	}
	s.logger.Warn("zone: the group that was playing is gone after the failed change, forming it again",
		"master", master.DeviceID, "members", len(prevLive.Members))
	if err := setZone(rctx, master, prevLive.Members); err != nil {
		s.logger.Warn("zone: could not form the previous group again", "err", err, "master", master.DeviceID)
		return
	}
	s.logger.Info("zone: the previous group is back", "master", master.DeviceID, "members", len(prevLive.Members))
}
