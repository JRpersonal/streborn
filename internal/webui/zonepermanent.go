// zonepermanent.go: the permanent-group engine (beta). Exactly one group
// template can be marked permanent; this file makes that group come back on
// its own, on EVENTS only, never on a tick that could drag a speaker
// anywhere it was deliberately taken from (the mistake that got the periodic
// zone reconcile disabled):
//
//   - once after boot, when the firmware zone is empty,
//   - when this master returns from standby,
//   - right after the user starts playback on the master,
//   - and members waking from standby are joined into the RUNNING group via
//     the incremental add, which does not interrupt the music.
//
// Hard rules, enforced here: never wake a sleeping member (all probing rides
// :8090/now_playing reads, which cannot wake a box), never fight deep sleep
// (a deep-sleep box is off the network and costs one failed dial, no
// retries), and a member the user removed or that plays its own source stays
// out (the store's deliberately-out list) until it is explicitly re-added.

package webui

import (
	"context"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zonetemplates"
)

// permMemberState classifies one template member for the permanent engine.
type permMemberState int

const (
	permUnreachable permMemberState = iota // deep sleep / off network: skip, no retry
	permStandby                            // asleep: skip, enrolling would wake it
	permSelfPlaying                        // awake and playing its own source: deliberately out
	permJoinable                           // awake and idle/paused: safe to join
)

// classifyPermMember probes ONE member once (no retries) via its Bose
// firmware port :8090, the only port reachable across all chassis
// generations, with a read that cannot wake a box: a standby box answers
// /now_playing with source STANDBY without waking, and a deep-sleep box is
// simply off the network so the probe fails.
func classifyPermMember(ctx context.Context, m zonetemplates.Member, fetch func(context.Context, string) nowPlayingSnapshot) permMemberState {
	if m.IP == "" {
		return permUnreachable
	}
	np := fetch(ctx, m.IP)
	switch {
	case np.Source == "":
		return permUnreachable
	case np.Source == "STANDBY":
		return permStandby
	case isPlayingStatus(np.PlayStatus):
		return permSelfPlaying
	default:
		return permJoinable
	}
}

// permTarget returns the permanent template, but only when this box is its
// master: followers never drive anything.
func (s *Server) permTarget(ctx context.Context) (zonetemplates.Template, bool) {
	if s.tpls == nil || s.boxHost == "" {
		return zonetemplates.Template{}, false
	}
	tpl, ok := s.tpls.Permanent()
	if !ok {
		return zonetemplates.Template{}, false
	}
	self := s.localDeviceID(ctx, boxapi.New(s.boxHost), "")
	if self == "" || !strings.EqualFold(strings.TrimSpace(self), strings.TrimSpace(tpl.Master.DeviceID)) {
		return zonetemplates.Template{}, false
	}
	return tpl, true
}

