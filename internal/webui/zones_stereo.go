// Multiroom zones and stereo-pair handling.

package webui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/upnp"
	"github.com/JRpersonal/streborn/internal/zones"
)

// handleBoxZone serves the SoundTouch multiroom zone (#70, BETA):
//
//	GET    -> the live zone the box reports {"master","senderIP","members"[]}
//	POST   -> form/replace a zone with THIS box as master (body: master + slaves)
//	DELETE -> dissolve the zone this box leads
//
// POST/DELETE also persist to the zones store so the zone auto-reforms after a
// reboot/standby/Wi-Fi outage without the user re-grouping. This is the blind
// beta path: it drives the native Bose /setZone family directly and logs every
// step (master, slaves, the firmware's read-back) into agent.log so multi-speaker
// testers' diagnostic bundles show exactly what the firmware did.
func (s *Server) handleBoxZone(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleZoneGet(w, r)
	case http.MethodPost:
		s.handleZoneForm(w, r)
	case http.MethodDelete:
		s.handleZoneDissolve(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleZoneGet(w http.ResponseWriter, r *http.Request) {
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	z, err := c.GetZone(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// A stereo pair is a firmware GROUP, not a zone, so /getZone says nothing
	// about it: a paired speaker reports {"members":[]} exactly like a
	// standalone one. That left the desktop app unable to see which speakers
	// were paired, so its pair controls just offered the first two candidates
	// and its "undo pair" went to whichever speaker the multiroom master
	// selection happened to point at. A user with three SoundTouch 10s pressed
	// undo twice, both times against a speaker that was not in the pair, and
	// the pair stayed up while the app reported success (field, 2026-08-04).
	//
	// Reported alongside the zone so one poll answers both. Best-effort: a box
	// that does not answer /getGroup simply reports no pair, which is what
	// every caller assumed until now anyway.
	// Embedded, so the zone fields keep their exact previous JSON shape
	// (omitempty and all) and only gain a sibling.
	// A FOLLOWER's own /getZone lists only itself, so a caller asking one
	// speaker saw a group of one while the group had five members. The
	// desktop app reads this endpoint, which is why it showed some speakers
	// and not others depending on which one it happened to ask (live
	// 2026-08-15). The leader has the full list, and a follower carries its
	// address, so it is fetched and reported here. The speaker's OWN master
	// and sender fields are kept, so nothing that relies on them changes.
	if full, ok := s.leaderZone(ctx, z); ok {
		z.Members = full
	}
	out := struct {
		boxapi.Zone
		Stereo *boxapi.Group `json:"stereo,omitempty"`
		// Remembered is the persisted zone membership (zones.json) when NO zone
		// is live: the follower IPs and names, so a client can offer "play
		// together again" with one tap. The desktop always showed the
		// remembered group; the phone page could not, and a three-speaker
		// household read that as the group being gone (mail report,
		// 2026-08-25). Only the desktop's own store, no probes.
		Remembered []rememberedMember `json:"remembered,omitempty"`
	}{Zone: z}
	if len(z.Members) == 0 {
		out.Remembered = s.rememberedZoneMembers()
	}
	// The pair read gets its own SHORT budget, never the zone's. The firmware's
	// /getGroup HANGS on scm/BCO chassis — no refusal, just silence (12 s and
	// counting on an awake scm ST30, 2026-08-18, while its /getZone answered in
	// 28 ms). Chained on the zone ctx it ate the whole 6 s budget, the desktop
	// app's 6 s client always gave up first, and so no non-ST10 speaker EVER
	// delivered a live zone to the app: both group screens then ran on
	// optimistic data alone (Bernard's two-screens-disagree family). After a
	// few consecutive hangs the read is paused for a while — the answer cannot
	// change while the firmware refuses to give one.
	if s.groupReadAllowed() {
		gctx, gcancel := context.WithTimeout(r.Context(), 1500*time.Millisecond)
		g, gerr := c.GetGroup(gctx)
		gcancel()
		s.noteGroupReadResult(gerr)
		if gerr == nil && (g.ID != "" || len(g.Members) > 0) {
			out.Stereo = &g
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// rememberedMember is one follower of the persisted (not currently live) zone.
type rememberedMember struct {
	IP       string `json:"ip"`
	DeviceID string `json:"deviceID,omitempty"`
	Name     string `json:"name,omitempty"`
}

// rememberedZoneMembers reads the persisted zone from the store and returns
// its follower IPs, named from the peer roster where a name is known. Empty
// when this box is not the remembered master, or remembers no zone, or the
// remembered pair is a stereo pair (re-forming that is the pair flow's job).
func (s *Server) rememberedZoneMembers() []rememberedMember {
	if s.zones == nil {
		return nil
	}
	z, ok := s.zones.Get()
	if !ok || z.Stereo || len(z.Slaves) == 0 {
		return nil
	}
	names := map[string]string{}
	if s.peersFn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		for _, p := range s.peersFn(ctx) {
			names[p.IP] = p.Name
		}
		cancel()
	}
	out := make([]rememberedMember, 0, len(z.Slaves))
	for _, m := range z.Slaves {
		if m.IP == "" {
			continue
		}
		out = append(out, rememberedMember{IP: m.IP, DeviceID: m.DeviceID, Name: names[m.IP]})
	}
	return out
}

// groupReadAllowed reports whether the stereo /getGroup read should be tried,
// i.e. it is not currently paused after consecutive firmware hangs.
func (s *Server) groupReadAllowed() bool {
	s.groupReadMu.Lock()
	defer s.groupReadMu.Unlock()
	return time.Now().After(s.groupReadSkipUntil)
}

// noteGroupReadResult tracks consecutive /getGroup timeouts and pauses the
// read once the firmware has proven it will not answer. The pause is short
// (5 min): a deep-standby speaker hangs this endpoint too and must get its
// pair read back soon after waking.
func (s *Server) noteGroupReadResult(err error) {
	s.groupReadMu.Lock()
	defer s.groupReadMu.Unlock()
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		s.groupReadFails = 0
		return
	}
	s.groupReadFails++
	if s.groupReadFails < 3 {
		return
	}
	s.groupReadSkipUntil = time.Now().Add(5 * time.Minute)
	s.groupReadFails = 0
	s.logger.Info("zone: the firmware's /getGroup keeps hanging (known on scm/BCO chassis), pausing the stereo-pair read",
		"pauseMin", 5)
}

// handleBoxBalance reports the left/right balance of a stereo pair.
//
// GET /api/box/balance -> {"available":bool,"min":-7,"max":7,"actual":0,...}
//
// Deliberately its OWN endpoint rather than a field on the zone read, and
// deliberately on a short budget. The firmware's /balance does not answer at
// all while the speaker is in deep standby: it does not refuse, it hangs (12 s
// and counting, measured 2026-08-04). The zone read is polled by the app every
// few seconds, so folding balance into it would have put a multi-second stall
// into a hot path for every speaker that happens to be asleep.
//
// Read-only for now. The firmware accepts no write over this API that we could
// make work: every POST /balance hung the same way, including the exact body
// the community reference sends, and left the endpoint unresponsive until the
// speaker was woken again. So STR reports what the balance IS, which is enough
// to explain a pair that sounds lopsided because it was set in the Bose app,
// and does not pretend to offer a control that would not work.
func (s *Server) handleBoxBalance(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	b, err := boxapi.New(s.boxHost).GetBalance(ctx)
	if err != nil {
		// A speaker asleep or otherwise not answering is not an error worth
		// showing: report "no balance to display" and let the caller move on.
		s.logger.Debug("balance: not readable", "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "reason": "unreachable"})
		return
	}
	writeJSON(w, http.StatusOK, b)
}

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
}

// handleZoneForm creates (or replaces) a group with this box as master (#70 beta).
// Two user-switchable modes: "native" drives the Bose /setZone family so the
// firmware syncs the slaves (tightest, when the firmware accepts STR's source);
// "mirror" points each slave's box at the master's current stream over UPnP
// (looser sync, works more widely). Either way the group is persisted so it
// auto-reforms after a reboot/standby. The caller supplies the master's and
// slaves' deviceID+IP from discovery, so the agent need not self-identify.
func (s *Server) handleZoneForm(w http.ResponseWriter, r *http.Request) {
	var req zoneFormReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	// The master's deviceID is optional. It is resolved from this box's own
	// firmware /info a few lines below and the supplied value is overwritten
	// anyway, so requiring it only kept out the one client that cannot know it:
	// the phone remote is served BY the master and has no reason to be told
	// which speaker it is running on.
	if len(req.Slaves) == 0 {
		http.Error(w, "at least one slave is required", http.StatusBadRequest)
		return
	}
	mode := req.Mode
	if mode != "mirror" {
		mode = "native"
	}
	master := boxapi.ZoneMember{DeviceID: req.Master.DeviceID, IP: req.Master.IP}
	slaves := make([]boxapi.ZoneMember, 0, len(req.Slaves))
	for _, m := range req.Slaves {
		slaves = append(slaves, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
	}
	// Log WHO, not just how many: the 2026-08-18 third-speaker bundle could
	// not say which box was being added, because every log line carried only
	// slaves=2.
	slaveIDs := make([]string, 0, len(slaves))
	for _, m := range slaves {
		slaveIDs = append(slaveIDs, m.DeviceID+"@"+m.IP)
	}
	s.logger.Info("zone: forming (beta)", "mode", mode, "master", master.DeviceID, "masterIP", master.IP,
		"slaves", len(slaves), "slaveList", strings.Join(slaveIDs, ","), "stereo", req.Stereo, "name", req.Name)

	ctx, cancel := context.WithTimeout(r.Context(), zoneFormBudget(len(slaves)))
	defer cancel()
	c := boxapi.New(s.boxHost)

	// The master is always THIS box, so resolve its deviceID from the local
	// firmware /info rather than trusting the app-supplied value (#70). The
	// desktop derives a member's deviceID from discovery, where a two-chip
	// chassis (ST20 spotty/BCO, Portable) announces its wlan0 (SMSC) MAC over
	// mDNS, which is NOT the SoundTouch deviceID the firmware keys /setZone and
	// /addGroup on (that is the SCM MAC in /info). For the master that mismatch
	// is fatal: the firmware never recognizes itself as master, so the zone reads
	// back empty (the "0.8.x regression" deqw and Albrecht hit was really this).
	master.DeviceID = s.localDeviceID(ctx, c, master.DeviceID)

	// The same correction for the SLAVES, for the same reason and from the same
	// evidence. A speaker has two MACs and only one of them is the SoundTouch
	// deviceID the firmware keys /setZone on; mDNS announces the other. The
	// master's side of this was fixed long ago and called fatal, but a slave
	// named by the wrong id is quietly just as broken: the master registers a
	// member, the follower never recognises itself, and the zone reads back
	// with the member "missing" while looking fine on the master.
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

	// A stereo pair is a firmware-native L/R group (POST /addGroup), not a
	// multiroom zone. It needs exactly one partner; the master is the LEFT
	// channel and the partner the RIGHT by Bose convention. Only the ST10
	// actually pairs, but every model lists /addGroup, so we let the firmware
	// be the authority and surface its real response to the app.
	if req.Stereo {
		s.formStereoPair(w, ctx, c, master, slaves, req.Name)
		return
	}

	// Coalesce rapid successive form requests (adding speakers one tap after
	// another): every caller sends the FULL member list it wants, so the
	// newest request carries the newest intent and older ones can stand down.
	// Each arrival takes a sequence number; after a short settle it waits its
	// turn on the serial lock, and a request that is no longer the newest
	// answers with the live zone instead of driving a stale list. Without
	// this, N quick taps ran N full drives back to back (live 2026-08-21,
	// three drives in 20 s, each one restarting the master's stream).
	mySeq := s.zoneFormSeq.Add(1)
	select {
	case <-time.After(zoneCoalesceSettle):
	case <-ctx.Done():
		http.Error(w, "canceled", http.StatusRequestTimeout)
		return
	}
	s.zoneFormSerial.Lock()
	defer s.zoneFormSerial.Unlock()
	if latest := s.zoneFormSeq.Load(); latest != mySeq {
		liveNow, lerr := c.GetZone(ctx)
		s.logger.Info("zone: form request superseded by a newer member list, standing down", "seq", mySeq, "latest", latest)
		if lerr != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "native", "superseded": true})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "mode": "native", "superseded": true,
			"master": liveNow.Master, "senderIP": liveNow.SenderIP, "members": liveNow.Members,
		})
		return
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
				"probeErr", perr, "master", master.DeviceID)
			http.Error(w, "the speaker leading the group is not answering: "+perr.Error(), http.StatusBadGateway)
			return
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

	// Persist first so a transient drive error still leaves the group on record
	// for the reconcile loop to retry. Only the master persists.
	z := zones.Zone{Master: master.DeviceID, MasterIP: master.IP, Mode: mode, Name: req.Name, Permanent: req.Permanent}
	for _, m := range slaves {
		z.Slaves = append(z.Slaves, zones.Member{DeviceID: m.DeviceID, IP: m.IP})
	}
	if s.zones != nil {
		if err := s.zones.Set(z); err != nil {
			s.logger.Warn("zone: persist failed", "err", err)
		}
		// A freshly formed group starts from a clean slate, so a doubt recorded
		// against an earlier group can never be spent on this one (boxInZone).
		s.forgetZoneDocDoubt()
	}

	if mode == "mirror" {
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
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "mirror"})
		return
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
	if _, busy := s.boxPlayState(); busy {
		s.lastPlayMu.Lock()
		if s.lastPlay != nil {
			cp := *s.lastPlay
			resume = &cp
		}
		s.lastPlayMu.Unlock()
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
	if err := s.ensureBoxReadyErr(ctx); err != nil {
		perr := s.speakerStaysSilent(ctx, c)
		if perr != nil {
			s.logger.Warn("zone: the speaker leading the group is not answering at all, not sending setZone",
				"wakeErr", err, "probeErr", perr, "master", master.DeviceID, "prevMembers", len(prevLive.Members))
			s.restorePreviousZone(ctx, c, master, prevDoc, hadPrevDoc, prevLive)
			http.Error(w, "the speaker leading the group is not answering: "+perr.Error(), http.StatusBadGateway)
			return
		}
		s.logger.Info("zone: the speaker did not report waking, but it is answering, so the group is formed anyway",
			"wakeErr", err, "master", master.DeviceID)
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
			http.Error(w, "setZone: "+err.Error(), http.StatusBadGateway)
			return
		}
	}
	z2, err := c.GetZone(ctx)
	if err != nil {
		s.logger.Warn("zone: formed but getZone read-back failed", "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "native"})
		return
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
			s.restorePreviousZone(ctx, c, master, prevDoc, hadPrevDoc, prevLive)
			http.Error(w, "setZone: "+err.Error(), http.StatusBadGateway)
			return
		}
		if z2, err = c.GetZone(ctx); err != nil {
			s.logger.Warn("zone: formed but getZone read-back failed", "err", err)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "native"})
			return
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
		"unverifiable", strings.Join(unverifiable, ","))
	// Verify-first. A member that never confirmed the join (missing) must not be
	// given the group's level: that is a speaker in another room, in no group at
	// all, jumping to the living room's volume. Same for a zone the firmware
	// never actually formed.
	if masterFormed && verified > 0 {
		if joining := dropMembers(newlyJoined, missing); len(joining) > 0 {
			go s.matchNewMembersToMasterVolume(joining, mySeq)
		}
	}
	if resume != nil && masterFormed {
		// resume is the copy of lastPlay taken before the drive, so it is both
		// the stream to push and the staleness reference. Only a change that
		// held as an incremental join may skip the push when the master's
		// stream survived; on a fresh (or re-formed) zone the members have
		// nothing yet (Martin, 2026-08-24).
		go s.resumeAfterZoneForm(zoneResume{push: *resume, ref: resume, survivorReachesMembers: heldIncremental})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": ok, "mode": "native", "master": z2.Master, "senderIP": z2.SenderIP,
		"members": z2.Members, "requested": len(slaves),
		"verified": verified, "missing": missing, "unverifiable": unverifiable,
		"masterMissing": masterMissing,
	})
}

