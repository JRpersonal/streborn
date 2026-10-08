package webui

import (
	"context"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// Forming a group out of idle speakers must stay silent. The speakers are
// asleep, STR wakes them for the group, and the firmware's power-on resumes
// each speaker's OWN last source. The quiet wake mutes that and sends one STOP
// right after the speaker reports awake, but the resume can start a moment
// later than that check. Fleet run 2026-10-04: a normal group of two idle
// SoundTouch 10s started the master's last station and it kept playing after
// the group was dissolved.
//
// Two places then turned the firmware's own resume into the group's music:
//
//   - the master capture: a busy master is assumed to be playing what the user
//     wants, so masterResumeForZone carried its stream over and pushed it to
//     the fresh group (bbc47347 made that the default for any stream STR did
//     not start, which is exactly what a self-resumed native station is);
//   - the member capture: a master with nothing to play takes over the stream
//     a member is playing (memberResumeForZone, aa01c098), and a member the app
//     just woke for this group is playing its own self-resume, not music.
//
// What a speaker plays inside the window after STR woke it for a group is the
// firmware's doing, unless the user started something through STR since.

// wokenMemberSet indexes the member addresses the app reports it woke out of
// standby for this group (zoneFormReq.WokenFromStandby).
func wokenMemberSet(ips []string) map[string]bool {
	out := make(map[string]bool, len(ips))
	for _, ip := range ips {
		if ip = strings.TrimSpace(ip); ip != "" {
			out[ip] = true
		}
	}
	return out
}

// membersNotWokenForGroup drops the members that were asleep until this group
// woke them: whatever they play is their own power-on resume, and the master
// must not carry it into every room.
func membersNotWokenForGroup(members []boxapi.ZoneMember, woken map[string]bool) []boxapi.ZoneMember {
	if len(woken) == 0 {
		return members
	}
	out := make([]boxapi.ZoneMember, 0, len(members))
	for _, m := range members {
		if woken[strings.TrimSpace(m.IP)] {
			continue
		}
		out = append(out, m)
	}
	return out
}

// isSelfResume reports whether the master's current state is the firmware's
// own resume after STR woke it for a group: STR woke it inside the episode
// window, it is producing (or about to produce) sound, and the user has not
// started anything through STR since the wake.
func isSelfResume(wokenForGroup, userPlayedSinceWake bool, np nowPlayingSnapshot) bool {
	if !wokenForGroup || userPlayedSinceWake {
		return false
	}
	if np.Source == "" || np.Source == "STANDBY" {
		return false
	}
	switch np.PlayStatus {
	case "PLAY_STATE", "BUFFERING_STATE":
		return true
	}
	return false
}

// groupWakeEpisode reports whether STR woke this speaker for a group operation
// inside the episode window, and when that wake happened.
func (s *Server) groupWakeEpisode() (active bool, since time.Time) {
	s.quietWakeMu.Lock()
	defer s.quietWakeMu.Unlock()
	if !time.Now().Before(s.quietWakeEpisodeUntil) {
		return false, time.Time{}
	}
	return true, s.quietWakeEpisodeUntil.Add(-quietWakeEpisodeWindow)
}

// userPlayedSince reports whether STR pushed a play to this speaker after t,
// i.e. the user started something after the group wake. A native station the
// firmware activated by itself does not count: inside a group wake that is its
// power-on resume (NoteNativeLastPlay).
func (s *Server) userPlayedSince(t time.Time) bool {
	s.lastPlayMu.Lock()
	defer s.lastPlayMu.Unlock()
	return s.lastPlay != nil && !s.lastPlay.fromBox && s.lastPlay.ts.After(t)
}

// groupWakeNowPlaying is the read seam for stopGroupWakeSelfResume, the same
// shape as quietWakeNowPlaying: the real read reaches :8090 on a fixed port.
var groupWakeNowPlaying = func(ctx context.Context, host string) nowPlayingSnapshot {
	return fetchNowPlaying(ctx, host)
}

// groupWakeStop is the STOP seam, for the same reason.
var groupWakeStop = func(ctx context.Context, host string) error {
	return boxapi.New(host).Key(ctx, "STOP")
}

// stopGroupWakeSelfResume stops the master's own power-on resume when STR woke
// it for this group, and reports whether it did. One read, at most one key.
func (s *Server) stopGroupWakeSelfResume(ctx context.Context, when string) bool {
	active, since := s.groupWakeEpisode()
	if !active || s.boxHost == "" {
		return false
	}
	np := groupWakeNowPlaying(ctx, s.boxHost)
	if !isSelfResume(true, s.userPlayedSince(since), np) {
		return false
	}
	if err := groupWakeStop(ctx, s.boxHost); err != nil {
		s.logger.Warn("zone: could not stop the master's own resume after the group wake", "when", when, "err", err)
		return false
	}
	s.logger.Info("zone: stopped what the master resumed by itself after STR woke it for the group",
		"when", when, "source", np.Source, "location", np.Location)
	return true
}

// lateSelfResumeCheck is how long after a silent form the master is looked at
// once more. The firmware's resume can start after the zone formed; a single
// read at this point catches it without a timer that keeps polling the box.
const lateSelfResumeCheck = 4 * time.Second

// masterPlaysUsersMusic is the "is the master playing the user's music" test
// both form paths run before they capture anything to restart on the fresh
// group or pair. A master STR woke for this group is playing its own power-on
// resume, so that is stopped and does not count as busy. When the master is
// NOT playing the user's music (asleep, idle, or only self-resumed), the
// Spotify engine's auto-attach is held off for hold: in #1074 the engine,
// still holding the album the user had played before pressing standby, pulled
// the box onto STR's Spotify stream 300 ms after the pairing woke it, so the
// pair formed around music nobody started. A master that IS playing is left
// alone, auto-attach included, so a pair or group formed mid-song keeps it.
func (s *Server) masterPlaysUsersMusic(ctx context.Context, when string, hold time.Duration) bool {
	_, busy := s.boxPlayState()
	if busy && s.stopGroupWakeSelfResume(ctx, when) {
		busy = false
	}
	if !busy {
		s.holdSpotifyAutoAttach(hold, when)
	}
	return busy
}

// holdSpotifyAutoAttach keeps the Spotify engine from pointing the box at its
// stream for d. Only ever extends an existing hold (Manager.SuppressActivate),
// so the callers can re-arm it at each step of a long form without cutting a
// longer one short. No-op when Spotify is not configured.
func (s *Server) holdSpotifyAutoAttach(d time.Duration, when string) {
	if s.spotifySuppressActivate == nil || d <= 0 {
		return
	}
	s.spotifySuppressActivate(d)
	s.logger.Info("zone: holding the Spotify auto-attach off while the group forms silent", "when", when, "for", d)
}

// lateSelfResumeHold is how long the Spotify auto-attach stays held after a
// silent form: up to and a little past the one late look at the master.
const lateSelfResumeHold = lateSelfResumeCheck + 2*time.Second

// stereoPairAutoAttachHold is one step of the Spotify auto-attach hold while a
// pair forms silent. formStereoPair arms it at the start, before /addGroup and
// before the two-sided verify; each of those steps is bounded by about 30 s
// (the quiet wake's whole budget, the /addGroup budget, the verify budget plus
// its slack), and the hold only ever extends, so the chain covers the pairing
// end to end and hands over to the late check's own hold.
const stereoPairAutoAttachHold = 30*time.Second + lateSelfResumeHold

// scheduleLateSelfResumeCheck gives a group or pair that formed silent one
// more look after lateSelfResumeCheck: the firmware's resume can start after
// the zone formed, and the zone then carries it into every room. One read, at
// most one STOP, no repeating timer. The Spotify auto-attach is held across
// the wait so the engine cannot fill the silence in the meantime.
func (s *Server) scheduleLateSelfResumeCheck(when string) {
	s.holdSpotifyAutoAttach(lateSelfResumeHold, when)
	go func() {
		time.Sleep(lateSelfResumeCheck)
		lctx, lcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer lcancel()
		s.stopGroupWakeSelfResume(lctx, when)
	}()
}
