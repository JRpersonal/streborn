package webui

import (
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// A speaker that is half of a stereo pair whose other half is gone, or no longer
// knows about the pair, refuses every source activation. The firmware wakes it,
// drops it back into standby a few seconds later and nothing it is told to play
// ever starts.
//
// Before this, a recall on such a speaker woke it (up to 8 s), pointed it at the
// stream and waited for audio that could never come, all while holding the box
// command lock. The app's request timed out, it retried, and the retries queued
// up behind the lock. The owner saw "the speaker is not ready" on every key, for
// an hour, through a reinstall of STR and a reboot (mail, 2026-10-04: two
// SoundTouch 10s, the second still holding the pair document naming the first
// as master, the first holding nothing). The real reason was one click away in
// the Multi-Room tab, and nothing pointed there.
//
// So a recall now asks first, from what the agent already knows, and refuses at
// once with the reason. It never reads the firmware for this: /getGroup hangs on
// some chassis, and the zone poll has just read it anyway.

// stereoSeenFresh is how long a pair read from the zone poll counts as current
// for the play path. The desktop polls the zone every few seconds while it is
// open and the phone page reads it on its own; a quarter of an hour without any
// read means nobody is looking, and an old answer must not refuse a recall.
const stereoSeenFresh = 15 * time.Minute

// noteStereoSeen records the pair document from a /getGroup read that
// answered. g is nil when the speaker answered with no pair.
func (s *Server) noteStereoSeen(g *boxapi.Group) {
	s.stereoSeenMu.Lock()
	defer s.stereoSeenMu.Unlock()
	if g == nil {
		s.stereoSeen = nil
	} else {
		cp := *g
		cp.Members = append([]boxapi.ZoneMember(nil), g.Members...)
		s.stereoSeen = &cp
	}
	s.stereoSeenAt = time.Now()
}

// incompletePair describes why this speaker cannot play on its own.
type incompletePair struct {
	// Reason is "partner-gone" (the other half did not answer) or
	// "group-error" (the firmware itself flags the pair as broken).
	Reason string
	// Master is the pair's master deviceID as the document names it, when
	// known. PartnerIP / PartnerID name the other half.
	Master    string
	PartnerIP string
	PartnerID string
	Name      string
	// Members are the pair's members as the document lists them (group-error).
	Members []boxapi.ZoneMember
}

// incompletePairReason reports whether this speaker is stuck in an incomplete
// stereo pair. now is injectable for tests.
//
// Deliberately narrow, because refusing a recall on a HEALTHY pair would be far
// worse than the timeout this replaces: only the firmware's own GROUP_ERROR
// verdict on a fresh read, or the partner-gone finding the agent makes once at
// start, count. A pair document alone does not: the half that holds it is
// normal for a healthy pair.
func (s *Server) incompletePairReason(now time.Time) (incompletePair, bool) {
	s.stereoSeenMu.Lock()
	g, at := s.stereoSeen, s.stereoSeenAt
	s.stereoSeenMu.Unlock()
	if s.pairPartnerGone != nil {
		if ip, id := s.pairPartnerGone(); ip != "" {
			out := incompletePair{Reason: "partner-gone", PartnerIP: ip, PartnerID: id}
			if g != nil {
				out.Master, out.Name = g.MasterDeviceID, g.Name
			}
			return out, true
		}
	}
	if g == nil || now.Sub(at) > stereoSeenFresh {
		return incompletePair{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(g.Status), "GROUP_ERROR") {
		return incompletePair{}, false
	}
	// The agent does not know its own deviceID here (it reaches its speaker
	// over loopback), so it cannot tell which member is "the other half". The
	// caller knows which speaker it asked; the members go out as they are.
	return incompletePair{Reason: "group-error", Master: g.MasterDeviceID, Name: g.Name,
		Members: append([]boxapi.ZoneMember(nil), g.Members...)}, true
}

// refuseIfIncompletePair answers a play request with 409 stereo-incomplete when
// this speaker is stuck in an incomplete pair, and reports whether it did. Call
// it BEFORE the wake: waking such a speaker is exactly what cannot succeed.
func (s *Server) refuseIfIncompletePair(w http.ResponseWriter) bool {
	p, ok := s.incompletePairReason(time.Now())
	if !ok {
		return false
	}
	s.logger.Warn("play refused: this speaker is half of an incomplete stereo pair, the firmware will not start a source on it",
		"reason", p.Reason, "master", p.Master, "partnerIP", p.PartnerIP, "partnerID", p.PartnerID)
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":     "stereo-incomplete",
		"reason":    p.Reason,
		"master":    p.Master,
		"partnerIP": p.PartnerIP,
		"partnerID": p.PartnerID,
		"pairName":  p.Name,
		"members":   p.Members,
	})
	return true
}
