// Forming a group: POST /api/box/zone, its request and answer, and the
// phases up to the point where the firmware is driven (the member
// resolve, the define-only template, the coalescing of rapid forms, the
// leader probe, the before-picture, the stored record and the mirror
// push), plus the budget and the member diff. Split out of zones_stereo.go.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

type zoneMemberReq struct {
	DeviceID string `json:"deviceID"`
	IP       string `json:"ip"`
}

type zoneFormReq struct {
	Master zoneMemberReq   `json:"master"`
	Slaves []zoneMemberReq `json:"slaves"`
	Name   string          `json:"name"`
	Stereo bool            `json:"stereo"`
	// Mode is "native" (firmware /setZone) or "mirror" (each slave's box pulls
	// the master's stream via UPnP). Empty defaults to native.
	Mode string `json:"mode"`
	// Permanent opts the group into the play-triggered re-form with member
	// wake (#70). Off by default (opt-in, Jens 2026-08-26).
	Permanent bool `json:"permanent"`
	// DefineOnly persists a permanent group as a template only: no wake, no
	// /setZone, no stream re-push. Forming a group wakes the master, and a woken
	// master resumes its last source, so creating a permanent group used to start
	// playback in every room (2026-08-31). The group forms itself on the next
	// play (formDefaultGroupOnPlay), so defining it is silent. Only honoured with
	// Permanent set.
	DefineOnly bool `json:"defineOnly"`
	// WokenFromStandby lists the member addresses the app woke out of standby
	// for this form. What those members play right now is their own power-on
	// resume, not music the group should take over (zoneformwoken.go). Older
	// apps do not send it and get the previous behaviour.
	WokenFromStandby []string `json:"wokenFromStandby,omitempty"`
	// from and ua name the client that sent the form (remote address and
	// User-Agent), for the "zone: forming" line. Not part of the wire body: a
	// group formed out of standby followed by a preset the user did not
	// remember starting could not be tied to any app (#1339).
	from, ua string
}

// sameMasterZone reports whether two zone records are led by the same speaker.
func sameMasterZone(a, b zones.Zone) bool {
	return a.Master != "" && a.Master == b.Master
}

// sameMembers reports whether two zone records carry the same member set,
// order ignored. Used only to decide whether a replacement is worth shouting
// about, so a re-form of the identical group stays quiet.
func sameMembers(a, b zones.Zone) bool {
	if len(a.Slaves) != len(b.Slaves) {
		return false
	}
	seen := make(map[string]int, len(a.Slaves))
	for _, m := range a.Slaves {
		seen[m.DeviceID]++
	}
	for _, m := range b.Slaves {
		seen[m.DeviceID]--
		if seen[m.DeviceID] < 0 {
			return false
		}
	}
	return true
}

// handleZoneForm creates (or replaces) a group with this box as master (#70 beta).
// Two user-switchable modes: "native" drives the Bose /setZone family so the
// firmware syncs the slaves (tightest, when the firmware accepts STR's source);
// "mirror" points each slave's box at the master's current stream over UPnP
// (looser sync, works more widely). Either way the group is persisted so it
// auto-reforms after a reboot/standby. The caller supplies the master's and
// slaves' deviceID+IP from discovery, so the agent need not self-identify.
//
// The phases, top to bottom, each one finishing the form with an answer or
// handing over to the next; the answers are rendered by writeZoneFormAnswer
// and nowhere else. The desktop app (FormZone), the phone remote and the
// groupkeys client all read these bodies, so they are pinned byte for byte.
func (s *Server) handleZoneForm(w http.ResponseWriter, r *http.Request) {
	var req zoneFormReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.from, req.ua = r.RemoteAddr, r.UserAgent()
	run, ans := s.zoneFormStart(req)
	if ans != nil {
		writeZoneFormAnswer(w, ans)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), zoneFormBudget(len(run.slaves)))
	defer cancel()
	run.ctx, run.c = ctx, boxapi.New(s.boxHost)
	s.zoneFormResolveMembers(run)
	if ans := s.zoneFormDefineOnly(run); ans != nil {
		writeZoneFormAnswer(w, ans)
		return
	}
	// A stereo pair is a firmware-native L/R group (POST /addGroup), not a
	// multiroom zone. It needs exactly one partner; the master is the LEFT
	// channel and the partner the RIGHT by Bose convention. Only the ST10
	// actually pairs, but every model lists /addGroup, so we let the firmware
	// be the authority and surface its real response to the app.
	if req.Stereo {
		s.formStereoPair(w, ctx, run.c, run.master, run.slaves, req.Name, wokenMemberSet(req.WokenFromStandby))
		return
	}
	writeZoneFormAnswer(w, s.zoneFormDrive(run))
}

