// A stereo flag on a group EXTEND, not a pairing.
//
// A phone remote (SoundRebornRemote) extends a group from the speaker it
// controls and, to keep what that speaker's zone says, sends its "stereo" back
// with the form. When that speaker is half of a stereo pair, the agent took
// the form for a pairing request and paired the half with the speaker being
// added, which took the user's pair apart instead of growing the group.
//
// So a stereo form from a speaker that is already half of a pair is a pairing
// only when it can be one; otherwise the flag is dropped and the form is a
// group form, which zoneFormPairs then answers (a pair half cannot lead a
// group, so it is refused with its reason, and nothing is re-paired).

package webui

import "github.com/JRpersonal/streborn/internal/boxapi"

// pairPartnerOf returns the other half of the pair self belongs to.
func pairPartnerOf(g boxapi.Group, self boxapi.ZoneMember, selfHost string) (boxapi.ZoneMember, bool) {
	for _, m := range g.Members {
		mm := boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP}
		if sameSpeaker(mm, self) || (selfHost != "" && m.IP == selfHost) {
			continue
		}
		return mm, true
	}
	return boxapi.ZoneMember{}, false
}

// stereoRequestIsPairing tells a real pairing request from a group extend
// that carries the stereo flag, for a speaker that is already half of a pair
// (own). Re-forming or editing this pair names its own partner; a pair has
// one partner, so two or more members are a group; and a member whose model
// cannot pair cannot be the other half of anything. A single other SoundTouch
// 10 (or a member of unknown model) stays a pairing request: re-pairing with
// a different speaker is a real thing to ask, and the firmware decides it.
func stereoRequestIsPairing(own boxapi.Group, self boxapi.ZoneMember, selfHost string, slaves []boxapi.ZoneMember, typeOf func(ip string) string) bool {
	if len(slaves) != 1 {
		return false
	}
	if partner, ok := pairPartnerOf(own, self, selfHost); ok && sameSpeaker(partner, slaves[0]) {
		return true
	}
	if t := typeOf(slaves[0].IP); t != "" && !pairableModel.MatchString(t) {
		return false
	}
	return true
}

// zoneFormStereoIsExtend reports whether a stereo form is really a group
// extend from half of a pair (own, inPair: this speaker's pair), and when it
// is, drops the stereo flag from the request. A speaker in no pair is always
// asked for a pairing.
func (s *Server) zoneFormStereoIsExtend(run *zoneFormRun, own boxapi.Group, inPair bool) bool {
	if !inPair {
		return false
	}
	typeOf := func(ip string) string { return s.cachedMemberType(ip) }
	if stereoRequestIsPairing(own, run.master, s.boxHost, run.slaves, typeOf) {
		return false
	}
	s.logger.Warn("zone: a group form from half of a stereo pair carried the stereo flag; treating it as a group form, not a pairing",
		"slaves", len(run.slaves), "pair", own.Name, "from", run.req.from, "ua", run.req.ua)
	run.req.Stereo = false
	return true
}
