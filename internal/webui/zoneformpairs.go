// A stereo pair in a multiroom group: the pair joins as ONE member, addressed
// by its pair master, and never leads the group.
//
// Two shapes are firmware facts, measured and kept refused (#792, a
// 12-speaker household on 2026-08-30): a group LED by a pair master never
// starts the firmware's audio distribution (the leader fetches the stream,
// never renders it, and loops on 3103 AUDIO_ERROR_TIMEOUT every 60 s, the
// same with the Bose app), and a pair's other half enrolled on its own serves
// two masters and plays out of step with its own pair.
//
// The third shape works and is what the Bose app does: the pair as a MEMBER,
// named in the leader's zone call by its pair master alone. The same household
// grouped a SoundTouch 30 with a pair of SoundTouch 10s that way (diagnostic
// bundle 2026-11-15): the leader's zone listed only the pair master, the pair
// master reported the zone AND its pair (GROUP_OK), the other half reported
// only the pair, and all three played the same station with no 3103 error.
//
// So this phase, which runs before anything is driven or stored:
//   - refuses a group whose leader is half of a pair, with a code a client can
//     read (pairCannotLeadCode);
//   - and maps a pair's other half onto the pair master, so a pair is always
//     one member and never two.

package webui

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// pairCannotLeadCode is the stable code of the refusal for a group led by a
// pair half, for clients that turn agent refusals into their own words.
const pairCannotLeadCode = "pair-cannot-lead"

// pairCannotLeadMsg is the refusal's English text.
const pairCannotLeadMsg = "this speaker is half of a stereo pair: a stereo pair can join a group but cannot lead one " +
	"(the speaker firmware never starts the audio then). Start the group on another speaker and add the pair to it"

// pairReadBudget bounds one /getGroup read. A SoundTouch 10 answers in
// milliseconds; the scm/BCO chassis hang the call instead of refusing it, and
// they cannot pair anyway.
const pairReadBudget = 1500 * time.Millisecond

// pairableModel matches the one model that can be half of a stereo pair, the
// SoundTouch 10 ("SoundTouch 10" in /info), and not the 20, 30, 300 or the
// Portable. The frontend uses the same \b10\b test.
var pairableModel = regexp.MustCompile(`\b10\b`)

// fetchGroup reads a speaker's stereo pair (/getGroup).
func (s *Server) fetchGroup(ctx context.Context, host string) (boxapi.Group, error) {
	if s.fetchGroupFn != nil {
		return s.fetchGroupFn(ctx, host)
	}
	return boxapi.New(host).GetGroup(ctx)
}

// groupIsPair reports whether a /getGroup answer describes a pair at all. An
// unpaired speaker answers an empty group.
func groupIsPair(g boxapi.Group) bool {
	return g.ID != "" || len(g.Members) > 0
}

// pairMasterMember is the member a pair is addressed by: the role entry of
// the group's masterDeviceId, else its LEFT half (the master by Bose
// convention). ok is false when neither carries an address.
func pairMasterMember(g boxapi.Group) (boxapi.ZoneMember, bool) {
	for _, m := range g.Members {
		if m.IP != "" && m.DeviceID != "" && strings.EqualFold(m.DeviceID, g.MasterDeviceID) {
			return boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP}, true
		}
	}
	for _, m := range g.Members {
		if m.IP != "" && strings.EqualFold(m.Role, "LEFT") {
			return boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP}, true
		}
	}
	return boxapi.ZoneMember{}, false
}

// sameSpeaker matches two member records by address or by deviceID.
func sameSpeaker(a, b boxapi.ZoneMember) bool {
	return (a.IP != "" && a.IP == b.IP) || (a.DeviceID != "" && strings.EqualFold(a.DeviceID, b.DeviceID))
}