// permEligibleMembers filters the template's members down to the ones the
// engine may act on right now: not deliberately out, awake, and not playing
// their own source. Members seen playing their own source are recorded as
// deliberately out (reason self-play) on the way.
func (s *Server) permEligibleMembers(ctx context.Context, members []zonetemplates.Member) (eligible []boxapi.ZoneMember, skipped int) {
	for _, m := range members {
		if s.tpls.IsOut(m.DeviceID, m.IP) {
			skipped++
			continue
		}
		switch classifyPermMember(ctx, m, fetchNowPlaying) {
		case permJoinable:
			eligible = append(eligible, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
		case permSelfPlaying:
			if err := s.tpls.AddOut(m.DeviceID, m.IP, "self-play"); err == nil {
				s.logger.Info("zone permanent: member is playing its own source, leaving it alone (beta)", "member", m.DeviceID, "ip", m.IP)
			}
			skipped++
		default:
			skipped++
		}
	}
	return eligible, skipped
}

// permReformIfZoneEmpty re-forms the permanent group when the firmware zone
// is EMPTY: the post-reboot and post-standby case, and nothing else. A live
// zone of any shape is never touched from here. Returns true only when a
// drive actually ran, so the callers' debounce can be released after a
// stand-down (a swallowed no-op wake kick used to eat the play-time assert
// that fired right after, and the group never formed).
func (s *Server) permReformIfZoneEmpty(trigger string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tpl, ok := s.permTarget(ctx)
	if !ok {
		return false
	}
	c := boxapi.New(s.boxHost)
	live, err := c.GetZone(ctx)
	if err != nil {
		s.logger.Info("zone permanent: could not read the live zone, not re-forming", "err", err, "trigger", trigger)
		return false
	}
	if live.Master != "" && len(live.Members) > 0 {
		return false // a zone already exists: never touch it here
	}
	// A master that is itself asleep stays asleep: forming would be pointless
	// (the next preset press re-asserts anyway) and this path must never be
	// the reason a box cannot reach deep standby.
	if np := fetchNowPlaying(ctx, s.boxHost); np.Source == "" || np.Source == "STANDBY" {
		return false
	}
	eligible, skipped := s.permEligibleMembers(ctx, tpl.Members)
	if len(eligible) == 0 {
		s.logger.Info("zone permanent: no member awake to re-form with, waiting for members to wake (beta)", "trigger", trigger, "skipped", skipped)
		return false
	}
	s.logger.Info("zone permanent: re-forming the permanent group (beta)", "trigger", trigger, "template", tpl.Name, "members", len(eligible), "skipped", skipped)
	res := s.driveZone(ctx, boxapi.ZoneMember{DeviceID: tpl.Master.DeviceID, IP: tpl.Master.IP}, eligible, tpl.Name, "native",
		zoneDriveOpts{coalesce: false, persist: true, resume: false, wake: false, reason: trigger,
			preflight: s.permReformPreflight(ctx, eligible)})
	if res.errText != "" {
		s.logger.Warn("zone permanent: re-form failed, next trigger event retries (beta)", "trigger", trigger, "err", res.errText)
	}
	// No retry here on purpose: the next trigger event (wake, play, keeper
	// tick finding a live zone to add to) is the retry.
	return res.body == nil || res.body["stoodDown"] != true
}

// permReformPreflight re-validates a queued re-form under the serial lock: a
// user form or dissolve may have run while this drive waited. The permanent
// flag must still stand, the firmware zone must still be empty, and every
// member must still be in the template and not deliberately out. The
// awake/asleep probes are NOT repeated (their result ages by seconds either
// way); the races that matter are flag, zone, and out list.
func (s *Server) permReformPreflight(ctx context.Context, eligible []boxapi.ZoneMember) func() ([]boxapi.ZoneMember, bool) {
	return func() ([]boxapi.ZoneMember, bool) {
		tpl, ok := s.permTarget(ctx)
		if !ok {
			return nil, false
		}
		live, err := boxapi.New(s.boxHost).GetZone(ctx)
		if err != nil || (live.Master != "" && len(live.Members) > 0) {
			return nil, false
		}
		fresh := s.filterStillWanted(tpl, eligible)
		if len(fresh) == 0 {
			return nil, false
		}
		return fresh, true
	}
}

// filterStillWanted keeps only members that are still in the template and
// not deliberately out.
func (s *Server) filterStillWanted(tpl zonetemplates.Template, members []boxapi.ZoneMember) []boxapi.ZoneMember {
	inTemplate := func(m boxapi.ZoneMember) bool {
		for _, tm := range tpl.Members {
			if (m.IP != "" && tm.IP == m.IP) || (m.DeviceID != "" && tm.DeviceID != "" && strings.EqualFold(tm.DeviceID, m.DeviceID)) {
				return true
			}
		}
		return false
	}
	out := make([]boxapi.ZoneMember, 0, len(members))
	for _, m := range members {
		if inTemplate(m) && !s.tpls.IsOut(m.DeviceID, m.IP) {
			out = append(out, m)
		}
	}
	return out
}

// KickPermanentWake schedules the standby-wake re-form, debounced so the
// several wake entry points cannot stack drives.
func (s *Server) KickPermanentWake() {
	if s.tpls == nil {
		return
	}
	s.permAssertMu.Lock()
	if time.Since(s.permLastAssert) < 30*time.Second {
		s.permAssertMu.Unlock()
		return
	}
	s.permLastAssert = time.Now()
	s.permAssertMu.Unlock()
	go func() {
		if !s.permReformIfZoneEmpty("standby-wake") {
			// Nothing was driven: release the debounce so the play-time
			// assert that typically follows a wake is not swallowed.
			s.permAssertMu.Lock()
			s.permLastAssert = time.Time{}
			s.permAssertMu.Unlock()
		}
	}()
}

// kickPermanentAssert runs after the user starts playback on this box: the
// permanent group is asserted (fresh form, or an incremental add of members
// that came back) so the music the user just started reaches the whole
// constellation. Async because NoteUserPlay is called with boxCmdMu held and
// adjacent to the gabbo read loop; zoneFormSerial orders the drive against
// user actions, and the incremental path joins a playing group without
// interrupting it, so "before the start" degrades to "within a second or two
// of it" with no audible difference.
func (s *Server) kickPermanentAssert() {
	if s.tpls == nil {
		return
	}
	s.permAssertMu.Lock()
	if time.Since(s.permLastAssert) < 20*time.Second {
		s.permAssertMu.Unlock()
		return
	}
	s.permLastAssert = time.Now()
	s.permAssertMu.Unlock()
	go func() {
		if !s.permAssertOnce() {
			s.permAssertMu.Lock()
			s.permLastAssert = time.Time{}
			s.permAssertMu.Unlock()
		}
	}()
}

func (s *Server) permAssertOnce() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tpl, ok := s.permTarget(ctx)
	if !ok {
		return false
	}
	// The play that triggered this assert may not have WOKEN the box yet: a
	// hardware preset notes the play before the recall's wake runs. Forming
	// against a standby master is the proven 1036 self-dissolve trap, so
	// wait briefly for the box to come up instead of racing its wake.
	awake := false
	for i := 0; i < 10; i++ {
		np := fetchNowPlaying(ctx, s.boxHost)
		if np.Source != "" && np.Source != "STANDBY" {
			awake = true
			break
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return false
		}
	}
	if !awake {
		return false
	}
	c := boxapi.New(s.boxHost)
	live, err := c.GetZone(ctx)
	if err != nil {
		return false
	}
	self := tpl.Master.DeviceID
	zoneLive := live.Master != "" && len(live.Members) > 0 && strings.EqualFold(strings.TrimSpace(live.Master), strings.TrimSpace(self))
	// Members already in the live zone need nothing; the rest are candidates.
	want := make([]zonetemplates.Member, 0, len(tpl.Members))
	for _, m := range tpl.Members {
		if zoneLive && zoneHasMember(live, m.DeviceID, m.IP) {
			continue
		}
		want = append(want, m)
	}
	eligible, _ := s.permEligibleMembers(ctx, want)
	if len(eligible) == 0 {
		return false
	}
	// Under the serial lock the world may have changed (a user form, a
	// dissolve): re-validate and rebuild the list from the then-current
	// zone. A dissolve also clears the permanent flag, which permTarget
	// re-checks, so a queued assert cannot re-form a just-dissolved group.
	preflight := func() ([]boxapi.ZoneMember, bool) {
		tpl2, ok2 := s.permTarget(ctx)
		if !ok2 {
			return nil, false
		}
		live2, lerr := boxapi.New(s.boxHost).GetZone(ctx)
		if lerr != nil {
			return nil, false
		}
		stillAbsent := make([]boxapi.ZoneMember, 0, len(eligible))
		for _, m := range s.filterStillWanted(tpl2, eligible) {
			if !zoneHasMember(live2, m.DeviceID, m.IP) {
				stillAbsent = append(stillAbsent, m)
			}
		}
		if len(stillAbsent) == 0 {
			return nil, false
		}
		if live2.Master != "" && len(live2.Members) > 0 && strings.EqualFold(strings.TrimSpace(live2.Master), strings.TrimSpace(tpl2.Master.DeviceID)) {
			return append(zoneMembersOf(live2), stillAbsent...), true
		}
		return stillAbsent, true
	}
	if zoneLive {
		// Seamless: join into the existing (possibly playing) zone via the
		// incremental path; keep the live members in the requested list so
		// the drive's diff only ADDS.
		full := append(zoneMembersOf(live), eligible...)
		s.logger.Info("zone permanent: asserting the permanent group around the user's play (beta)", "adding", len(eligible))
		res := s.driveZone(ctx, boxapi.ZoneMember{DeviceID: tpl.Master.DeviceID, IP: tpl.Master.IP}, full, tpl.Name, "native",
			zoneDriveOpts{coalesce: false, persist: false, resume: false, wake: false, reason: "preplay", preflight: preflight})
		return res.body == nil || res.body["stoodDown"] != true
	}
	// Fresh form at play time (the boot re-form found nobody awake earlier).
	// resume:true is essential: the drive's own capture plus the conditional
	// restart is what heals the 1036 tear-down when the user's stream landed
	// before the SetZone.
	s.logger.Info("zone permanent: forming the permanent group for the user's play (beta)", "members", len(eligible))
	res := s.driveZone(ctx, boxapi.ZoneMember{DeviceID: tpl.Master.DeviceID, IP: tpl.Master.IP}, eligible, tpl.Name, "native",
		zoneDriveOpts{coalesce: false, persist: true, resume: true, wake: false, reason: "preplay", preflight: preflight})
	return res.body == nil || res.body["stoodDown"] != true
}

