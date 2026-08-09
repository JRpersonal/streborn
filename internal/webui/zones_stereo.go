// Multiroom zones and stereo-pair handling.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
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
	out := struct {
		boxapi.Zone
		Stereo *boxapi.Group `json:"stereo,omitempty"`
	}{Zone: z}
	if g, gerr := c.GetGroup(ctx); gerr == nil && (g.ID != "" || len(g.Members) > 0) {
		g := g
		out.Stereo = &g
	}
	writeJSON(w, http.StatusOK, out)
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
	s.logger.Info("zone: forming (beta)", "mode", mode, "master", master.DeviceID, "masterIP", master.IP,
		"slaves", len(slaves), "stereo", req.Stereo, "name", req.Name)

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
	for i := range slaves {
		if slaves[i].IP == "" {
			continue
		}
		ictx, icancel := context.WithTimeout(ctx, 2*time.Second)
		info, err := boxapi.New(slaves[i].IP).GetInfo(ictx)
		icancel()
		if err != nil {
			continue // unreachable right now: keep what the caller supplied
		}
		real := strings.TrimSpace(info.DeviceID)
		if real == "" || strings.EqualFold(real, slaves[i].DeviceID) {
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

	// Persist first so a transient drive error still leaves the group on record
	// for the reconcile loop to retry. Only the master persists.
	z := zones.Zone{Master: master.DeviceID, MasterIP: master.IP, Mode: mode, Name: req.Name}
	for _, m := range slaves {
		z.Slaves = append(z.Slaves, zones.Member{DeviceID: m.DeviceID, IP: m.IP})
	}
	if s.zones != nil {
		if err := s.zones.Set(z); err != nil {
			s.logger.Warn("zone: persist failed", "err", err)
		}
	}

	if mode == "mirror" {
		// Deliberate user action: push unconditionally (reconcile=false), the
		// user just asked for exactly this group.
		s.mirrorToSlaves(ctx, z, false)
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
	s.ensureBoxReady(ctx)

	// Remove members the user dropped from the group. /setZone only ADDS the
	// listed slaves, it never removes one, so re-forming with a smaller list -
	// exactly how the app removes a member (uncheck + apply) - leaves the dropped
	// box in the firmware zone: it "briefly leaves then comes back" (Albrecht,
	// 7-box fleet, 2026-07-14). Read the live zone and RemoveZoneSlave anyone no
	// longer wanted. Match on IP, the chassis-stable key: a two-chip box (Portable,
	// ST20 BCO) announces its wlan0 MAC over discovery, which is NOT the SCM
	// deviceID the firmware lists for it, so a deviceID-only match would wrongly
	// keep the dropped box. Best-effort, before the add below.
	if live, gerr := c.GetZone(ctx); gerr == nil && live.Master != "" && len(live.Members) > 0 {
		wantIP := make(map[string]bool, len(slaves))
		wantDev := make(map[string]bool, len(slaves))
		for _, sl := range slaves {
			if sl.IP != "" {
				wantIP[sl.IP] = true
			}
			if sl.DeviceID != "" {
				wantDev[strings.ToLower(sl.DeviceID)] = true
			}
		}
		var toRemove []boxapi.ZoneMember
		for _, m := range live.Members {
			keep := (m.IP != "" && wantIP[m.IP]) || (m.DeviceID != "" && wantDev[strings.ToLower(m.DeviceID)])
			if !keep {
				toRemove = append(toRemove, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
			}
		}
		if len(toRemove) > 0 {
			s.logger.Info("zone: dropping members no longer in the group before re-forming", "count", len(toRemove), "master", master.DeviceID)
			if err := c.RemoveZoneSlave(ctx, master, toRemove); err != nil {
				s.logger.Warn("zone: reconcile removeZoneSlave failed", "err", err)
			}
		}
	}

	if err := c.SetZone(ctx, master, slaves); err != nil {
		s.logger.Warn("zone: setZone failed", "err", err, "master", master.DeviceID)
		http.Error(w, "setZone: "+err.Error(), http.StatusBadGateway)
		return
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
	missing, unverifiable := verifyFollowersJoined(ctx, s.logger, z2.Master, slaves, func(fctx context.Context, ip string) (boxapi.Zone, error) {
		return boxapi.New(ip).GetZone(fctx)
	})
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
	if resume != nil && masterFormed {
		go s.resumeAfterZoneForm(*resume)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": ok, "mode": "native", "master": z2.Master, "senderIP": z2.SenderIP,
		"members": z2.Members, "requested": len(slaves),
		"verified": verified, "missing": missing, "unverifiable": unverifiable,
		"masterMissing": masterMissing,
	})
}

// resumeAfterZoneForm re-pushes the stream that was playing on this box before
// a native zone form tore it down (see handleZoneForm). The firmware needs a
// settle moment after /setZone before it accepts a new SetURI - pushing too
// early just re-triggers the 1036 wrong-state error - so wait, then push under
// the box command lock, standing down when the user stopped meanwhile or a
// newer play superseded the captured one.
func (s *Server) resumeAfterZoneForm(lp lastPlayInfo) {
	if s.renderer == nil {
		return
	}
	time.Sleep(1500 * time.Millisecond)
	if s.userStoppedRecently() {
		s.logger.Info("zone: not restarting playback after forming, user stopped meanwhile")
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	s.lastPlayMu.Lock()
	cur := s.lastPlay
	s.lastPlayMu.Unlock()
	if resumeIsStale(lp.boxURL, lp.ts, cur) {
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

// formStereoPair drives POST /addGroup to make a real left/right stereo pair
// and persists it so it is honored on dissolve. master becomes LEFT, the single
// partner becomes RIGHT. The firmware decides whether the box can pair (ST10
// only); its error is returned verbatim to the app so testers see the truth.
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
	actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
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
		if err != nil {
			// Before reporting a failure, ASK the speaker. A timed-out or reset
			// /addGroup does not mean the firmware did nothing: it kept going and
			// formed the pair after the call had already been abandoned (live,
			// two SoundTouch 10s, 2026-08-04). Reporting failure for a pair that
			// exists is worse than the timeout itself, because the user's next
			// move is to pair again, which the firmware then rejects with
			// GROUP_ALREADY_EXISTS.
			cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Second)
			g, gerr := c.GetGroup(cctx)
			ccancel()
			if gerr == nil && len(g.Members) == 2 {
				s.logger.Warn("stereo: addGroup reported an error but the speaker formed the pair anyway, treating it as paired",
					"err", err, "id", g.ID)
			} else {
				s.logger.Warn("stereo: addGroup failed (only the ST10 supports stereo pairs)", "err", err)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "stereo": true, "id": g.ID, "name": g.Name, "members": g.Members,
		"canonicalGroup": canonicalDoc, "partnerIP": partner.IP,
		"partnerMargeSynced": partnerSynced,
	})
}

// isGroupExistsErr reports whether an /addGroup error is the firmware's 5510
// GROUP_ALREADY_EXISTS rejection.
func isGroupExistsErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "5510") || strings.Contains(msg, "GROUP_ALREADY_EXISTS")
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
	s.reconcileZoneOnce()
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-s.mirrorKick:
		}
		s.reconcileZoneOnce()
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
	if z, ok := s.zones.Get(); !ok || !z.Mirror() {
		return // standalone, or a native zone / stereo pair: not our business
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

func (s *Server) reconcileZoneOnce() {
	z, ok := s.zones.Get()
	if !ok {
		return // standalone
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if z.Mirror() {
		// Re-form the mirror group, guarded (best-effort): the master must be
		// actively playing the mirrored stream, and a slave is only (re)pointed
		// when it is idle, dropped off the mirror, or on a stale master stream.
		// A standby or otherwise-busy speaker is left alone — the unguarded
		// version of this path hijacked a slave's Spotify playback with the
		// master's persisted last station every 5 minutes (#342). Not gated by
		// the native opt-in below; the guards make it safe on their own.
		s.mirrorToSlaves(ctx, z, true)
		return
	}
	if z.Stereo {
		// A left/right stereo pair is a firmware-native group, not a multiroom
		// zone. Re-asserting it with the zone API (/setZone) would use the wrong
		// endpoint and could fight the firmware's own pairing, so leave a native
		// stereo pair alone; the firmware persists it across reboot/standby itself.
		return
	}
	if !s.zoneReconcileEnabled() {
		// Native re-assert is opt-in (default OFF): re-asserting setZone whenever a
		// member is missing dragged solo speakers back into the group, and on 0.8.x
		// native zones do not distribute anyway. See zoneReconcileEnabled.
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
			if err != nil || z.Master == "" || len(z.Members) == 0 {
				break // zone gone (or unreadable): done
			}
			cur = z.Members
			s.logger.Info("zone: members still present after removeZoneSlave, retrying", "remaining", len(cur), "attempt", attempt)
		}
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
