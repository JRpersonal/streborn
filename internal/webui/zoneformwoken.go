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
	s.scheduleLateSelfResumeCheckThen(when, nil)
}

// scheduleLateSelfResumeCheckThen is scheduleLateSelfResumeCheck with one
// follow-up step that runs after the late look, on its own fresh deadline. The
// stereo path uses it to put a pair formed out of standby back to sleep once
// the firmware's own resume has been dealt with (afterSilentPair).
func (s *Server) scheduleLateSelfResumeCheckThen(when string, then func(context.Context)) {
	s.holdSpotifyAutoAttach(lateSelfResumeHold, when)
	go func() {
		time.Sleep(lateSelfResumeCheck)
		lctx, lcancel := context.WithTimeout(context.Background(), 5*time.Second)
		s.stopGroupWakeSelfResume(lctx, when)
		lcancel()
		if then == nil {
			return
		}
		// Give a STOP the late look may just have sent a moment to land, so
		// the follow-up reads the settled state rather than the old one.
		time.Sleep(pairStandbySettle)
		tctx, tcancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer tcancel()
		then(tctx)
	}()
}

// A stereo pair formed out of two sleeping speakers has to go back to sleep.
//
// The pairing wakes a standby master on purpose: pairing against a sleeping
// master let the firmware drag the fresh pair into standby and lose the
// partner's music (#705). The quiet wake then keeps the firmware's own resume
// from starting music (#1074), so the pair forms silent, as it should. But
// nothing put the master back afterwards, and it stayed on with no source,
// solid amber, until somebody switched it off by hand (#1074, two SoundTouch
// 10s on v1.0.10: "stereo: paired" and then nothing, the indicator never went
// out).
//
// A pair is one logical device to the firmware and the partner follows its
// master's power state (that is exactly what #705 measured), so standing the
// MASTER down is enough. The pair itself is a firmware group that survives
// standby; a native multiroom ZONE does not (the master dropping to STANDBY
// dissolves it, measured 2026-08-18), which is why the group form path does
// not do this: it would undo the group the user just created.

// pairStandbySettle is the pause between the late self-resume look and the
// standby decision.
const pairStandbySettle = time.Second

// pairStandbySend is the standby seam, for the same reason as groupWakeStop:
// the real call reaches :8090 on a fixed port.
var pairStandbySend = func(ctx context.Context, host string) error {
	return boxapi.New(host).Standby(ctx)
}

// pairStandbyIdleSources are the sources a woken master can show while it is
// merely idle: the station the firmware resumed by itself and STR stopped
// again. Anything else (AUX, Bluetooth, a soundbar's TV input) is a source
// somebody picked, and the speaker is left on for it.
var pairStandbyIdleSources = map[string]bool{
	"LOCAL_INTERNET_RADIO": true,
	"UPNP":                 true,
	"STORED_MUSIC":         true,
	"SPOTIFY":              true,
	"TUNEIN":               true,
	"INTERNET_RADIO":       true,
}

// pairStandbyDecision decides whether a master STR woke for a pairing may go
// back to standby now. Only a speaker that is readable, awake and doing nothing
// the user asked for qualifies: a play pushed through STR since the wake, any
// playing, buffering or paused transport, and any source the user could have
// selected on the speaker all keep it on.
func pairStandbyDecision(np nowPlayingSnapshot, userPlayed bool) (bool, string) {
	switch {
	case userPlayed:
		return false, "the user started something since the wake"
	case np.Source == "":
		return false, "the speaker's state could not be read"
	case np.Source == "STANDBY":
		return false, "already in standby"
	}
	switch np.PlayStatus {
	case "PLAY_STATE", "BUFFERING_STATE", "PAUSE_STATE":
		return false, "something is playing on the pair"
	}
	if np.Source == "INVALID_SOURCE" {
		return true, "awake with no source selected"
	}
	if pairStandbyIdleSources[np.Source] && np.PlayStatus == "STOP_STATE" {
		return true, "awake with a stopped source"
	}
	return false, "a source the user may have selected is active"
}

// afterSilentPair is what follows a pair that formed with nothing to restart.
// Always the late self-resume look; and when STR pulled both halves out of
// standby for this pairing, the master goes back to standby after it, which
// takes the partner with it. since is the earliest moment a play by the user
// counts as "started after the wake".
func (s *Server) afterSilentPair(bothWereAsleep bool, since time.Time) {
	if !bothWereAsleep {
		s.scheduleLateSelfResumeCheck("after pairing")
		return
	}
	s.scheduleLateSelfResumeCheckThen("after pairing", func(ctx context.Context) {
		s.returnSilentPairToStandby(ctx, since)
	})
}

// returnSilentPairToStandby puts a master STR woke for a pairing back to
// standby, unless the user has started something on it since. One read, at
// most one standby call. It reports whether the standby was sent.
//
// The self-wake guard stays armed on purpose: the group-wake episode is left
// running and the pair's stored document still marks this speaker as half of
// a pair, so the next power-on does not auto-resume anything. The standby is
// noted as a deliberate stop, so the source drop it causes is not taken for a
// spontaneous firmware drop and recovered (#419).
func (s *Server) returnSilentPairToStandby(ctx context.Context, since time.Time) bool {
	if s.boxHost == "" {
		return false
	}
	if active, woke := s.groupWakeEpisode(); active && woke.Before(since) {
		since = woke
	}
	np := groupWakeNowPlaying(ctx, s.boxHost)
	ok, why := pairStandbyDecision(np, s.userPlayedSince(since))
	if !ok {
		s.logger.Info("stereo: leaving the pair on after pairing it out of standby",
			"reason", why, "source", np.Source, "playStatus", np.PlayStatus)
		return false
	}
	// The level the quiet wake muted from goes back first, while the speaker
	// is still awake: the restore timer would otherwise write it into a
	// sleeping speaker, and on some chassis a request into a sleeping box is
	// what wakes it.
	s.restoreQuietWakeVolume("pair back to standby")
	s.NoteUserStop()
	if err := pairStandbySend(ctx, s.boxHost); err != nil {
		s.logger.Warn("stereo: could not put the pair back to standby after pairing it out of standby", "err", err)
		return false
	}
	s.logger.Info("stereo: pair formed out of standby, put it back to standby",
		"reason", why, "source", np.Source, "playStatus", np.PlayStatus)
	return true
}