// zoneResume is what resumeAfterZoneForm needs to restore playback after a
// group change: the stream to push, the staleness reference, and whether a
// stream that survived the change already serves every member.
type zoneResume struct {
	// push is the stream to (re)start on this box after the change. Usually
	// this box's own captured lastPlay; for a stereo pair it can also be the
	// PARTNER's stream (issue #705, the partner was the one playing).
	push lastPlayInfo
	// ref is this box's lastPlay entry as it stood when the capture was taken,
	// nil when there was none. The resume stands down when the live lastPlay no
	// longer matches ref, because that means a user play landed in between and
	// pushing the capture would clobber it (#252). Kept separate from push so a
	// partner-derived capture, which never was this box's lastPlay, still gets
	// exactly that guard instead of always looking superseded.
	ref *lastPlayInfo
	// survivorReachesMembers says a stream that survived the group change is
	// already reaching every member, so a re-push would only be an audible gap:
	// true for an incremental join over a live zone (the firmware keeps the
	// master's source running and the existing members keep hearing it,
	// v0.9.56) and for a firmware stereo pair (the pair is one logical device;
	// in the #705 bundle the partner flipped to GROUP_SLAVE the moment the
	// pair formed). False for a fresh full /setZone form: there the members
	// have nothing yet, and skipping the push because the MASTER kept playing
	// left them silent (Martin, 2026-08-24: regroup mid-stream, members mute
	// until a manual play).
	survivorReachesMembers bool
}