// zoneFormAnswer is what POST /api/box/zone answers, before it is rendered:
// a plain-text refusal (Text, written with http.Error) or a JSON body.
type zoneFormAnswer struct {
	Status int
	Text   string
	JSON   map[string]any
}

func zoneFormRefusal(status int, msg string) *zoneFormAnswer {
	return &zoneFormAnswer{Status: status, Text: msg}
}

func zoneFormOK(body map[string]any) *zoneFormAnswer {
	return &zoneFormAnswer{Status: http.StatusOK, JSON: body}
}

// writeZoneFormAnswer renders an answer onto the wire, byte for byte what
// the handler wrote before it was split into phases.
func writeZoneFormAnswer(w http.ResponseWriter, a *zoneFormAnswer) {
	if a.JSON == nil {
		http.Error(w, a.Text, a.Status)
		return
	}
	writeJSON(w, a.Status, a.JSON)
}

// zoneFormRun is the state one group form carries from phase to phase: the
// request as the caller sent it, the normalised mode and members, the
// budgeted context and the firmware client, and what the phases learn on
// the way (the sequence number of this form, the before-picture of the
// group, the stored record, what to restart afterwards). The phase bodies
// are handleZoneForm's former body, moved; each starts by naming the fields
// it reads the way that body did.
type zoneFormRun struct {
	req    zoneFormReq
	mode   string
	master boxapi.ZoneMember
	slaves []boxapi.ZoneMember
	ctx    context.Context
	c      *boxapi.Client

	// mySeq is this form's sequence number in the coalescing of rapid
	// successive forms (zoneFormCoalesce).
	mySeq uint64
	// The before-picture: the stored document and the live zone as they
	// stood before anything was changed, for the restore on failure.
	prevDoc     zones.Zone
	hadPrevDoc  bool
	prevLive    boxapi.Zone
	prevLiveErr error
	// newlyJoined are the members actually joining, the only ones that get
	// the group's volume (#401); nil when the before-picture is not trusted.
	newlyJoined []boxapi.ZoneMember
	// z is the record being stored for this group.
	z zones.Zone
	// What to restart on the master after a native form (zoneFormPrepareResume).
	masterRef     *lastPlayInfo
	resume        *lastPlayInfo
	masterBlocked bool
	// The native drive's working state (zoneFormDriveNative).
	toAdd, toRemove []boxapi.ZoneMember
	usedIncremental bool
	heldIncremental bool
	readBackFailed  bool
	z2              boxapi.Zone
	missing         []string
	unverifiable    []string
}

// zoneFormStart checks the decoded request and normalises it: the mode, the
// member records, and the log line that names WHO is being grouped. It
// answers 400 when no member was asked for.
func (s *Server) zoneFormStart(req zoneFormReq) (*zoneFormRun, *zoneFormAnswer) {
	// The master's deviceID is optional. It is resolved from this box's own
	// firmware /info a few lines below and the supplied value is overwritten
	// anyway, so requiring it only kept out the one client that cannot know it:
	// the phone remote is served BY the master and has no reason to be told
	// which speaker it is running on.
	if len(req.Slaves) == 0 {
		return nil, zoneFormRefusal(http.StatusBadRequest, "at least one slave is required")
	}
	mode := req.Mode
	if mode != "mirror" {
		mode = "native"
	}
	// The member addresses are the one part of the request that ends up in a
	// URL the agent fetches, so they are taken as IP addresses and nothing
	// else: parsed, then written back in canonical form. A name, a port or a
	// multicast address is refused here, before anything is dialled.
	masterIP, err := memberAddr(req.Master.IP)
	if err != nil {
		return nil, zoneFormRefusal(http.StatusBadRequest, "master: "+err.Error())
	}
	master := boxapi.ZoneMember{DeviceID: req.Master.DeviceID, IP: masterIP}
	slaves := make([]boxapi.ZoneMember, 0, len(req.Slaves))
	for _, m := range req.Slaves {
		ip, err := memberAddr(m.IP)
		if err != nil {
			return nil, zoneFormRefusal(http.StatusBadRequest, "slave "+m.DeviceID+": "+err.Error())
		}
		slaves = append(slaves, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: ip})
	}
	// Log WHO, not just how many: the 2026-08-18 third-speaker bundle could
	// not say which box was being added, because every log line carried only
	// slaves=2.
	slaveIDs := make([]string, 0, len(slaves))
	for _, m := range slaves {
		slaveIDs = append(slaveIDs, m.DeviceID+"@"+m.IP)
	}
	s.logger.Info("zone: forming (beta)", "mode", mode, "master", master.DeviceID, "masterIP", master.IP,
		"slaves", len(slaves), "slaveList", strings.Join(slaveIDs, ","), "stereo", req.Stereo, "name", req.Name,
		"from", req.from, "ua", req.ua)
	return &zoneFormRun{req: req, mode: mode, master: master, slaves: slaves}, nil
}

