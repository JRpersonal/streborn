// Taking a group or a stereo pair apart: DELETE /api/box/zone. Split out
// of zones_stereo.go.

package webui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// zoneTeardown1036Window is how long a dissolve's own 1036 rejections are kept
// out of the storm count. The handler's budget is 8 s and the firmware keeps
// answering for a beat after it, so the window covers both rather than ending
// mid-teardown.
const zoneTeardown1036Window = 12 * time.Second

// handleZoneDissolve tears down the zone this box leads and stops re-forming it.
// It reads the two switches off the query and renders what dissolveZone
// answers; every answer of a dissolve is a 200 with a JSON body. The desktop
// app (DissolveZone, the undo-pair action), the phone remote and the
// groupkeys client read these bodies, so they are pinned byte for byte.
func (s *Server) handleZoneDissolve(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, s.dissolveZone(r.Context(), zoneDissolveReq{
		Stereo: q.Get("stereo") == "1",
		Forget: q.Get("forget") == "1",
	}))
}

// zoneDissolveReq is what a dissolve is asked to do beyond taking the live
// group apart.
type zoneDissolveReq struct {
	// Stereo is "Undo stereo pair" (?stereo=1): act on a stereo pair, and on
	// nothing else. Without it a pair is left alone.
	Stereo bool
	// Forget (?forget=1) deletes a saved permanent group as well, which a
	// plain dissolve keeps.
	Forget bool
}

// zoneDissolveAnswer is the JSON body DELETE /api/box/zone answers with,
// always under a 200.
type zoneDissolveAnswer map[string]any

// zoneDissolveRun is the state one dissolve carries from phase to phase: the
// request, the budgeted context and the firmware client, and who is to be
// taken apart. The phase bodies are handleZoneDissolve's former body, moved;
// each starts by naming the fields it reads the way that body did.
type zoneDissolveRun struct {
	req zoneDissolveReq
	ctx context.Context
	c   *boxapi.Client

	master boxapi.ZoneMember
	slaves []boxapi.ZoneMember
	// stereo: what is being dissolved is a stereo pair, not a multiroom zone.
	stereo bool
}

// dissolveZone is the service behind DELETE /api/box/zone: it takes the
// group or the stereo pair of this box apart and says what happened. ctx is
// the caller's; the dissolve runs on its own 8 s budget below it.
//
// The phases, top to bottom, each one finishing the dissolve with an answer
// or handing over to the next. The order of the calls to the speakers is
// the order they had in the handler.
func (s *Server) dissolveZone(ctx context.Context, req zoneDissolveReq) zoneDissolveAnswer {
	// A dissolve is a membership change like any form, so stamp the sequence:
	// a join-volume applier still sitting in its settle has to stand down
	// instead of writing the group's level onto a speaker that just left it.
	// Before the serial lock on purpose, so it also supersedes a form that is
	// still inside its coalesce window rather than only one already past its
	// own check.
	s.zoneFormSeq.Add(1)
	// Same serial lock as handleZoneForm: a dissolve arriving between two
	// member changes executes in arrival order and never interleaves with a
	// drive against the firmware.
	s.zoneFormSerial.Lock()
	defer s.zoneFormSerial.Unlock()
	// Taking a zone apart kills the in-flight UPnP session on every member, and
	// the members answer that with errorUpdate 1036
	// UpnpRcvdContentItemInWrongState. Expected, caused by us, and no evidence
	// at all about the box - but the storm counter used to count it, so a user
	// who formed and dissolved a group a few times was shown a red banner
	// claiming the speaker refuses every station (field, 2026-09-07). Armed
	// before the teardown starts, so the frames it produces arrive inside the
	// window.
	s.suppress1036For(zoneTeardown1036Window, "zone teardown")
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	run := &zoneDissolveRun{req: req, ctx: ctx, c: c}
	if ans := s.zoneDissolveTargets(run); ans != nil {
		return ans
	}
	if ans := s.zoneDissolveConfirmPair(run); ans != nil {
		return ans
	}
	if run.stereo {
		return s.dissolveStereoPair(run)
	}
	if run.master.DeviceID == "" && len(run.slaves) == 0 {
		return s.zoneDissolveNothing(run)
	}
	return s.zoneDissolveMultiroom(run)
}