// resumeRefSuperseded reports whether this box's live lastPlay entry no longer
// matches the one captured when the group change began, i.e. a user play landed
// in between. Unlike resumeIsStale it treats "no entry then, no entry now" as
// NOT superseded: a partner-derived stereo resume (#705) must be able to fire
// on a master that never played anything itself.
func resumeRefSuperseded(ref, cur *lastPlayInfo) bool {
	if ref == nil {
		return cur != nil
	}
	if cur == nil {
		return true
	}
	return cur.boxURL != ref.boxURL || !cur.ts.Equal(ref.ts)
}

// resumeAfterZoneForm (re)starts the stream that was playing before a group
// change tore it down or left members without it (see handleZoneForm and
// formStereoPair). The firmware needs a settle moment after /setZone before it
// accepts a new SetURI, pushing too early just re-triggers the 1036 wrong-state
// error, so wait, then push under the box command lock, standing down when the
// user stopped meanwhile or a newer play superseded the captured one.
func (s *Server) resumeAfterZoneForm(rz zoneResume) {
	if s.renderer == nil {
		return
	}
	lp := rz.push
	time.Sleep(1500 * time.Millisecond)
	if s.userStoppedRecently() {
		s.logger.Info("zone: not restarting playback after forming, user stopped meanwhile")
		return
	}
	// The re-push exists for the members that have nothing yet. When the change
	// was an incremental join (or a firmware stereo pair), a master still
	// playing after the settle kept its stream through the change and every
	// member already hears it, so the push would only interrupt it. On a FRESH
	// full form that logic was wrong: the master kept playing but the freshly
	// joined members had no stream at all, and this very skip left them silent
	// (Martin, 2026-08-24). So the survived-stream skip only applies when the
	// caller knows the surviving stream reaches the members. An unreadable or
	// idle box falls through to the push, the historical safe behavior.
	if rz.survivorReachesMembers {
		if standby, busy := s.boxPlayState(); busy && !standby {
			s.logger.Info("zone: stream survived the group change, not restarting playback")
			return
		}
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	s.lastPlayMu.Lock()
	cur := s.lastPlay
	s.lastPlayMu.Unlock()
	if resumeRefSuperseded(rz.ref, cur) {
		s.logger.Info("zone: not restarting playback after forming, a newer play superseded it",
			"captured", lp.boxURL, "current", lastPlayURL(cur))
		return
	}
	push := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if lp.mime != "" {
			return s.renderer.PlayURLMime(ctx, lp.boxURL, lp.title, lp.art, lp.mime)
		}
		return s.renderer.PlayURL(ctx, lp.boxURL, lp.title, lp.art)
	}
	err := push()
	if err != nil {
		// One retry after a longer settle: right after /setZone the firmware
		// sporadically rejects the first SetURI while the zone is still wiring
		// its followers.
		time.Sleep(3 * time.Second)
		err = push()
	}
	if err != nil {
		s.logger.Warn("zone: could not restart the master's stream after forming; the group is formed but silent - press play or a preset to start it",
			"err", err, "url", lp.boxURL)
		return
	}
	s.logger.Info("zone: master's stream restarted after group forming", "url", lp.boxURL, "title", lp.title)
}