// zoneFormResolveMembers replaces the deviceIDs the caller supplied with the
// ones the speakers' own firmware reports, the master's from this box and
// each member's from its /info.
func (s *Server) zoneFormResolveMembers(run *zoneFormRun) {
	ctx, c, slaves := run.ctx, run.c, run.slaves
	// The master is always THIS box, so resolve its deviceID from the local
	// firmware /info rather than trusting the app-supplied value (#70). The
	// desktop derives a member's deviceID from discovery, where a two-chip
	// chassis (ST20 spotty/BCO, Portable) announces its wlan0 (SMSC) MAC over
	// mDNS, which is NOT the SoundTouch deviceID the firmware keys /setZone and
	// /addGroup on (that is the SCM MAC in /info). For the master that mismatch
	// is fatal: the firmware never recognizes itself as master, so the zone reads
	// back empty (the "0.8.x regression" deqw and Albrecht hit was really this).
	run.master.DeviceID = s.localDeviceID(ctx, c, run.master.DeviceID)

	// The same correction for the SLAVES, for the same reason and from the same
	// evidence. A speaker has two MACs and only one of them is the SoundTouch
	// deviceID the firmware keys /setZone on; mDNS announces the other. The
	// master's side of this was fixed long ago and called fatal, but a slave
	// named by the wrong id is quietly just as broken: the master registers a
	// member, the follower never recognises itself, and the zone reads back
	// with the member "missing" while looking fine on the master.
	//
	// Measured 2026-08-09 on a SoundTouch 10, which reports deviceID
	// 0000000000A1 while announcing 0000000000B2 over mDNS: a group formed from
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
}

// zoneFormDefineOnly stores a permanent group as a template and ends the
// form there, or hands over (nil) when the request is a live form.
func (s *Server) zoneFormDefineOnly(run *zoneFormRun) *zoneFormAnswer {
	req, mode, master, slaves := run.req, run.mode, run.master, run.slaves
	// Defining a permanent group persists the TEMPLATE only and stops here: no
	// wake, no /setZone, no stream re-push. Forming the live zone below has to
	// wake the master, and a woken master resumes its last source, so creating a
	// permanent group used to start playback in every room the moment it was made
	// (Jens, 2026-08-31: "beim Erstellen einer Gruppe faengt die Gruppe an zu
	// spielen"). The permanent group forms itself on the next play
	// (formDefaultGroupOnPlay), so defining it must be silent. Member deviceIDs
	// were just resolved above, so the stored template is correct for that
	// play-time formation. Native only; the app never sends DefineOnly with Stereo.
	if !req.DefineOnly || req.Stereo {
		return nil
	}
	z := zones.Zone{Master: master.DeviceID, MasterIP: master.IP, Mode: mode, Name: req.Name, Permanent: true}
	for _, m := range slaves {
		z.Slaves = append(z.Slaves, zones.Member{DeviceID: m.DeviceID, IP: m.IP})
	}
	if s.zones != nil {
		if err := s.zones.Set(z); err != nil {
			s.logger.Warn("zone: permanent template persist failed", "err", err)
			return zoneFormRefusal(http.StatusInternalServerError, err.Error())
		}
		s.forgetZoneDocDoubt()
	}
	s.logger.Info("zone: permanent group defined (template saved, not formed live)",
		"master", master.DeviceID, "slaves", len(slaves))
	return zoneFormOK(map[string]any{"ok": true, "mode": "native", "permanent": true, "defined": true})
}