// zoneHasMember reports whether the live zone lists the member, IP first
// (chassis-stable), deviceID as the fallback.
func zoneHasMember(z boxapi.Zone, deviceID, ip string) bool {
	for _, m := range z.Members {
		if ip != "" && m.IP == ip {
			return true
		}
		if deviceID != "" && m.DeviceID != "" && strings.EqualFold(m.DeviceID, deviceID) {
			return true
		}
	}
	return false
}

func zoneMembersOf(z boxapi.Zone) []boxapi.ZoneMember {
	out := make([]boxapi.ZoneMember, 0, len(z.Members))
	for _, m := range z.Members {
		out = append(out, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
	}
	return out
}

// PermanentZoneKeeper is the member-wake pickup: master-side polling of the
// members' :8090, the load-bearing channel on purpose. Agent-to-agent HTTP is
// firewalled between series-I boxes, a standby-dropped member does not know
// its master (only the master persists the zone document), and gabbo gives
// the master no signal when a member wakes (zoneUpdated fires on drop, not on
// the later wake). :8090 reads cannot wake a box and cannot hold one out of
// deep sleep; a deep-sleep member costs one failed dial per minute, no
// retries. The keeper only ever ADDS to an existing live zone: forming
// belongs to the boot/wake/preplay events, never to a tick.
func (s *Server) PermanentZoneKeeper() {
	if s.tpls == nil || s.boxHost == "" {
		return
	}
	time.Sleep(75 * time.Second) // after the boot re-form window
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for range t.C {
		s.permWatchOnce()
	}
}

func (s *Server) permWatchOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	tpl, ok := s.permTarget(ctx)
	if !ok {
		s.permLastWatch.Store("no permanent template, or this box is not its master")
		return
	}
	live, err := boxapi.New(s.boxHost).GetZone(ctx)
	if err != nil {
		s.permLastWatch.Store("live zone unreadable: " + err.Error())
		return
	}
	if live.Master == "" || len(live.Members) == 0 || !strings.EqualFold(strings.TrimSpace(live.Master), strings.TrimSpace(tpl.Master.DeviceID)) {
		s.permLastWatch.Store("no live zone led by this box; forming happens on boot/wake/play events only")
		return
	}
	// An idle master in standby leaves everyone alone.
	if np := fetchNowPlaying(ctx, s.boxHost); np.Source == "" || np.Source == "STANDBY" {
		s.permLastWatch.Store("master in standby")
		return
	}
	absent := make([]zonetemplates.Member, 0, len(tpl.Members))
	for _, m := range tpl.Members {
		if !zoneHasMember(live, m.DeviceID, m.IP) {
			absent = append(absent, m)
		}
	}
	if len(absent) == 0 {
		s.permLastWatch.Store("complete")
		return
	}
	eligible, skipped := s.permEligibleMembers(ctx, absent)
	if len(eligible) == 0 {
		s.permLastWatch.Store("members absent but none joinable (asleep, out, or unreachable)")
		_ = skipped
		return
	}
	ids := make([]string, 0, len(eligible))
	for _, m := range eligible {
		ids = append(ids, m.DeviceID)
	}
	s.logger.Info("zone permanent: member woke, joining it into the running group (beta)", "members", strings.Join(ids, ","))
	s.permLastWatch.Store("joining: " + strings.Join(ids, ","))
	// Incremental AddZoneSlave under the hood: the running music never stops.
	// The preflight rebuilds the list under the serial lock: a user form or a
	// dissolve may have queued ahead of this drive, and the join must apply
	// to the zone as it is THEN, or stand down entirely.
	s.driveZone(ctx, boxapi.ZoneMember{DeviceID: tpl.Master.DeviceID, IP: tpl.Master.IP},
		append(zoneMembersOf(live), eligible...), tpl.Name, "native",
		zoneDriveOpts{coalesce: false, persist: false, resume: false, wake: false, reason: "wake-rejoin",
			preflight: func() ([]boxapi.ZoneMember, bool) {
				tpl2, ok2 := s.permTarget(ctx)
				if !ok2 {
					return nil, false
				}
				live2, lerr := boxapi.New(s.boxHost).GetZone(ctx)
				if lerr != nil || live2.Master == "" || len(live2.Members) == 0 ||
					!strings.EqualFold(strings.TrimSpace(live2.Master), strings.TrimSpace(tpl2.Master.DeviceID)) {
					return nil, false // the watch only ever ADDS to a live zone
				}
				stillAbsent := make([]boxapi.ZoneMember, 0, len(eligible))
				for _, m := range s.filterStillWanted(tpl2, eligible) {
					if !zoneHasMember(live2, m.DeviceID, m.IP) {
						stillAbsent = append(stillAbsent, m)
					}
				}
				if len(stillAbsent) == 0 {
					return nil, false
				}
				return append(zoneMembersOf(live2), stillAbsent...), true
			}})
}