// handleSpotifyCredential moves the go-librespot Spotify login between speakers
// (#45): GET returns this box's active credential blob, POST installs a blob
// exported from another box and restarts go-librespot to log in with it. LAN-only,
// same trust model as the rest of the agent API; the blob is a reusable Spotify
// Connect credential, so the desktop app should only move it between the user's
// own speakers.
func (s *Server) handleSpotifyCredential(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if s.spotifyExportCred == nil {
			http.Error(w, "spotify not configured", http.StatusServiceUnavailable)
			return
		}
		data, err := s.spotifyExportCred()
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "no spotify login stored on this speaker", "detail": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	case http.MethodPost:
		if s.spotifyImportCred == nil {
			http.Error(w, "spotify not configured", http.StatusServiceUnavailable)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256*1024))
		if err != nil || len(data) == 0 {
			http.Error(w, "empty or oversized credential", http.StatusBadRequest)
			return
		}
		if err := s.spotifyImportCred(r.Context(), data); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": "import failed", "detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// followerZoneFetch returns one follower's own zone self-report. Split out so a
// test can inject a fake without standing up a :8090 server (boxapi.New hardcodes
// the :8090 port via Client.url).
type followerZoneFetch func(ctx context.Context, ip string) (boxapi.Zone, error)

// verifyFollowersJoined polls each requested slave's OWN /getZone until it
// reports masterID as its zone master, or a per-follower deadline elapses (#70).
// Trusting only the master's optimistic member list reports a complete group
// while a follower is actually still standalone: the master lists a member the
// firmware announced before the slave actually enrolled, and the slave's own
// zone lags ~100ms to several seconds behind. A follower that never names
// masterID as its master within the budget is returned in "missing" so the app
// can flag it instead of claiming success. Followers with no known IP cannot be
// verified and are returned in "unverifiable" (left to the master's view).
func verifyFollowersJoined(ctx context.Context, logger *slog.Logger, masterID string, slaves []boxapi.ZoneMember, fetch followerZoneFetch) (missing, unverifiable []string) {
	return verifyFollowersJoinedTimed(ctx, logger, masterID, slaves, fetch, defaultFollowerVerifyTiming)
}

// followerVerifyTiming bounds verifyFollowersJoined's polling. Injected so the
// tests can shrink the budget from seconds to milliseconds; production always
// uses defaultFollowerVerifyTiming.
type followerVerifyTiming struct {
	perFollowerBudget time.Duration
	pollInterval      time.Duration
	perCallTimeout    time.Duration
}

var defaultFollowerVerifyTiming = followerVerifyTiming{
	perFollowerBudget: 4 * time.Second,
	pollInterval:      700 * time.Millisecond,
	perCallTimeout:    2 * time.Second,
}

// verifyFollowersJoinedTimed is verifyFollowersJoined with explicit timing;
// see there for the semantics.
func verifyFollowersJoinedTimed(ctx context.Context, logger *slog.Logger, masterID string, slaves []boxapi.ZoneMember, fetch followerZoneFetch, timing followerVerifyTiming) (missing, unverifiable []string) {
	// Every follower is polled AT THE SAME TIME. They are separate speakers on
	// separate addresses and nothing about asking one depends on having asked
	// another, so the old speaker-after-speaker walk bought nothing and cost
	// the whole budget.
	//
	// It cost correctness, not just time. This runs under the form's context,
	// which is bounded by zoneFormBudget, and each follower was given up to
	// four seconds of its own. Eleven followers therefore needed up to
	// forty-four seconds inside a budget that stops at thirty-eight, minus
	// whatever /setZone had already spent. The context died partway down the
	// list and every follower after that point was reported "missing" although
	// it had joined perfectly well.
	//
	// A twelve-speaker household saw exactly that on 2026-08-09: ok=true with
	// all members listed, but verified=3 on one attempt and verified=7 minutes
	// later, a different set each time, and 11 of 11 the day before when
	// /setZone happened to be quick and left more budget. Read as a grouping
	// failure that is baffling; read as a report running out of time it is
	// obvious. Polled together, the whole check costs about one follower's
	// budget no matter how large the fleet.
	type result struct {
		deviceID     string
		unverifiable bool
		joined       bool
	}
	results := make([]result, len(slaves))
	var wg sync.WaitGroup
	for i, sl := range slaves {
		results[i].deviceID = sl.DeviceID
		if sl.IP == "" {
			results[i].unverifiable = true
			continue
		}
		wg.Add(1)
		go func(i int, sl boxapi.ZoneMember) {
			defer wg.Done()
			results[i].joined = pollFollowerJoined(ctx, logger, masterID, sl, fetch, timing)
		}(i, sl)
	}
	wg.Wait()

	// Reported in the order the caller asked for them, so the output does not
	// shuffle between runs just because the speakers answered out of order.
	for _, r := range results {
		switch {
		case r.unverifiable:
			unverifiable = append(unverifiable, r.deviceID)
		case !r.joined:
			missing = append(missing, r.deviceID)
		}
	}
	return missing, unverifiable
}