// zoneFormDrive forms the live multiroom zone: the coalescing of rapid
// forms, the leader probe, the before-picture and the record, then the
// mirror push or the native drive. It always ends with an answer.
func (s *Server) zoneFormDrive(run *zoneFormRun) *zoneFormAnswer {
	if ans := s.zoneFormSettle(run); ans != nil {
		return ans
	}
	s.zoneFormSerial.Lock()
	defer s.zoneFormSerial.Unlock()
	if ans := s.zoneFormSuperseded(run); ans != nil {
		return ans
	}
	if ans := s.zoneFormProbeLeader(run); ans != nil {
		return ans
	}
	s.zoneFormBeforePicture(run)
	s.zoneFormPersist(run)
	if ans := s.zoneFormDeferPermanent(run); ans != nil {
		return ans
	}
	if ans := s.zoneFormMirror(run); ans != nil {
		return ans
	}
	s.zoneFormPrepareResume(run)
	if ans := s.zoneFormWakeMaster(run); ans != nil {
		return ans
	}
	if ans := s.zoneFormDriveNative(run); ans != nil {
		return ans
	}
	return s.zoneFormVerify(run)
}

// zoneFormSettle stamps this form with its sequence number and waits out
// the settle before it takes its turn; a caller that gave up meanwhile gets
// 408.
func (s *Server) zoneFormSettle(run *zoneFormRun) *zoneFormAnswer {
	ctx := run.ctx
	// Coalesce rapid successive form requests (adding speakers one tap after
	// another): every caller sends the FULL member list it wants, so the
	// newest request carries the newest intent and older ones can stand down.
	// Each arrival takes a sequence number; after a short settle it waits its
	// turn on the serial lock, and a request that is no longer the newest
	// answers with the live zone instead of driving a stale list. Without
	// this, N quick taps ran N full drives back to back (live 2026-08-21,
	// three drives in 20 s, each one restarting the master's stream).
	run.mySeq = s.zoneFormSeq.Add(1)
	select {
	case <-time.After(zoneCoalesceSettle):
	case <-ctx.Done():
		return zoneFormRefusal(http.StatusRequestTimeout, "canceled")
	}
	return nil
}

// zoneFormSuperseded stands down, with the live zone as the answer, when a
// newer member list arrived during the settle. Called with zoneFormSerial
// held; the read below runs under it, as it always did.
func (s *Server) zoneFormSuperseded(run *zoneFormRun) *zoneFormAnswer {
	ctx, c := run.ctx, run.c
	if latest := s.zoneFormSeq.Load(); latest != run.mySeq {
		liveNow, lerr := c.GetZone(ctx)
		s.logger.Info("zone: form request superseded by a newer member list, standing down", "seq", run.mySeq, "latest", latest)
		if lerr != nil {
			return zoneFormOK(map[string]any{"ok": true, "mode": "native", "superseded": true})
		}
		return zoneFormOK(map[string]any{
			"ok": true, "mode": "native", "superseded": true,
			"master": liveNow.Master, "senderIP": liveNow.SenderIP, "members": liveNow.Members,
		})
	}
	return nil
}

// zoneFormProbeLeader asks the leader one cheap question before anything
// is touched and refuses when it does not answer.
func (s *Server) zoneFormProbeLeader(run *zoneFormRun) *zoneFormAnswer {
	ctx, c, mode, master := run.ctx, run.c, run.mode, run.master
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
				"probeErr", perr, "master", master.DeviceID)
			return zoneFormRefusal(http.StatusBadGateway, "the speaker leading the group is not answering: "+perr.Error())
		}
	}
	return nil
}