// notePermanentMembership keeps the deliberately-out list in sync with the
// user's explicit group edits: a form INCLUDING a template member clears its
// out entry, a form of a smaller list marks the dropped template members out
// (reason user-removed). Declarative on purpose: the user's full-list POST
// defines who is out, no live state needed, and it covers the desktop chips,
// the desktop multiroom form, and the phone's leave in one place.
func (s *Server) notePermanentMembership(master boxapi.ZoneMember, slaves []boxapi.ZoneMember) {
	if s.tpls == nil {
		return
	}
	tpl, ok := s.tpls.Permanent()
	if !ok || !strings.EqualFold(strings.TrimSpace(tpl.Master.DeviceID), strings.TrimSpace(master.DeviceID)) {
		return
	}
	inRequest := func(m zonetemplates.Member) bool {
		for _, sl := range slaves {
			if m.IP != "" && sl.IP == m.IP {
				return true
			}
			if m.DeviceID != "" && sl.DeviceID != "" && strings.EqualFold(sl.DeviceID, m.DeviceID) {
				return true
			}
		}
		return false
	}
	for _, m := range tpl.Members {
		if inRequest(m) {
			_ = s.tpls.RemoveOut(m.DeviceID, m.IP)
			continue
		}
		if !s.tpls.IsOut(m.DeviceID, m.IP) {
			s.logger.Info("zone permanent: member removed by the user, staying out until re-added (beta)", "member", m.DeviceID)
			_ = s.tpls.AddOut(m.DeviceID, m.IP, "user-removed")
		}
	}
}

// ZoneTemplatesDebug surfaces the engine's state in every diagnostic bundle.
func (s *Server) ZoneTemplatesDebug() any {
	if s.tpls == nil {
		return map[string]any{"enabled": false}
	}
	names := []string{}
	for _, t := range s.tpls.List() {
		names = append(names, t.Name)
	}
	perm := ""
	if p, ok := s.tpls.Permanent(); ok {
		perm = p.Name
	}
	lastWatch, _ := s.permLastWatch.Load().(string)
	s.permAssertMu.Lock()
	lastAssert := s.permLastAssert
	s.permAssertMu.Unlock()
	return map[string]any{
		"enabled":        true,
		"templates":      names,
		"permanent":      perm,
		"out":            s.tpls.OutList(),
		"bootReformDone": s.permBootDone.Load(),
		"lastAssert":     lastAssert,
		"lastWatch":      lastWatch,
	}
}