// pollFollowerJoined polls one follower's own /getZone until it names masterID
// as its master, its own budget runs out, or the surrounding context ends.
func pollFollowerJoined(ctx context.Context, logger *slog.Logger, masterID string, sl boxapi.ZoneMember, fetch followerZoneFetch, timing followerVerifyTiming) bool {
	deadline := time.Now().Add(timing.perFollowerBudget)
	var lastSelfMaster string
	var lastMembers int
	var lastErr error
	for {
		cctx, cancel := context.WithTimeout(ctx, timing.perCallTimeout)
		fz, ferr := fetch(cctx, sl.IP)
		cancel()
		if ferr != nil {
			lastErr = ferr
		} else {
			lastErr = nil
			lastSelfMaster = fz.Master
			lastMembers = len(fz.Members)
			if fz.Master != "" && strings.EqualFold(fz.Master, masterID) {
				logger.Info("zone: follower confirmed", "follower", sl.DeviceID, "ip", sl.IP, "selfMaster", lastSelfMaster)
				return true
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(timing.pollInterval):
		}
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr != nil {
		logger.Info("zone: follower never confirmed (self-report unreachable)", "follower", sl.DeviceID, "ip", sl.IP, "err", lastErr.Error())
	} else {
		logger.Info("zone: follower never confirmed", "follower", sl.DeviceID, "ip", sl.IP, "selfMaster", lastSelfMaster, "selfMembers", lastMembers)
	}
	return false
}

// localDeviceID returns this box's authoritative Bose SoundTouch deviceID, read
// from the local firmware /info, falling back to supplied when /info is
// unreachable or carries no deviceID. The zone protocol (/setZone, /addGroup)
// keys on this exact ID. The desktop derives a member's deviceID from discovery,
// which on a two-chip chassis can be the wlan0 (SMSC) MAC instead of the
// SoundTouch (SCM) deviceID; since the master is always the box this agent runs
// on, the agent is the authority for its own ID and corrects the mismatch (#70).
func (s *Server) localDeviceID(ctx context.Context, c *boxapi.Client, supplied string) string {
	info, err := c.GetInfo(ctx)
	if err != nil {
		return supplied
	}
	real := strings.TrimSpace(info.DeviceID)
	if real == "" {
		return supplied
	}
	if supplied != "" && !strings.EqualFold(real, supplied) {
		s.logger.Info("zone: corrected master deviceID from firmware /info (app sent the chassis wlan0/SMSC MAC, not the SoundTouch ID)",
			"supplied", supplied, "firmware", real)
	}
	return real
}

// rememberMemberDeviceID records what a member speaker's own firmware answered
// for its deviceID, keyed by the IP it was asked on. Only ever called with a
// firmware answer, so the cache cannot be poisoned by a caller-supplied value.
func (s *Server) rememberMemberDeviceID(ip, deviceID string) {
	if ip == "" || deviceID == "" {
		return
	}
	s.memberIDsMu.Lock()
	defer s.memberIDsMu.Unlock()
	if s.memberIDs == nil {
		s.memberIDs = make(map[string]string)
	}
	s.memberIDs[ip] = deviceID
}

// cachedMemberDeviceID returns the last deviceID a speaker at this IP reported
// for itself, and whether there was one. Used when its /info does not answer in
// time during zone forming, so a busy member is still enrolled under the ID its
// firmware actually keys on.
func (s *Server) cachedMemberDeviceID(ip string) (string, bool) {
	s.memberIDsMu.RLock()
	defer s.memberIDsMu.RUnlock()
	v, ok := s.memberIDs[ip]
	return v, ok
}

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
	if err := s.zones.Clear(); err != nil {
		s.logger.Warn("stereo: could not remove the pair document after a failed pairing", "err", err, "why", why)
		return
	}
	s.logger.Info("stereo: pairing failed, the pair document was removed again", "why", why)
}

func (s *Server) formStereoPair(w http.ResponseWriter, ctx context.Context, c *boxapi.Client, master boxapi.ZoneMember, slaves []boxapi.ZoneMember, name string) {
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
	s.lastPlayMu.Lock()
	var masterRef *lastPlayInfo
	if s.lastPlay != nil {
		cp := *s.lastPlay
		masterRef = &cp
	}
	s.lastPlayMu.Unlock()
	var resume *lastPlayInfo
	if _, busy := s.boxPlayState(); busy {
		resume = masterRef
	}
	if resume == nil && partner.IP != "" {
		if pr := partnerResumeForPair(fetchNowPlaying(ctx, partner.IP), partner.IP); pr != nil {
			s.logger.Info("stereo: captured the partner's stream to restart on the pair (the master is not playing)",
				"partnerIP", partner.IP, "url", pr.boxURL, "title", pr.title)
			resume = pr
		}
	}

	// Never pair against a standby master, for the same reason the zone form
	// wakes it (handleZoneForm): in #705 the standby master dragged the whole
	// fresh pair into standby. As there, a failed wake alone is not a reason to
	// stop; only a speaker that answers nothing at all is.
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
	// Restart what was playing before the pairing (#705). A pair is one logical
	// device to the firmware, so a master stream that SURVIVED the pairing
	// already serves both channels (in the #705 bundle the partner flipped to
	// GROUP_SLAVE while the master kept its stream); resumeAfterZoneForm
	// therefore keeps its survived-stream skip here and only pushes when the
	// pair sits silent, which is exactly the #705 failure.
	if resume != nil {
		go s.resumeAfterZoneForm(zoneResume{push: *resume, ref: masterRef, survivorReachesMembers: true})
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

// healStaleStereoGroups clears a stale stereo pair from both speakers'
// firmwares (and this box's marge record) so a fresh /addGroup can proceed.
// The partner's firmware API (:8090) is reachable even between series-I boxes;
// only the agent ports are firewalled there.
func (s *Server) healStaleStereoGroups(ctx context.Context, c *boxapi.Client, partner boxapi.ZoneMember) {
	s.logger.Warn("stereo: firmware reports GROUP_ALREADY_EXISTS (5510), clearing the stale pair on both speakers and retrying")
	if g, err := c.GetGroup(ctx); err == nil && (g.ID != "" || len(g.Members) > 0) {
		s.logger.Info("stereo: stale pair on this speaker", "id", g.ID, "master", g.MasterDeviceID, "members", len(g.Members))
	}
	if err := c.RemoveGroup(ctx); err != nil {
		s.logger.Warn("stereo: stale-pair removeGroup failed on this speaker", "err", err)
	}
	if partner.IP != "" {
		pc := boxapi.New(partner.IP)
		if pg, err := pc.GetGroup(ctx); err == nil && (pg.ID != "" || len(pg.Members) > 0) {
			s.logger.Info("stereo: stale pair on the partner", "id", pg.ID, "master", pg.MasterDeviceID, "members", len(pg.Members))
			if err := pc.RemoveGroup(ctx); err != nil {
				s.logger.Warn("stereo: stale-pair removeGroup failed on the partner", "err", err, "partnerIP", partner.IP)
			}
		}
	}
	if s.margeGroupClear != nil {
		s.margeGroupClear("stale pair heal (5510)")
	}
	// Give the firmware a moment to settle the teardown before the retry; a
	// back-to-back addGroup right after removeGroup has been seen to 500.
	select {
	case <-ctx.Done():
	case <-time.After(1200 * time.Millisecond):
	}
}

// pushGroupDocToPartner installs (doc != "") or clears (doc == "") the
// canonical pair document on the partner's marge via its agent. Best-effort:
// between series-I boxes the agent port is firewalled and the desktop app
// relays instead (the caller reports that via partnerMargeSynced/-Cleared).
func (s *Server) pushGroupDocToPartner(ctx context.Context, partnerIP, doc string) bool {
	if partnerIP == "" {
		return false
	}
	// The partner of a stereo pair is on the local network, always. Requiring
	// that keeps a caller from pointing this push at an address off the LAN:
	// the path and the two ports are fixed, so the reach was already narrow,
	// but "narrow" is not a reason to leave it open.
	if !isPrivateHost(partnerIP) {
		s.logger.Warn("stereo: refusing to push the pair document off the local network", "partnerIP", partnerIP)
		return false
	}
	// Short per-port budget: on series-I (the only stereo hardware) a blocked
	// agent port black-holes the SYN, and this push runs inside the pairing
	// response — an open LAN port answers in milliseconds.
	client := &http.Client{Timeout: 800 * time.Millisecond}
	for _, port := range []string{"8888", "17008"} {
		url := "http://" + net.JoinHostPort(partnerIP, port) + "/api/marge/group"
		var req *http.Request
		var err error
		if doc == "" {
			req, err = http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
		} else {
			req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(doc))
			if req != nil {
				req.Header.Set("Content-Type", "application/xml")
			}
		}
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		// An agent that predates /api/marge/group answers via its catch-all
		// index: 200 + text/html. Only the real endpoint's JSON counts, or a
		// "success" here would suppress the desktop app's relay while the
		// partner stored nothing.
		ok := resp.StatusCode == http.StatusOK &&
			strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json")
		tooOld := resp.StatusCode == http.StatusOK && !ok
		resp.Body.Close()
		if ok {
			s.logger.Info("stereo: partner marge updated directly", "partnerIP", partnerIP, "port", port, "cleared", doc == "")
			return true
		}
		if tooOld {
			s.logger.Warn("stereo: partner agent predates the pair-document relay (answered HTML), update the partner speaker", "partnerIP", partnerIP, "port", port)
			return false
		}
	}
	s.logger.Info("stereo: partner marge not reachable directly (expected between series-I speakers), the desktop app will relay", "partnerIP", partnerIP)
	return false
}

// mirrorToSlaves points each slave's box at the master's current stream URL over
// UPnP (the mirror path). The master's stream is whatever STR last told the
// master box to play (s.lastPlay), which the slaves can pull from the master
// agent's stream proxy. Looser than firmware sync, but works when the firmware
// refuses to distribute STR's source. Best-effort + heavily logged.
//
// reconcile marks the periodic 5-minute re-form (PeriodicZoneReconcile) as
// opposed to the user just having formed the group. The reconcile must only
// repair what is actually broken: lastPlay is PERSISTED across reboots, so
// without state guards an idle or standby master sprayed its stale last stream
// onto every slave each tick — a slave busy with a Spotify playlist was yanked
// to the master's old radio station every 5 minutes (#342), and a healthy
// mirroring slave was re-pushed into a re-buffer hiccup each tick. With
// reconcile set, the master must be actively playing the mirrored stream, and
// each slave is only (re)pointed per slaveMirrorAction (never woken from
// standby, never taken off another source it is playing).
func (s *Server) mirrorToSlaves(ctx context.Context, z zones.Zone, reconcile bool) {
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	s.lastPlayMu.Unlock()
	if lp == nil || lp.boxURL == "" {
		s.logger.Info("zone mirror: master is not playing yet; slaves will mirror once you start playback and the reconcile fires (beta)")
		return
	}
	// A deliberate form/remove must NOT resurrect playback the user stopped.
	// lastPlay outlives a stop, so pushing it here on the unconditional
	// (reconcile=false) path restarted a group the user had just stopped:
	// stopping a mirror group and then removing one slave restarted the stream
	// on ALL members including the removed one (live, Portable master + 2 ST10,
	// 2026-07-10). Only push when the master is actually playing its stream and
	// no user stop is in effect; otherwise just update the membership silently.
	// The reconcile=true path has its own per-box now-playing guards below.
	if !reconcile {
		if standby, busy := s.boxPlayState(); standby || !busy || s.userStoppedRecently() {
			s.logger.Info("zone mirror: master is stopped, updating group membership without restarting playback (beta)")
			return
		}
	}
	if reconcile {
		np := s.snapshotNowPlaying(ctx)
		if reason := masterMirrorSkipReason(np, lp.boxURL); reason != "" {
			s.logMirrorSkip("master", reason)
			return
		}
		s.clearMirrorSkip("master")
	}
	// lp.boxURL points the MASTER's own box at its loopback stream proxy
	// (http://127.0.0.1:8888/...). A slave cannot fetch that; it must reach the
	// master across the LAN. Rewrite the host to the master's LAN IP so each
	// slave pulls the master's stream (#70: the slave's display updated but its
	// audio kept its old stream because it was handed the master's loopback URL).
	slaveURL := s.mirrorURLForSlaves(ctx, lp.boxURL, z.MasterIP)
	for _, m := range z.Slaves {
		if m.IP == "" {
			continue
		}
		if reconcile {
			push, reason := slaveMirrorAction(fetchNowPlaying(ctx, m.IP), slaveURL)
			if !push {
				s.logMirrorSkip("slave "+m.IP, reason)
				continue
			}
			s.clearMirrorSkip("slave " + m.IP)
			s.logger.Info("zone mirror: re-forming slave (beta)", "slave", m.IP, "reason", reason)
		}
		rr := upnp.NewBoseRenderer(m.IP)
		var err error
		if lp.mime != "" {
			err = rr.PlayURLMime(ctx, slaveURL, lp.title, lp.art, lp.mime)
		} else {
			err = rr.PlayURL(ctx, slaveURL, lp.title, lp.art)
		}
		if err != nil {
			s.logger.Warn("zone mirror: slave play failed", "slave", m.IP, "err", err)
		} else {
			s.logger.Info("zone mirror: slave mirroring master stream (beta)", "slave", m.IP, "url", slaveURL)
		}
	}
}

// mirrorStreamPort is the port a SLAVE box uses to reach the master agent's
// stream proxy. The proxy listens on :8888, but a remote box cannot use that
// directly: on a BCO/whitelisted chassis (ST20 spotty/scm, Portable) the SMSC
// chipset drops an external :8888 connection, routing external TCP only to
// Bose-binary-owned listeners. Every chassis instead REDIRECTs :17008
// (SoftwareUpdate, whitelisted) to the agent's loopback :8888, which is exactly
// how the desktop app already reaches every box, so the mirror uses it too.
const mirrorStreamPort = "17008"

// mirrorURLForSlaves rewrites the master's own loopback stream URL
// (http://127.0.0.1:8888/...) into one a SLAVE box can fetch over the LAN: the
// master's LAN IP on the externally reachable :17008 redirect (mirrorStreamPort).
// masterIP comes from the persisted zone; when it is empty we fall back to the
// firmware /info IP. If no LAN IP can be resolved we return the URL unchanged
// (a no-op push beats pointing a slave at the wrong host).
func (s *Server) mirrorURLForSlaves(ctx context.Context, boxURL, masterIP string) string {
	u, err := url.Parse(boxURL)
	if err != nil {
		return boxURL
	}
	if strings.TrimSpace(masterIP) == "" {
		if info, ierr := boxapi.New(s.boxHost).GetInfo(ctx); ierr == nil {
			masterIP = strings.TrimSpace(info.IP)
		}
	}
	if masterIP == "" {
		return boxURL
	}
	u.Host = net.JoinHostPort(masterIP, mirrorStreamPort)
	return u.String()
}

// defaultZoneReconcilePath is the NAND flag file that opts a box INTO the
// periodic zone reconcile (#70 beta). Absent (the default) means OFF: the box
// never re-asserts a persisted native zone, so a speaker the user plays on its
// own is never dragged back into a group. Only an explicit "1"/"true"/"on"/"yes"
// turns it on. The default is OFF after multi-speaker users (Albrecht 5-box,
// Michal multi-ST10, 2026-06-19) reported standalone speakers being pulled into
// the master's zone every few minutes: when a member leaves to play its own
// source the master's match-before-assert guard sees a missing member and
// re-asserts setZone, dragging it back. On 0.8.x the native setZone does not even
// distribute (slaves never join, "master read-back empty"), so the periodic
// re-assert is pure churn with a real downside and no upside. Re-enable per box
// once the native path is verified on hardware (#70).
const defaultZoneReconcilePath = "/mnt/nv/streborn/zone-reconcile"

// zoneReconcileEnabled reports whether the periodic NATIVE zone re-assert runs on
// this box. Default OFF (opt-in): the flag file must explicitly say
// "1"/"true"/"on"/"yes" to turn it on. See defaultZoneReconcilePath for why the
// default flipped to OFF. A mirror zone is not gated here: its re-push has its
// own per-tick state guards (see mirrorToSlaves/slaveMirrorAction — the master
// must be actively playing, and standby or busy slaves are left alone, #342),
// so this gate only governs the broken/harmful native re-assert.
func (s *Server) zoneReconcileEnabled() bool {
	b, err := os.ReadFile(defaultZoneReconcilePath)
	if err != nil {
		return false // default OFF (opt-in)
	}
	switch strings.ToLower(strings.TrimSpace(string(b))) {
	case "1", "true", "on", "yes":
		return true // explicit opt-in
	default:
		return false
	}
}

// PeriodicZoneReconcile re-pushes a persisted mirror group so it survives
// reboot/standby/Wi-Fi outage (#70 beta), and re-asserts a native zone only when
// the box is opted in (see zoneReconcileEnabled, default OFF). No-op when
// standalone. Started by cmd/agent after the server is built. Lives on the Server
// so the mirror path can reach s.lastPlay + the UPnP renderer.
func (s *Server) PeriodicZoneReconcile() {
	if s.zones == nil || s.boxHost == "" {
		return
	}
	time.Sleep(45 * time.Second) // let the box finish booting
	s.reconcileZoneOnce(false)
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.reconcileZoneOnce(false)
		case <-s.mirrorKick:
			// A play kick: the master just started music, which is the one
			// moment the persisted group re-forms with full force (members
			// woken, zone re-asserted). The tick above stays hands-off.
			s.reconcileZoneOnce(true)
		}
	}
}

