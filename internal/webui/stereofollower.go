package webui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// A play started on the FOLLOWING half of a healthy stereo pair did nothing.
//
// The follower's firmware takes SetAVTransportURI and Play without an error and
// then ignores both, because in a pair only the master plays a source and the
// follower renders what the master sends it. STR only ever learned "this
// speaker follows a group" from the firmware's 501 "Can't control member of
// group", which a multiroom zone follower answers and a stereo follower never
// does. So the phone page of the following ST10 woke the speaker, pushed a
// media-server track, answered "playing", and the room went on hearing the
// music from before (mail, 2026-10-26: two SoundTouch 10s in a pair, three taps
// on one track from the following half's phone page, no error anywhere).
//
// The agent knows the role: the pair record in marge names the master, and the
// speaker's own deviceID is known. So every play path now asks first and, on a
// follower, refuses before the wake with the same 409 box-grouped answer a zone
// follower gets, plus stereo:true and where the master is. The request is not
// forwarded to the master's agent: two ST10s often cannot reach each other at
// all (peer isolation), and a forward that silently fails is the same bug again.
//
// A broken pair (partner gone, GROUP_ERROR) is refused earlier with
// stereo-incomplete; this only ever answers for a pair whose master is there.

// StereoFollower is the answer of the stereo-pair role check. Follower is false
// for a standalone speaker, for the pair's master, and whenever the role cannot
// be decided (no pair record, own deviceID not known yet).
type StereoFollower struct {
	Follower bool
	// MasterID is the master's deviceID as the pair record names it.
	MasterID string
	// MasterIP is the master's LAN address from the record's role entry, empty
	// when the record carries none.
	MasterIP string
	// PairName is the pair's display name.
	PairName string
}

// stereoFollowerPeerBudget caps the peer-list lookup that puts a name and a
// link on the refusal. The list is cached and normally answers at once; a cold
// one must not hold the refusal (and the box command lock) for a whole browse.
const stereoFollowerPeerBudget = time.Second

// stereoFollower asks the wired role check. Zero value when none is wired.
func (s *Server) stereoFollower() StereoFollower {
	if s.stereoFollowerFn == nil {
		return StereoFollower{}
	}
	return s.stereoFollowerFn()
}

// stereoFollowerRefusal is the 409 box-grouped refusal for the following half
// of a stereo pair, or nil when this speaker can be played on directly. Call it
// BEFORE the wake: waking the follower costs a Spotify pause and a source
// switch for a play that cannot happen.
func (s *Server) stereoFollowerRefusal() *playRefusal {
	f := s.stereoFollower()
	if !f.Follower {
		return nil
	}
	name, link := s.stereoMasterPeer(f)
	s.logger.Info("play refused: this speaker is the following half of a stereo pair, only the pair's master can start playback",
		"master", f.MasterID, "masterIP", f.MasterIP, "masterName", name, "pair", f.PairName)
	return jsonRefusal(http.StatusConflict, map[string]any{
		"error":      "box-grouped",
		"stereo":     true,
		"master":     f.MasterID,
		"masterIP":   f.MasterIP,
		"masterName": name,
		"masterURL":  link,
		"pairName":   f.PairName,
	})
}

// refuseIfStereoFollower answers a play request with the follower refusal and
// reports whether it did.
func (s *Server) refuseIfStereoFollower(w http.ResponseWriter) bool {
	ref := s.stereoFollowerRefusal()
	if ref != nil {
		writePlayRefusal(w, ref)
	}
	return ref != nil
}

// pairPlayRefusal is the one question every play path asks before it wakes the
// speaker: half of a broken pair (stereo-incomplete), or the following half of
// a healthy one (box-grouped, stereo). nil when the speaker plays on its own.
func (s *Server) pairPlayRefusal() *playRefusal {
	if ref := s.incompletePairRefusal(); ref != nil {
		return ref
	}
	return s.stereoFollowerRefusal()
}

// stereoMasterPeer finds the master in the STR peer list, for its display name
// and the address of its phone page. Matched on deviceID first, then on the
// address the pair record names. Empty when the master is not listed.
func (s *Server) stereoMasterPeer(f StereoFollower) (name, link string) {
	if s.peersFn == nil {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), stereoFollowerPeerBudget)
	defer cancel()
	for _, p := range s.peersFn(ctx) {
		host := p.IP
		if host == "" {
			if u, err := url.Parse(p.URL); err == nil {
				host = u.Hostname()
			}
		}
		byID := f.MasterID != "" && strings.EqualFold(strings.TrimSpace(p.DeviceID), strings.TrimSpace(f.MasterID))
		byIP := f.MasterIP != "" && host == f.MasterIP
		if byID || byIP {
			return strings.TrimSpace(p.Name), p.URL
		}
	}
	return "", ""
}