// zoneFormBeforePicture reads what the user already had, before anything is
// changed: the stored document, the live zone, and from both who is
// actually joining.
func (s *Server) zoneFormBeforePicture(run *zoneFormRun) {
	ctx, c, master, slaves := run.ctx, run.c, run.master, run.slaves
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

	// Who is actually JOINING. Computed HERE, from the only before-picture that
	// exists prior to any change: the mirror branch below returns before toAdd is
	// ever calculated, and after the drive the live zone already contains the new
	// members. Only these get the group's volume - handing it to members that
	// were already in the group would flatten the per-speaker balance the
	// relative group slider exists to preserve (#401).
	newlyJoined, joinSetTrusted := newlyJoinedMembers(slaves, prevLive, prevLiveErr, prevDoc, hadPrevDoc)
	if !joinSetTrusted {
		s.logger.Info("zone: no reliable before-picture of this group, leaving every member's volume alone")
		newlyJoined = nil
	}
	run.prevDoc, run.hadPrevDoc, run.prevLive, run.prevLiveErr = prevDoc, hadPrevDoc, prevLive, prevLiveErr
	run.newlyJoined = newlyJoined
}

// zoneFormPersist stores the group record first, so a transient drive error
// still leaves the group on record for the reconcile loop to retry.
func (s *Server) zoneFormPersist(run *zoneFormRun) {
	req, mode, master, slaves := run.req, run.mode, run.master, run.slaves
	// Persist first so a transient drive error still leaves the group on record
	// for the reconcile loop to retry. Only the master persists.
	z := zones.Zone{Master: master.DeviceID, MasterIP: master.IP, Mode: mode, Name: req.Name, Permanent: req.Permanent}
	for _, m := range slaves {
		z.Slaves = append(z.Slaves, zones.Member{DeviceID: m.DeviceID, IP: m.IP})
	}
	if s.zones != nil {
		// A group that is not permanent no longer replaces a permanent one: the
		// store sets the permanent group aside and brings it back when this one
		// ends (zones.Store.Set / Clear). Two reports of a lost permanent group
		// (2026-09-30, 2026-11-02) were both a group key or speakers added by
		// hand on the permanent group's main speaker. Only a NEW permanent group
		// still replaces the old one, because that is the user changing it.
		if prev, ok := s.zones.Get(); ok && prev.Permanent && sameMasterZone(prev, z) && !sameMembers(prev, z) {
			if z.Permanent {
				s.logger.Warn("zone: REPLACING the standing permanent group of this speaker with a different permanent one",
					"master", z.Master,
					"previousMembers", len(prev.Slaves), "previousName", prev.Name,
					"newMembers", len(z.Slaves), "newName", z.Name)
			} else {
				s.logger.Info("zone: a group formed on top of the permanent group, which stays saved and comes back when this one ends",
					"master", z.Master,
					"permanentMembers", len(prev.Slaves), "permanentName", prev.Name,
					"newMembers", len(z.Slaves), "newName", z.Name)
			}
		}
		if err := s.zones.Set(z); err != nil {
			s.logger.Warn("zone: persist failed", "err", err)
		}
		// A freshly formed group starts from a clean slate, so a doubt recorded
		// against an earlier group can never be spent on this one (boxInZone).
		s.forgetZoneDocDoubt()
	}
	run.z = z
}

// zoneFormDeferPermanent leaves a permanent group whose master is idle to
// the next play instead of waking it, and says so.
func (s *Server) zoneFormDeferPermanent(run *zoneFormRun) *zoneFormAnswer {
	req, mode, master, slaves := run.req, run.mode, run.master, run.slaves
	// A PERMANENT group whose master is idle must not be woken and formed now.
	// Waking a standby speaker (sys power) resumes its last station, so creating
	// a permanent group started music on the speakers (reported live 2026-09-03).
	// The document is persisted above; formDefaultGroupOnPlay wires the firmware
	// zone, waking the members, the next time the master actually plays. A master
	// that is already playing falls through and forms live so the group hears the
	// current stream at once.
	if req.Permanent {
		if standby, busy := s.boxPlayState(); standby || !busy {
			s.logger.Info("zone: permanent group stored, forming deferred to the next play (master idle, not waking it)",
				"master", master.DeviceID, "slaves", len(slaves), "mode", mode)
			return zoneFormOK(map[string]any{"ok": true, "mode": mode, "deferred": true})
		}
	}
	return nil
}