// pairUnitMembers maps a requested member list onto what the leader's zone
// call can take: a member that is half of a pair (groups names the pair each
// paired member reported, by its address) is replaced by the pair master,
// and every speaker appears once. mapped lists the halves that were replaced,
// as "from>to" addresses for the log. leadsPair is true when a pair named
// among the members is led by self, i.e. the group's own leader is half of
// that pair: the caller refuses then.
func pairUnitMembers(self boxapi.ZoneMember, selfHost string, slaves []boxapi.ZoneMember, groups map[string]boxapi.Group) (out []boxapi.ZoneMember, mapped []string, leadsPair bool) {
	out = make([]boxapi.ZoneMember, 0, len(slaves))
	seen := func(m boxapi.ZoneMember) bool {
		for _, o := range out {
			if sameSpeaker(o, m) {
				return true
			}
		}
		return false
	}
	isSelf := func(m boxapi.ZoneMember) bool {
		return sameSpeaker(m, self) || (selfHost != "" && m.IP == selfHost)
	}
	for _, m := range slaves {
		unit := m
		if g, ok := groups[m.IP]; ok && groupIsPair(g) {
			if pm, ok := pairMasterMember(g); ok {
				if isSelf(pm) {
					leadsPair = true
					continue
				}
				if !sameSpeaker(pm, m) {
					mapped = append(mapped, m.IP+">"+pm.IP)
					unit = pm
				}
			}
			// The pair's other half may be this very box: the leader is in
			// the pair then, which the caller refuses.
			for _, gm := range g.Members {
				if isSelf(boxapi.ZoneMember{DeviceID: gm.DeviceID, IP: gm.IP}) {
					leadsPair = true
				}
			}
		}
		if isSelf(unit) || seen(unit) {
			continue
		}
		out = append(out, unit)
	}
	return out, mapped, leadsPair
}

// ownPair reads this speaker's own stereo pair, from its firmware first and
// from the last answer the zone poll saw when the firmware does not answer.
func (s *Server) ownPair(ctx context.Context) (boxapi.Group, bool) {
	if s.boxHost != "" && (s.fetchGroupFn != nil || s.groupReadAllowed()) {
		gctx, cancel := context.WithTimeout(ctx, pairReadBudget)
		g, err := s.fetchGroup(gctx, s.boxHost)
		cancel()
		if s.fetchGroupFn == nil {
			s.noteGroupReadResult(err)
		}
		if err == nil {
			return g, groupIsPair(g)
		}
	}
	s.stereoSeenMu.Lock()
	seen, at := s.stereoSeen, s.stereoSeenAt
	s.stereoSeenMu.Unlock()
	if seen != nil && time.Since(at) < stereoSeenFresh && groupIsPair(*seen) {
		return *seen, true
	}
	return boxapi.Group{}, false
}

// rememberMemberType records the model a member's firmware reported.
func (s *Server) rememberMemberType(ip, typ string) {
	typ = strings.TrimSpace(typ)
	if ip == "" || typ == "" {
		return
	}
	s.memberIDsMu.Lock()
	defer s.memberIDsMu.Unlock()
	if s.memberTypes == nil {
		s.memberTypes = make(map[string]string)
	}
	s.memberTypes[ip] = typ
}

// cachedMemberType returns the model a member reported last, or "".
func (s *Server) cachedMemberType(ip string) string {
	s.memberIDsMu.RLock()
	defer s.memberIDsMu.RUnlock()
	return s.memberTypes[ip]
}

// mayBePairHalf reports whether a member is worth a /getGroup read: a
// speaker whose firmware said it is a SoundTouch 10. A member of unknown
// model is not asked, because the read hangs on the chassis that cannot
// pair; the desktop app maps a pair half on its side as well.
func (s *Server) mayBePairHalf(ip string) bool {
	if ip == "" {
		return false
	}
	if s.fetchGroupFn != nil {
		return true
	}
	return pairableModel.MatchString(s.cachedMemberType(ip))
}