// zoneDissolveTargets works out who is to be taken apart: the persisted
// membership first, the live zone when the store is empty. A plain dissolve
// that finds a persisted stereo pair ends here and leaves it alone.
func (s *Server) zoneDissolveTargets(run *zoneDissolveRun) zoneDissolveAnswer {
	ctx, c := run.ctx, run.c
	var master boxapi.ZoneMember
	var slaves []boxapi.ZoneMember
	stereo := false
	wantStereo := run.req.Stereo
	// Prefer the persisted membership; fall back to the live zone so a dissolve
	// still works after an agent restart.
	if s.zones != nil {
		if z, ok := s.zones.Get(); ok {
			// A STR-formed stereo pair persists Stereo=true. A plain Ungroup
			// (no ?stereo=1) must NEVER take a pair apart: that is what "Undo
			// stereo pair" (?stereo=1) is for, and the design contract is that a
			// plain multiroom dissolve keeps its no-op semantics for a pair.
			// Honouring the persisted Stereo flag here regardless of intent was
			// the bug that let Ungroup unpair a stereo pair (#843): the ?stereo=1
			// gate below is only reached with an empty store, so a persisted pair
			// slipped straight through to the RemoveGroup teardown.
			if z.Stereo && !wantStereo {
				// Leave the pair persisted and untouched; report nothing-to-do so
				// the app can say there was no group to ungroup.
				s.logger.Info("zone: plain dissolve left a stereo pair intact (use undo-stereo-pair)", "master", z.Master)
				return zoneDissolveAnswer{"ok": true, "nothing": true, "stereoPairKept": true}
			}
			master = boxapi.ZoneMember{DeviceID: z.Master, IP: z.MasterIP}
			stereo = z.Stereo
			for _, m := range z.Slaves {
				slaves = append(slaves, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
			}
		}
	}
	// The persisted store can be empty here (a slave got the dissolve, or the
	// agent was reinstalled) while the FIRMWARE still holds state. Consult the
	// live zone first, then the live stereo group, before deciding there is
	// nothing to do: field bundles showed a box logging
	// `zone: dissolving (beta) master="" slaves=0` in an endless retry storm
	// because the dissolve never looked past the empty store.
	if !stereo && master.DeviceID == "" {
		if z, err := c.GetZone(ctx); err == nil && z.Master != "" {
			master = boxapi.ZoneMember{DeviceID: z.Master, IP: z.SenderIP}
			for _, m := range z.Members {
				slaves = append(slaves, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
			}
		}
	}
	run.master, run.slaves, run.stereo = master, slaves, stereo
	return nil
}

// zoneDissolveConfirmPair is the "Undo stereo pair" half that the store
// could not answer: it asks the firmware whether this speaker is in a pair,
// refuses when it holds group state and no pair is confirmed, and retargets
// the dissolve at the pair when one is.
func (s *Server) zoneDissolveConfirmPair(run *zoneDissolveRun) zoneDissolveAnswer {
	ctx, c, wantStereo := run.ctx, run.c, run.req.Stereo
	master, slaves, stereo := run.master, run.slaves, run.stereo
	// "Undo stereo pair" asks the PAIR AUTHORITY, always.
	//
	// This escalation used to carry `master.DeviceID == ""` as a precondition,
	// which quietly made it unreachable on any speaker holding group state: the
	// two blocks above fill master from the persisted document or from the live
	// zone, so by the time intent was consulted there was already a master and
	// the read never happened. `?stereo=1` therefore ADDED a path without ever
	// RESTRICTING the handler to it, and the request fell through to the
	// multiroom teardown below.
	//
	// #907, reported with a bundle that proves it twice over: three ST10s in a
	// plain group, no pair anywhere (marge_group present=false on all three,
	// the firmware answering /getGroup empty), and four presses of "Undo stereo
	// pair" each logging `zone: dissolving (beta) slaves=2` and purging both
	// peers' stores. The stereo arm's own log line appears in the same bundle
	// exactly once, the day before, when she really did have a pair.
	//
	// Dropping the precondition also fixes the mirror case: a speaker carrying
	// a stale multiroom document while it IS in a real firmware pair (paired in
	// the Bose app, or the agent reinstalled over an old document) could never
	// be unpaired from that speaker, because intent was never consulted there
	// either.
	//
	// The read gets its OWN short budget rather than the dissolve's, for the
	// reason spelled out at the top of this file: /getGroup HANGS on scm/BCO
	// chassis, silently, and on the dissolve's context that hang would eat the
	// whole request.
	if !stereo && wantStereo {
		gctx, gcancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		g, gerr := c.GetGroup(gctx)
		gcancel()
		pairConfirmed := gerr == nil && (g.ID != "" || len(g.Members) > 0)
		// Group state we would otherwise tear down. This is the ONLY thing the
		// refusal has to protect, and scoping it this way keeps the
		// nothing-to-dissolve branch below reachable, which is where a phantom
		// marge pair record gets cleared. Refusing on a speaker that holds
		// nothing at all would have closed that escape hatch.
		inZone := master.DeviceID != "" || len(slaves) > 0
		if !pairConfirmed && inZone {
			// Only a POSITIVE confirmation may act, so an unreadable answer
			// refuses too. /getGroup hangs rather than refuses on scm/BCO
			// chassis, and treating a hang as "carry on" is exactly what put
			// the multiroom teardown behind this button. A STR-formed pair is
			// covered by its own persisted document above and never reaches
			// here, so the cost of refusing an unconfirmed pair is a retry,
			// while the cost of continuing is somebody's group.
			s.logger.Info("stereo: undo-pair refused, no pair confirmed on this speaker",
				"readable", gerr == nil, "master", master.DeviceID, "slaves", len(slaves))
			return zoneDissolveAnswer{
				"ok": false, "nothing": true, "noPair": true, "inZone": true,
				"pairReadable": gerr == nil,
			}
		}
		if pairConfirmed {
			// The firmware confirms a pair, so dissolve it as a pair, whatever
			// the local document happened to say. That covers both the
			// no-persisted-zone case this was written for and a speaker holding
			// a stale multiroom document over a real pair. The members are partitioned relative to THIS box, not
			// the group master — the dissolve may run on the RIGHT/slave box
			// (the store only exists on the master), where "everyone but the
			// master" would be ourselves and the remote teardown would clear
			// this box twice while the real partner kept the pair.
			stereo = true
			selfID := s.localDeviceID(ctx, c, "")
			master = boxapi.ZoneMember{DeviceID: g.MasterDeviceID}
			slaves = nil
			for _, m := range g.Members {
				if strings.EqualFold(m.DeviceID, g.MasterDeviceID) {
					master.IP = m.IP
				}
				if selfID != "" {
					if strings.EqualFold(m.DeviceID, selfID) {
						continue // ourselves; the partner is the OTHER member
					}
				} else if strings.EqualFold(m.DeviceID, g.MasterDeviceID) {
					continue // self unknown: fall back to assuming we lead
				}
				slaves = append(slaves, m)
			}
			s.logger.Info("stereo: no persisted zone but the firmware reports a pair, dissolving that",
				"id", g.ID, "master", g.MasterDeviceID, "self", selfID, "members", len(g.Members))
		}
	}
	run.master, run.slaves, run.stereo = master, slaves, stereo
	return nil
}

// dissolveStereoPair takes a stereo pair apart on BOTH halves: this box's
// firmware, the partner's firmware, both marge records, and the stored
// document. In that order.
func (s *Server) dissolveStereoPair(run *zoneDissolveRun) zoneDissolveAnswer {
	ctx, c, master, slaves := run.ctx, run.c, run.master, run.slaves
	// A stereo pair is a firmware-native L/R group, so tear it down with the
	// matching endpoint (GET /removeGroup), not the multiroom /removeZoneSlave.
	// The PARTNER's firmware keeps its own copy of the pair (GroupService),
	// and a partner left uncleared answers every later /addGroup with 5510
	// GROUP_ALREADY_EXISTS — so clear both firmwares and both marge records,
	// not just our own. Always clear our store afterwards so we stop
	// honoring the pair.
	partnerIP, partnerID := "", ""
	if len(slaves) > 0 {
		partnerIP, partnerID = slaves[0].IP, slaves[0].DeviceID
	}
	s.logger.Info("stereo: dissolving pair via /removeGroup (beta)", "master", master.DeviceID, "partnerIP", partnerIP)
	if err := c.RemoveGroup(ctx); err != nil {
		s.logger.Warn("stereo: removeGroup failed (the user may need to undo the pair in the Bose app)", "err", err)
	}
	if partnerIP != "" {
		// The stored partner IP can be stale (DHCP renewal since pairing):
		// confirm the box at that address IS the recorded partner before
		// tearing its pair down. A mismatch is reported, not acted on.
		pc := boxapi.New(partnerIP)
		skipRemote := false
		if partnerID != "" {
			if pinfo, perr := pc.GetInfo(ctx); perr == nil &&
				strings.TrimSpace(pinfo.DeviceID) != "" && !strings.EqualFold(pinfo.DeviceID, partnerID) {
				s.logger.Warn("stereo: box at the stored partner IP is a DIFFERENT speaker, skipping its teardown",
					"partnerIP", partnerIP, "expected", partnerID, "found", pinfo.DeviceID)
				skipRemote = true
			}
		}
		if skipRemote {
			partnerIP = ""
		} else if err := pc.RemoveGroup(ctx); err != nil {
			s.logger.Warn("stereo: removeGroup on the partner failed", "err", err, "partnerIP", partnerIP)
		} else {
			s.logger.Info("stereo: partner firmware pair cleared", "partnerIP", partnerIP)
		}
	}
	if s.margeGroupClear != nil {
		s.margeGroupClear("dissolve")
	}
	partnerCleared := s.pushGroupDocToPartner(ctx, partnerIP, "")
	if s.zones != nil {
		if err := s.clearZoneDoc(); err != nil {
			s.logger.Warn("stereo: clear store failed", "err", err)
		}
	}
	return zoneDissolveAnswer{
		"ok": true, "stereo": true,
		"partnerIP": partnerIP, "partnerDeviceID": partnerID,
		"partnerMargeCleared": partnerCleared,
	}
}

// zoneDissolveNothing answers for a speaker that holds no group at all, and
// clears what is provably stale on the way: the stored document, and a marge
// pair record the firmware no longer backs.
func (s *Server) zoneDissolveNothing(run *zoneDissolveRun) zoneDissolveAnswer {
	ctx, c := run.ctx, run.c
	// Nothing to act on: no persisted zone, firmware zone empty. Say so
	// once instead of pretending to dissolve — and still clear the store
	// so a repeating caller stops finding stale state to retry on.
	firmwarePair := false
	if g, err := c.GetGroup(ctx); err == nil && (g.ID != "" || len(g.Members) > 0) {
		// A plain dissolve leaves a firmware stereo pair alone (the
		// escalation above needs ?stereo=1); report it so the caller can
		// tell "standalone" from "paired but not asked to unpair".
		firmwarePair = true
		s.logger.Info("zone: nothing to dissolve, but the firmware holds a stereo pair (use the undo-pair action to dissolve it)", "id", g.ID)
	} else if err == nil && s.margeGroupClear != nil {
		// Firmware has neither zone nor group: a stored marge pair record
		// is provably phantom in this state (a factory reset wipes the
		// firmware pairing but not /mnt/nv/streborn), so drop it — the
		// explicit escape hatch.
		s.margeGroupClear("nothing to dissolve (firmware reports no pair)")
	}
	if !firmwarePair {
		s.logger.Info("zone: nothing to dissolve (no persisted zone, firmware reports no zone and no group)")
	}
	if s.zones != nil {
		_ = s.clearZoneDoc()
	}
	return zoneDissolveAnswer{"ok": true, "nothing": true, "firmwarePair": firmwarePair}
}

// zoneDissolveMultiroom takes the live multiroom zone apart: the members off
// the master until the firmware reports the zone empty, the stragglers
// silenced, the stored document cleared (a saved permanent group only when
// asked), and the members' own stores purged. The answer says whether the
// speaker confirmed it.
func (s *Server) zoneDissolveMultiroom(run *zoneDissolveRun) zoneDissolveAnswer {
	ctx, c, master, slaves := run.ctx, run.c, run.master, run.slaves
	s.logger.Info("zone: dissolving (beta)", "master", master.DeviceID, "slaves", len(slaves))
	// What the master is playing RIGHT NOW, before anything is torn down. It is
	// the only way to tell, afterwards, whether a member that is still making
	// noise is carrying the group's content or something of its own. See
	// dissolvestragglers.go.
	masterLocation := s.playingLocation(ctx, s.boxHost)
	// What the caller is told at the end. Empty means the speaker confirmed the
	// zone is gone; anything else names the reason it could not be confirmed.
	dissolveUnverified := ""
	remaining := 0
	if master.DeviceID != "" && len(slaves) > 0 {
		// Loop until the firmware reports an empty zone (or the ctx deadline): a
		// single RemoveZoneSlave can leave a straggler, which forced a SECOND
		// ungroup press to clear the speaker's display (#70). Bounded by the 8s
		// ctx and a small attempt cap so a box that never lets go cannot hang.
		cur := slaves
		for attempt := 0; attempt < 4 && len(cur) > 0; attempt++ {
			if err := c.RemoveZoneSlave(ctx, master, cur); err != nil {
				// Log but keep going; the store is cleared below regardless so we
				// stop re-forming a broken zone.
				s.logger.Warn("zone: removeZoneSlave failed", "err", err, "attempt", attempt)
			}
			z, err := c.GetZone(ctx)
			if err != nil {
				// Unreadable is not the same as gone. Saying "dissolved" here is
				// how a group that is still playing was reported as taken apart.
				dissolveUnverified = err.Error()
				break
			}
			if z.Master == "" || len(z.Members) == 0 {
				cur = nil
				break // the speaker confirms it: the zone is gone
			}
			cur = z.Members
			s.logger.Info("zone: members still present after removeZoneSlave, retrying",
				"remaining", len(cur), "members", memberIDs(cur), "attempt", attempt)
		}
		// The master took every call and kept the group. Try the two routes the
		// batch-at-the-master loop above cannot reach before answering.
		if len(cur) > 0 && dissolveUnverified == "" {
			left, reason := s.escalateDissolve(c, master, cur)
			cur = left
			if reason != "" {
				dissolveUnverified = reason
			}
		}
		remaining = len(cur)
	}
	// The teardown above only reaches members the MASTER registered. One it
	// never registered still got audio and would play on in an empty room, so
	// silence any that are demonstrably still on the group's stream. A mirror
	// group has no firmware zone at all, so the loop above did nothing and this
	// is the ONLY thing that stops the followers: pass the master's mirror-proxy
	// host:port (masterIP:17008), the address every mirror slave pulls from, so a
	// follower on that stream is recognised even though its URL never equals the
	// master's own loopback location.
	mirrorHostPort := hostPortOf(s.mirrorURLForSlaves(ctx, masterLocation, master.IP))
	s.stopStragglers(ctx, masterLocation, mirrorHostPort, slaves)
	if s.zones != nil {
		// A PERMANENT group is a saved arrangement, not the live zone. Taking
		// the live zone apart has to leave the saved one alone, or "ungroup for
		// now" silently deletes what the user built and there is no way back.
		//
		// Every other Clear() call site already knows this: the peer purge
		// checks Permanent (zonemirror.go), and so does the standby two-strike
		// doubt clear (resume_standby.go). This one did not, and it is the one
		// behind the button. It cost a stored group on the maintainer's own
		// fleet during a test on 2026-09-03, and it is the first thing a
		// reporter suspects when a group is missing after an update, which is
		// how it came up again on 2026-09-09 (that group turned out to be
		// intact; this path is what would have taken it).
		// ... unless the caller says it means the saved one as well. That is a
		// separate, deliberate act in the UI (the x on the saved group's own
		// dashed frame), and without it a saved group could not be removed from
		// the app at all: every route led here, and here it always survived.
		// Reported as "the multiroom connection cannot be separated", with a log
		// showing three dissolves in ninety seconds and the group re-forming
		// each time (#119).
		forget := run.req.Forget
		if doc, ok := s.zones.Get(); ok && doc.Permanent && !forget {
			s.logger.Info("zone: live group taken apart, the saved group stays saved and forms again when its main speaker plays",
				"master", doc.Master, "members", len(doc.Slaves))
		} else if forget {
			if err := s.zones.Forget(); err != nil {
				s.logger.Warn("zone: clear store failed", "err", err)
			} else {
				s.logger.Info("zone: the saved group was deleted as well, it will not form again on its own")
			}
		} else if err := s.clearZoneDoc(); err != nil {
			s.logger.Warn("zone: clear store failed", "err", err)
		}
	}
	// Also clear the group from every member's own persisted store
	// (best-effort, background): a member that itself persisted a zone naming
	// this box would otherwise keep re-forming the group forever (#342).
	if master.DeviceID != "" || master.IP != "" {
		s.purgePeerZones(master, slaves)
	}
	// Report what actually happened. This used to answer ok:true whatever the
	// firmware said, so a dissolve that removed nothing, and one whose result
	// could not be read at all, both looked like success, and the user pressed
	// the button again on a group that was still playing.
	resp := zoneDissolveAnswer{"ok": dissolveUnverified == "" && remaining == 0}
	if remaining > 0 {
		resp["remaining"] = remaining
		resp["error"] = "the speaker still reports members in the group"
		s.logger.Warn("zone: dissolve did not empty the group, every teardown route was tried", "remaining", remaining)
	}
	if dissolveUnverified != "" {
		resp["unverified"] = dissolveUnverified
		s.logger.Warn("zone: dissolve could not be confirmed", "err", dissolveUnverified)
	}
	return resp
}