// zoneFormMirror is the mirror mode's whole drive: each member's box is
// pointed at the master's stream over UPnP. Hands over (nil) in native mode.
func (s *Server) zoneFormMirror(run *zoneFormRun) *zoneFormAnswer {
	ctx, mode, z, newlyJoined, mySeq := run.ctx, run.mode, run.z, run.newlyJoined, run.mySeq
	if mode != "mirror" {
		return nil
	}
	// Deliberate user action: push unconditionally (reconcile=false), the
	// user just asked for exactly this group.
	s.mirrorToSlaves(ctx, z, false)
	// Detached: the volume match settles for a couple of seconds and the form
	// budget is already capped at 38s against an app that gives up at 45s.
	// It also must not race mirrorToSlaves' own PlayURL push, which is the
	// play-start a raw volume write is documented to kill - the applier's
	// settle covers that.
	if len(newlyJoined) > 0 {
		go s.matchNewMembersToMasterVolume(newlyJoined, mySeq)
	}
	return zoneFormOK(map[string]any{"ok": true, "mode": "mirror"})
}

// zoneFormBudget is how long the whole form is allowed to take, as a function
// of how many speakers are joining.
//
// It used to be a flat ten seconds, and that one budget covers everything the
// form does: waking the master, reading the live zone, removing members the
// user dropped, the /setZone drive itself, and the read that confirms it. The
// firmware gets slower as the group grows, so the fixed budget turned into a
// ceiling on group size rather than a safety net.
//
// A twelve-speaker household measured it exactly on 2026-08-08, adding one
// speaker at a time and waiting between each:
//
//	1 to 5 slaves   formed in 4 to 8 seconds
//	6 slaves        formed, but took 22 seconds
//	7 slaves        failed every time, always "setZone: context deadline
//	                exceeded", five attempts in a row
//
// Nothing was wrong with the eighth speaker: the same fleet had formed a group
// of twelve earlier that afternoon, when the box happened to answer quickly.
// The owner's conclusion was that STR cannot do more than six, which is exactly
// what a fixed budget looks like from outside.
//
// So the budget grows with the group. The ceiling stays below the desktop
// app's own 45 s call timeout, because an agent that answers after the app has
// given up is worse than one that fails: the app would report failure for a
// group the firmware went on to build.
// zoneCoalesceSettle is how long a form request waits before checking whether
// a newer one arrived: rapid successive taps (adding speakers one after
// another) then merge into the newest request's full list instead of running
// one drive per tap.
const zoneCoalesceSettle = 700 * time.Millisecond

// zoneDiff splits the requested member list against the live firmware zone:
// toAdd are requested members not yet in the zone, toRemove are live members
// no longer requested. Matching is IP-or-deviceID; IP is the chassis-stable
// key (a two-chip box announces its wlan0 MAC over discovery, which is not
// the SCM deviceID the firmware lists). With no live zone every requested
// member is toAdd and nothing is toRemove.
func zoneDiff(live boxapi.Zone, want []boxapi.ZoneMember) (toAdd, toRemove []boxapi.ZoneMember) {
	wantIP := make(map[string]bool, len(want))
	wantDev := make(map[string]bool, len(want))
	for _, m := range want {
		if m.IP != "" {
			wantIP[m.IP] = true
		}
		if m.DeviceID != "" {
			wantDev[strings.ToLower(m.DeviceID)] = true
		}
	}
	liveIP := make(map[string]bool, len(live.Members))
	liveDev := make(map[string]bool, len(live.Members))
	for _, m := range live.Members {
		if m.IP != "" {
			liveIP[m.IP] = true
		}
		if m.DeviceID != "" {
			liveDev[strings.ToLower(m.DeviceID)] = true
		}
		if (m.IP == "" || !wantIP[m.IP]) && (m.DeviceID == "" || !wantDev[strings.ToLower(m.DeviceID)]) {
			toRemove = append(toRemove, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
		}
	}
	for _, m := range want {
		if (m.IP == "" || !liveIP[m.IP]) && (m.DeviceID == "" || !liveDev[strings.ToLower(m.DeviceID)]) {
			toAdd = append(toAdd, m)
		}
	}
	return toAdd, toRemove
}

func zoneFormBudget(slaves int) time.Duration {
	const (
		base    = 10 * time.Second
		perSlve = 4 * time.Second
		ceiling = 38 * time.Second // the app gives up at 45 s
	)
	d := base + time.Duration(slaves)*perSlve
	if d > ceiling {
		return ceiling
	}
	return d
}