// kickMirrorAfterPlay asks for an out-of-turn reconcile shortly after a fresh
// play, so the other speakers in a group join within seconds.
//
// Waiting for the 5-minute tick is what users experience as losing their
// group. The speakers come out of standby, the user presses play on the main
// one, and for up to five minutes it is the only one playing - long enough
// that people conclude the group is gone, start the desktop app and build it
// again. It is asked about constantly ("can the group be stored permanently on
// the speakers so I don't have to start the PC after every standby", 2026-08-04).
//
// The delay lets the master's stream actually start: the reconcile requires the
// master to be audibly playing the stream it was told to play, and a speaker
// reports the new stream in now_playing a few seconds after the push.
//
// Nothing here weakens the #342 guards. This only changes WHEN a round runs;
// which speakers it touches is still slaveMirrorAction's decision, so a speaker
// in standby is left asleep and one playing its own source is left alone.
func (s *Server) kickMirrorAfterPlay() {
	if s.zones == nil || s.mirrorKick == nil {
		return
	}
	if z, ok := s.zones.Get(); !ok || z.Stereo {
		// Standalone, or a stereo pair (the firmware persists a pair itself).
		// Native zones pass since the default group (#70): the play kick is
		// their re-form trigger too, routed in reconcileZoneOnce. The first
		// live test (2026-08-26, .59 master) died on the old !z.Mirror() gate
		// here: the members stayed in standby because the kick never left.
		return
	}
	// One pending kick at a time. Skipping a play that lands inside the window
	// loses nothing: the round reads the speaker's live state when it runs, so
	// it acts on the LATEST stream either way. Deduplicating here rather than
	// at the send is what keeps a burst of plays (a user stepping through
	// presets) to a single reconcile.
	if !s.mirrorKickPending.CompareAndSwap(false, true) {
		return
	}
	go func() {
		time.Sleep(6 * time.Second)
		s.mirrorKickPending.Store(false)
		select {
		case s.mirrorKick <- struct{}{}:
		default: // a round is already queued and has not started yet
		}
	}()
}

