// zonedrive.go: the native zone drive, extracted from handleZoneForm so it
// can run WITHOUT an HTTP request behind it. handleZoneForm remains the only
// user-facing caller and keeps its exact HTTP behavior; the group-template
// activation and the permanent-group engine reuse the same drive with
// different options instead of duplicating the most field-hardened code in
// the repo (every dated incident comment below was paid for live).

package webui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// zoneDriveOpts selects which of the form path's side effects a caller wants.
// handleZoneForm passes everything true; background drives (boot re-form,
// wake rejoin) run without coalescing, resume capture, or a master wake.
type zoneDriveOpts struct {
	// coalesce takes a zoneFormSeq number, waits the settle window, and
	// stands down when a newer request arrived. User actions only: a
	// background drive must never bump the sequence, or a settled user
	// request would wrongly read itself as superseded.
	coalesce bool
	// persist writes the zone document (zones.json) before driving.
	persist bool
	// resume captures the currently playing stream and arms the conditional
	// post-form restart (resumeAfterZoneForm).
	resume bool
	// wake runs ensureBoxReadyErr on the master before driving.
	wake bool
	// reason tags the log lines: "form" | "activate" | "boot" |
	// "standby-wake" | "preplay" | "wake-rejoin".
	reason string
}

// zoneDriveResult carries the drive's outcome in the exact shape
// handleZoneForm answers with, so the HTTP behavior stays byte-compatible.
type zoneDriveResult struct {
	status  int            // HTTP status
	errText string         // non-empty: text/plain error body (http.Error path)
	body    map[string]any // non-nil: JSON body
}

func (r zoneDriveResult) write(w http.ResponseWriter) {
	if r.errText != "" {
		http.Error(w, r.errText, r.status)
		return
	}
	writeJSON(w, r.status, r.body)
}

// correctZoneMemberIDs asks each member's own firmware /info for its real
// SoundTouch deviceID and overrides the caller's value. A speaker has two
// MACs and only one of them is the deviceID the firmware keys /setZone on;
// mDNS announces the other. The master's side of this was fixed long ago and
// called fatal, but a slave named by the wrong id is quietly just as broken:
// the master registers a member, the follower never recognises itself, and
// the zone reads back with the member "missing" while looking fine on the
// master.
//
// Measured 2026-08-09 on a SoundTouch 10, which reports deviceID
// EC24B8B790CC while announcing 7CEC79F9ECA2 over mDNS: a group formed from
// the phone came back ok=false, verified=0, and the follower's own /getZone
// said {"members":[]}.
//
// Each slave is asked directly, by IP, which is the one identifier that is
// never ambiguous. Sequential rather than parallel on purpose: this runs
// inside the form budget, /info answers in milliseconds on a reachable box,
// and a fleet-wide fan-out on a speaker with 120 MB of RAM buys nothing.
// Falling back to the caller's value when that read fails is not safe, and
// two field bundles on the same day showed why (#544 and a 7-speaker fleet,
// both 2026-08-13). A member that is waking or busy right after an OTA
// answers :8090 a few seconds late, the correction was skipped, and the
// wrong id went into /setZone: the master enrolls a member nobody answers
// for, its own zone reads back one member short, and the speaker sits there
// showing "Select a source". In the 7-speaker log the correction is visible
// firing for .26 at 18:38:10 and then NOT firing for the very same speaker
// 25 seconds later, when its :8090 timed out, which put both that box and
// one other into the group under their wlan0 MAC.
//
// So remember what a speaker's firmware said last time and use that when it
// cannot be asked right now. The map is keyed by IP, the same identifier the
// read uses, and it is only ever written from a firmware answer.
func (s *Server) correctZoneMemberIDs(ctx context.Context, slaves []boxapi.ZoneMember) []boxapi.ZoneMember {
	for i := range slaves {
		if slaves[i].IP == "" {
			continue
		}
		ictx, icancel := context.WithTimeout(ctx, 2*time.Second)
		info, err := boxapi.New(slaves[i].IP).GetInfo(ictx)
		icancel()
		real := ""
		if err == nil {
			real = strings.TrimSpace(info.DeviceID)
			if real != "" {
				s.rememberMemberDeviceID(slaves[i].IP, real)
			}
		}
		if real == "" {
			cached, ok := s.cachedMemberDeviceID(slaves[i].IP)
			if !ok {
				// Never seen this speaker answer: the caller's value is all
				// there is, and refusing the member outright would break the
				// common case where it is already correct.
				continue
			}
			if strings.EqualFold(cached, slaves[i].DeviceID) {
				continue
			}
			s.logger.Warn("zone: member did not answer its firmware /info, using the deviceID it reported earlier instead of the caller's",
				"ip", slaves[i].IP, "supplied", slaves[i].DeviceID, "cached", cached, "err", err)
			slaves[i].DeviceID = cached
			continue
		}
		if strings.EqualFold(real, slaves[i].DeviceID) {
			continue
		}
		s.logger.Info("zone: corrected a member's deviceID from its own firmware /info (the caller had the chassis wlan0/SMSC MAC, not the SoundTouch ID)",
			"ip", slaves[i].IP, "supplied", slaves[i].DeviceID, "firmware", real)
		slaves[i].DeviceID = real
	}
	return slaves
}