// memberPairs reads the pair of every member that can be half of one, keyed
// by the member's address. Members that do not answer count as unpaired,
// the same best-effort rule every other pair check here follows.
func (s *Server) memberPairs(ctx context.Context, members []boxapi.ZoneMember) map[string]boxapi.Group {
	out := map[string]boxapi.Group{}
	for _, m := range members {
		if !s.mayBePairHalf(m.IP) {
			continue
		}
		if _, done := out[m.IP]; done {
			continue
		}
		gctx, cancel := context.WithTimeout(ctx, pairReadBudget)
		g, err := s.fetchGroup(gctx, m.IP)
		cancel()
		if err != nil {
			s.logger.Info("zone: could not read a member's stereo pair, taking it as a single speaker", "ip", m.IP, "err", err)
			continue
		}
		if groupIsPair(g) {
			out[m.IP] = g
		}
	}
	return out
}

// pairCannotLead is the refusal of a group led by a pair half: 409 with a
// JSON body, so a client that treats every 2xx as done (a phone remote) does
// not report a group that was never formed, and the desktop app can tell it
// from a firmware failure.
func (s *Server) pairCannotLead() *zoneFormAnswer {
	return &zoneFormAnswer{Status: http.StatusConflict, JSON: map[string]any{
		"ok": false, "code": pairCannotLeadCode, "inPair": []string{s.boxHost}, "error": pairCannotLeadMsg,
	}}
}

// zoneFormPairs is the stereo pair phase of a group form, between resolving
// the members and anything that is stored or driven (see the top of this
// file). It may drop the stereo flag of the request, replaces pair halves in
// run.slaves by their pair master, and answers the refusal when this box,
// the group's leader, is half of a pair.
func (s *Server) zoneFormPairs(run *zoneFormRun) *zoneFormAnswer {
	ctx := run.ctx
	own, inPair := s.ownPair(ctx)
	if run.req.Stereo {
		return nil // a pairing: the pair flow decides
	}
	if inPair {
		s.logger.Warn("zone: refused, the speaker asked to lead the group is half of a stereo pair (a pair can join a group but not lead one, #792)",
			"master", run.master.DeviceID, "pair", own.Name, "from", run.req.from)
		return s.pairCannotLead()
	}
	slaves, mapped, leadsPair := pairUnitMembers(run.master, s.boxHost, run.slaves, s.memberPairs(ctx, run.slaves))
	if leadsPair {
		s.logger.Warn("zone: refused, a member's stereo pair names this speaker, so the group would be led by half of a pair (#792)",
			"master", run.master.DeviceID)
		return s.pairCannotLead()
	}
	if len(mapped) > 0 {
		s.logger.Info("zone: a stereo pair joins the group as one member, addressed by its pair master",
			"mapped", strings.Join(mapped, ","), "slaves", len(slaves))
	}
	if len(slaves) == 0 {
		return zoneFormRefusal(http.StatusBadRequest, "at least one slave is required")
	}
	run.slaves = slaves
	return nil
}

// pairUnitJoiners is pairUnitMembers for a re-form of a stored group: a
// member that has become half of a pair since the group was saved (or a group
// saved with a pair's other half) is taken in as the pair, by its pair master,
// so a re-form never puts one half of a pair into the group on its own. A
// re-form led by a pair half is not this function's call: the play-time paths
// only ever re-form the stored group of a leader that was accepted.
func (s *Server) pairUnitJoiners(ctx context.Context, master boxapi.ZoneMember, joiners []boxapi.ZoneMember) []boxapi.ZoneMember {
	groups := s.memberPairs(ctx, joiners)
	if len(groups) == 0 {
		return joiners
	}
	out, mapped, _ := pairUnitMembers(master, s.boxHost, joiners, groups)
	if len(mapped) > 0 {
		s.logger.Info("default group: a stored member is half of a stereo pair, taking the pair in by its pair master",
			"mapped", strings.Join(mapped, ","))
	}
	if len(out) == 0 {
		return joiners
	}
	return out
}