func (s *Server) reconcileZoneOnce(playKick bool) {
	z, ok := s.zones.Get()
	if !ok {
		return // standalone
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if z.Stereo {
		// A left/right stereo pair is a firmware-native group, not a multiroom
		// zone. Re-asserting it with the zone API (/setZone) would use the wrong
		// endpoint and could fight the firmware's own pairing, so leave a native
		// stereo pair alone; the firmware persists it across reboot/standby itself.
		return
	}
	if z.Mirror() {
		if playKick {
			// The master just started music: the stored members come along,
			// standby ones included (the default-group design, 2026-08-26).
			// The wake runs before the mirror pass so a just-woken member is
			// re-pointed in the same round instead of at the next tick.
			s.wakeStoredMembersForPlay(z)
		}
		// Re-form the mirror group, guarded (best-effort): the master must be
		// actively playing the mirrored stream, and a slave is only (re)pointed
		// when it is idle, dropped off the mirror, or on a stale master stream.
		// A busy speaker is left alone — the unguarded version of this path
		// hijacked a slave's Spotify playback with the master's persisted last
		// station every 5 minutes (#342). Not gated by the native opt-in below;
		// the guards make it safe on their own.
		s.mirrorToSlaves(ctx, z, true)
		return
	}
	if playKick {
		// Native default group: the play kick is the trigger the periodic
		// re-assert never had a safe answer for. Member classification keeps
		// deliberately-solo speakers out (zones_default_group.go).
		s.formDefaultGroupOnPlay(z)
		return
	}
	if !s.zoneReconcileEnabled() {
		// The periodic native re-assert stays opt-in (default OFF): on a tick
		// there is no user intent to lean on, and re-asserting whenever a
		// member is missing dragged solo speakers back. See zoneReconcileEnabled.
		return
	}
	// Native: only re-assert when the live zone does not already match.
	c := boxapi.New(s.boxHost)
	if live, err := c.GetZone(ctx); err == nil && live.Master == z.Master && len(live.Members) == len(z.Slaves) {
		return
	}
	master := boxapi.ZoneMember{DeviceID: z.Master, IP: z.MasterIP}
	slaves := make([]boxapi.ZoneMember, 0, len(z.Slaves))
	for _, m := range z.Slaves {
		slaves = append(slaves, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
	}
	s.logger.Info("zone reconcile: re-asserting native zone (beta)", "master", z.Master, "slaves", len(slaves))
	if err := c.SetZone(ctx, master, slaves); err != nil {
		s.logger.Warn("zone reconcile: setZone failed", "err", err, "master", z.Master)
	}
}

// handleZoneDissolve tears down the zone this box leads and stops re-forming it.
func (s *Server) handleZoneDissolve(w http.ResponseWriter, r *http.Request) {
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
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var master boxapi.ZoneMember
	var slaves []boxapi.ZoneMember
	stereo := false
	// Prefer the persisted membership; fall back to the live zone so a dissolve
	// still works after an agent restart.
	if s.zones != nil {
		if z, ok := s.zones.Get(); ok {
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
	// The stereo escalation is gated on explicit caller intent (?stereo=1, set
	// by the app's undo-stereo-pair button): a plain multiroom dissolve that
	// happens to hit a box in a firmware pair must keep its pre-existing
	// no-op semantics instead of silently destroying the pair.
	if !stereo && master.DeviceID == "" && r.URL.Query().Get("stereo") == "1" {
		if g, err := c.GetGroup(ctx); err == nil && (g.ID != "" || len(g.Members) > 0) {
			// A firmware-native stereo pair with no persisted zone: dissolve it
			// as a pair. The members are partitioned relative to THIS box, not
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
	if stereo {
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
			if err := s.zones.Clear(); err != nil {
				s.logger.Warn("stereo: clear store failed", "err", err)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "stereo": true,
			"partnerIP": partnerIP, "partnerDeviceID": partnerID,
			"partnerMargeCleared": partnerCleared,
		})
		return
	}
	if master.DeviceID == "" && len(slaves) == 0 {
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
			_ = s.zones.Clear()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "nothing": true, "firmwarePair": firmwarePair})
		return
	}
	s.logger.Info("zone: dissolving (beta)", "master", master.DeviceID, "slaves", len(slaves))
	// What the master is playing RIGHT NOW, before anything is torn down. It is
	// the only way to tell, afterwards, whether a member that is still making
	// noise is carrying the group's content or something of its own. See
	// dissolvestragglers.go.
	masterLocation := playingLocation(ctx, s.boxHost)
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
			s.logger.Info("zone: members still present after removeZoneSlave, retrying", "remaining", len(cur), "attempt", attempt)
		}
		remaining = len(cur)
	}
	// The teardown above only reaches members the MASTER registered. One it
	// never registered still got audio and would play on in an empty room, so
	// silence any that are demonstrably still on the group's stream.
	s.stopStragglers(ctx, masterLocation, slaves)
	if s.zones != nil {
		if err := s.zones.Clear(); err != nil {
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
	resp := map[string]any{"ok": dissolveUnverified == "" && remaining == 0}
	if remaining > 0 {
		resp["remaining"] = remaining
		resp["error"] = "the speaker still reports members in the group"
		s.logger.Warn("zone: dissolve did not empty the group", "remaining", remaining)
	}
	if dissolveUnverified != "" {
		resp["unverified"] = dissolveUnverified
		s.logger.Warn("zone: dissolve could not be confirmed", "err", dissolveUnverified)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleBoxGroup reads the box's current stereo pair group.
// Read-only. Response is {"id":"...","name":"...","members":[...]}.
// For a box without a pair, id is empty and members is empty.
func (s *Server) handleBoxGroup(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	g, err := c.GetGroup(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// handleMargeGroupDoc exposes this box's marge stereo-pair record so both
// members of a pair can be kept on ONE canonical document. GET returns the
// stored record; POST installs a canonical document (body = the group XML);
// DELETE clears the record (dissolve). The desktop app is the usual caller:
// it relays the master's document to the partner because agent-to-agent HTTP
// is blocked between series-I boxes.
func (s *Server) handleMargeGroupDoc(w http.ResponseWriter, r *http.Request) {
	if s.margeGroupGet == nil || s.margeGroupSet == nil || s.margeGroupClear == nil {
		http.Error(w, "marge group bridge not wired", http.StatusNotImplemented)
		return
	}
	switch r.Method {
	case http.MethodGet:
		xmlDoc, canonical, ok := s.margeGroupGet()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no pair record stored"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "xml": xmlDoc, "canonical": canonical})
	case http.MethodPost, http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil || len(bytes.TrimSpace(body)) == 0 {
			http.Error(w, "empty group document", http.StatusBadRequest)
			return
		}
		if err := s.margeGroupSet(string(body)); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.logger.Info("stereo: canonical pair document installed on this marge (relay)")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case http.MethodDelete:
		s.margeGroupClear("relay dissolve")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
	ownID := fetchDeviceID(ctx, s.boxHost)
	if ownID == "" || strings.EqualFold(ownID, master) {
		return nil, false // we lead it, or cannot tell who we are
	}
	lz, err := fetchZone(ctx, senderIP)
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

// isPrivateHost reports whether host is a literal address on this machine's own
// network. A name is refused rather than resolved: resolution here would be a
// second chance to point the push somewhere else.
func isPrivateHost(host string) bool {
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
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