// driveZone runs the native (or mirror) zone drive: coalescing, the leader
// probe, the previous-group snapshot, persist, the incremental-or-full
// firmware drive, follower verification, and the conditional stream restart.
// This is handleZoneForm's body from the coalescer down, moved verbatim; the
// opts gates default a background caller to "touch nothing but the zone".
func (s *Server) driveZone(ctx context.Context, master boxapi.ZoneMember, slaves []boxapi.ZoneMember, name, mode string, opts zoneDriveOpts) zoneDriveResult {
	c := boxapi.New(s.boxHost)

	// Coalesce rapid successive form requests (adding speakers one tap after
	// another): every caller sends the FULL member list it wants, so the
	// newest request carries the newest intent and older ones can stand down.
	// Each arrival takes a sequence number; after a short settle it waits its
	// turn on the serial lock, and a request that is no longer the newest
	// answers with the live zone instead of driving a stale list. Without
	// this, N quick taps ran N full drives back to back (live 2026-08-21,
	// three drives in 20 s, each one restarting the master's stream).
	if opts.coalesce {
		mySeq := s.zoneFormSeq.Add(1)
		select {
		case <-time.After(zoneCoalesceSettle):
		case <-ctx.Done():
			return zoneDriveResult{status: http.StatusRequestTimeout, errText: "canceled"}
		}
		s.zoneFormSerial.Lock()
		defer s.zoneFormSerial.Unlock()
		if latest := s.zoneFormSeq.Load(); latest != mySeq {
			liveNow, lerr := c.GetZone(ctx)
			s.logger.Info("zone: form request superseded by a newer member list, standing down", "seq", mySeq, "latest", latest)
			if lerr != nil {
				return zoneDriveResult{status: http.StatusOK, body: map[string]any{"ok": true, "mode": "native", "superseded": true}}
			}
			return zoneDriveResult{status: http.StatusOK, body: map[string]any{
				"ok": true, "mode": "native", "superseded": true,
				"master": liveNow.Master, "senderIP": liveNow.SenderIP, "members": liveNow.Members,
			}}
		}
	} else {
		// Background drives skip the settle but still serialize against user
		// actions and each other.
		s.zoneFormSerial.Lock()
		defer s.zoneFormSerial.Unlock()
		s.logger.Info("zone: forming (beta)", "mode", mode, "master", master.DeviceID, "masterIP", master.IP,
			"slaves", len(slaves), "reason", opts.reason)
	}

	// Ask the leader ONE cheap question before touching anything. On 2026-08-18
	// a fleet bundle showed the leader's BoseApp (:8090) frozen for minutes
	// BEFORE the user added a third speaker: the form then burned ~30 s in
	// timing-out pre-reads and the playing 2-box group was lost along the way.
	// A leader whose :8090 does not answer cannot take a /setZone anyway, so
	// fail fast here, with the stored document and the live group untouched.
	// Mirror mode is exempt: it streams to each member directly and does not
	// need the leader's :8090. (A reboot of the leading speaker clears this
	// firmware freeze; the wedge is documented in status_index.go.)
	if mode != "mirror" {
		if perr := s.speakerStaysSilent(ctx, c); perr != nil {
			s.logger.Warn("zone: the speaker leading the group is not answering, not starting the group change",
				"probeErr", perr, "master", master.DeviceID, "reason", opts.reason)
			return zoneDriveResult{status: http.StatusBadGateway, errText: "the speaker leading the group is not answering: " + perr.Error()}
		}
	}

	// What the user already had, read BEFORE anything is changed. Adding a
	// speaker to a group that is playing must not be able to end with no group
	// at all, and on 2026-08-16 it did: a working pair, a third speaker added,
	// and 24 s later the firmware had dissolved the pair while the master's
	// :8090 stopped answering mid-drive. The music stopped in both rooms and
	// nothing put it back. Keeping the previous group here is what makes the
	// restore below possible.
	prevDoc, hadPrevDoc := zones.Zone{}, false
	if s.zones != nil {
		prevDoc, hadPrevDoc = s.zones.Get()
	}
	prevLive, prevLiveErr := c.GetZone(ctx)
	if prevLiveErr != nil {
		// A swallowed error here left the restore blind on 2026-08-18: the
		// leader's :8090 died between the probe above and this read, prevLive
		// came back empty, and restorePreviousZone concluded "there was no live
		// group to lose". Fall back to the stored document, which describes the
		// group the user last asked for, so the restore still knows what to put
		// back once the leader answers again.
		s.logger.Warn("zone: could not read the live group before changing it, falling back to the stored document for the restore",
			"err", prevLiveErr)
		if hadPrevDoc && !prevDoc.Stereo && len(prevDoc.Slaves) > 0 &&
			strings.EqualFold(strings.TrimSpace(prevDoc.Master), strings.TrimSpace(master.DeviceID)) {
			prevLive.Master = prevDoc.Master
			for _, m := range prevDoc.Slaves {
				prevLive.Members = append(prevLive.Members, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
			}
		}
	}

	// Persist first so a transient drive error still leaves the group on record
	// for the reconcile loop to retry. Only the master persists.
	if opts.persist {
		z := zones.Zone{Master: master.DeviceID, MasterIP: master.IP, Mode: mode, Name: name}
		for _, m := range slaves {
			z.Slaves = append(z.Slaves, zones.Member{DeviceID: m.DeviceID, IP: m.IP})
		}
		if s.zones != nil {
			if err := s.zones.Set(z); err != nil {
				s.logger.Warn("zone: persist failed", "err", err)
			}
		}
	}

	if mode == "mirror" {
		// Deliberate user action: push unconditionally (reconcile=false), the
		// user just asked for exactly this group.
		z := zones.Zone{Master: master.DeviceID, MasterIP: master.IP, Mode: mode, Name: name}
		for _, m := range slaves {
			z.Slaves = append(z.Slaves, zones.Member{DeviceID: m.DeviceID, IP: m.IP})
		}
		s.mirrorToSlaves(ctx, z, false)
		return zoneDriveResult{status: http.StatusOK, body: map[string]any{"ok": true, "mode": "mirror"}}
	}

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
	var resume *lastPlayInfo
	if opts.resume {
		if _, busy := s.boxPlayState(); busy {
			s.lastPlayMu.Lock()
			if s.lastPlay != nil {
				cp := *s.lastPlay
				resume = &cp
			}
			s.lastPlayMu.Unlock()
		}
	}

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
	if opts.wake {
		if err := s.ensureBoxReadyErr(ctx); err != nil {
			perr := s.speakerStaysSilent(ctx, c)
			if perr != nil {
				s.logger.Warn("zone: the speaker leading the group is not answering at all, not sending setZone",
					"wakeErr", err, "probeErr", perr, "master", master.DeviceID, "prevMembers", len(prevLive.Members))
				s.restorePreviousZone(ctx, c, master, prevDoc, hadPrevDoc, prevLive)
				return zoneDriveResult{status: http.StatusBadGateway, errText: "the speaker leading the group is not answering: " + perr.Error()}
			}
			s.logger.Info("zone: the speaker did not report waking, but it is answering, so the group is formed anyway",
				"wakeErr", err, "master", master.DeviceID)
		}
	}

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
	if !usedIncremental {
		if err := c.SetZone(ctx, master, slaves); err != nil {
			s.logger.Warn("zone: setZone failed", "err", err, "master", master.DeviceID)
			s.restorePreviousZone(ctx, c, master, prevDoc, hadPrevDoc, prevLive)
			return zoneDriveResult{status: http.StatusBadGateway, errText: "setZone: " + err.Error()}
		}
	}
	z2, err := c.GetZone(ctx)
	if err != nil {
		s.logger.Warn("zone: formed but getZone read-back failed", "err", err)
		return zoneDriveResult{status: http.StatusOK, body: map[string]any{"ok": true, "mode": "native"}}
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
	// Incremental join where NOT ONE added member confirmed: distrust
	// /addZoneSlave on this firmware and run the proven full re-form once.
	if usedIncremental && len(toAdd) > 0 && len(missing) == len(toAdd) {
		s.logger.Warn("zone: no added member confirmed the incremental join, re-forming the whole zone once", "adding", len(toAdd))
		if err := c.SetZone(ctx, master, slaves); err != nil {
			s.logger.Warn("zone: fallback setZone failed", "err", err, "master", master.DeviceID)
			s.restorePreviousZone(ctx, c, master, prevDoc, hadPrevDoc, prevLive)
			return zoneDriveResult{status: http.StatusBadGateway, errText: "setZone: " + err.Error()}
		}
		if z2, err = c.GetZone(ctx); err != nil {
			s.logger.Warn("zone: formed but getZone read-back failed", "err", err)
			return zoneDriveResult{status: http.StatusOK, body: map[string]any{"ok": true, "mode": "native"}}
		}
		missing, unverifiable = verifyFollowersJoined(ctx, s.logger, z2.Master, slaves, fetchFollower)
	}
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
	// Pre-existing members of an incremental join count as verified: only the
	// added ones were polled, so "missing" can only name those.
	verified := len(slaves) - len(missing)
	// Regression guard (#70 / Albrecht 0.8.x): if the master's own read-back shows
	// no members and no master after SetZone, the firmware never actually formed a
	// zone (it worked in 0.7.29, broke in 0.8.0x). Report that honestly as ok=false
	// so the app stops claiming success when nothing joined, instead of leaning on
	// the optimistic "ok=true" the old code always returned.
	masterFormed := len(z2.Members) > 0 && z2.Master != ""
	ok := verified > 0
	if !masterFormed {
		s.logger.Warn("zone: master read-back empty after setZone (slaves did not join — possible 0.8.x regression)",
			"liveMaster", z2.Master, "liveMembers", len(z2.Members), "requestedSlaves", len(slaves))
	}
	s.logger.Info("zone: formed", "mode", "native", "ok", ok, "liveMaster", z2.Master,
		"requestedSlaves", len(slaves), "liveMembers", len(z2.Members),
		"masterMissing", strings.Join(masterMissing, ","),
		"verified", verified, "missing", strings.Join(missing, ","),
		"unverifiable", strings.Join(unverifiable, ","), "reason", opts.reason)
	if resume != nil && masterFormed {
		go s.resumeAfterZoneForm(*resume)
	}
	return zoneDriveResult{status: http.StatusOK, body: map[string]any{
		"ok": ok, "mode": "native", "master": z2.Master, "senderIP": z2.SenderIP,
		"members": z2.Members, "requested": len(slaves),
		"verified": verified, "missing": missing, "unverifiable": unverifiable,
		"masterMissing": masterMissing,
	}}
}
