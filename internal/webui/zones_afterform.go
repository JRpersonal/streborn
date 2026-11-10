// After a group or a pair formed: restarting what was playing, the timings
// of that restart, and what a member was playing before it joined. Split
// out of zones_stereo.go.

package webui

import (
	"context"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

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
	// members are the speakers this change actually put in the group, so the
	// resume can ASK them whether they are already hearing the master instead
	// of inferring it from the master alone. Empty means "not known", which is
	// read as not-served and therefore always pushes.
	members []boxapi.ZoneMember
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

// captureMasterResume snapshots this box's own last-play entry.
//
// It is the staleness reference for every group-change re-push, and it is
// deliberately not the same thing as the stream being pushed. The push may come
// from a MEMBER (#954) or from the stereo PARTNER (#705); comparing one of those
// against the master's live entry made every master that had ever played
// anything look superseded, so the takeover was captured and then dropped 1.5 s
// later and the group formed with nothing to play (#965).
func (s *Server) captureMasterResume() *lastPlayInfo {
	cp, ok := s.play.snapshot()
	if !ok {
		return nil
	}
	return &cp
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

// zoneTiming is the zone clock: the waits the group paths sleep through. A
// struct on the Server so a test can install a faster one without touching a
// package-level variable (a goroutine still reading it after the test's cleanup
// restored it is a data race); the production values are in
// productionZoneTiming.
type zoneTiming struct {
	// mirrorKickDelay is how long kickMirrorAfterPlay waits before the round. A
	// variable so the tests can shorten it; the value is the production one.
	mirrorKickDelay time.Duration
	// formResumeSettle is the settle before resumeAfterZoneForm's push. A
	// variable so the tests can shorten it; the value is the production one.
	formResumeSettle time.Duration
	// joinVolumeConfirmAfter is the pause before matchNewMembersToMasterVolume's
	// confirming read. A variable so the tests can shorten it; the value is the
	// production one.
	joinVolumeConfirmAfter time.Duration
	// defaultGroupHoldCheckAfter is how long after a re-form the master looks
	// whether the zone is still standing. A member that was just woken can fall
	// out a second or two after joining: its own power-on resume fails late, the
	// firmware raises the error after the join and the member drops to
	// INVALID_SOURCE, which takes the zone down with it (Küche, 2026-10-17
	// 09:51). On the zone clock so the tests do not wait for it.
	defaultGroupHoldCheckAfter time.Duration
}

var productionZoneTiming = zoneTiming{
	mirrorKickDelay:            6 * time.Second,
	formResumeSettle:           1500 * time.Millisecond,
	joinVolumeConfirmAfter:     2500 * time.Millisecond,
	defaultGroupHoldCheckAfter: 8 * time.Second,
}

// zoneTimes is the zone clock: the production values unless a test installed a
// faster one.
func (s *Server) zoneTimes() *zoneTiming {
	if s.zoneTiming != nil {
		return s.zoneTiming
	}
	return &productionZoneTiming
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
	time.Sleep(s.zoneTimes().formResumeSettle)
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
	// What the master actually reports once the form has settled, on EVERY path.
	//
	// Until now this state was read only INSIDE the survivor branch below, so a
	// fresh full form left no trace of it at all: a bundle could say the group
	// was formed and that a re-push followed, and nothing about whether the
	// master had kept playing through it. That is exactly the question a user
	// reporting "forming a two-speaker group interrupts the music" needs
	// answered, and answering it from the code alone is guesswork, because
	// whether /setZone tears the master's session down is a property of the
	// chassis and differs between them.
	//
	// Read-only and deliberately outside the decision: the branch below is
	// unchanged, so this cannot alter what anybody's speakers do. It only means
	// the next diagnostic can tell a firmware tear-down apart from our own
	// re-push, which today it cannot.
	npCtx, npCancel := context.WithTimeout(context.Background(), 5*time.Second)
	settleNP := s.readBoxSnapshot(npCtx)
	npCancel()
	s.logger.Info("zone: master state after the form settle",
		"incremental", rz.survivorReachesMembers,
		"source", settleNP.Source, "playStatus", settleNP.PlayStatus,
		"location", settleNP.Location, "wouldPush", lp.boxURL)

	if standby, busy := s.boxPlayState(); busy && !standby {
		if rz.survivorReachesMembers {
			// A confirmed incremental join, or a firmware stereo pair: the
			// caller already knows the surviving stream reaches everyone.
			s.logger.Info("zone: stream survived the group change, not restarting playback")
			return
		}
		// A FRESH form. The master is still playing, which is precisely why the
		// push hurts: it stops a stream nobody asked to stop (measured on a
		// SoundTouch 10, 2026-09-24: PLAY_STATE 1.5 s after the form, push sent
		// anyway, ~2.5 s of silence). It was made unconditional because a skip
		// here once left freshly joined members silent while the master played
		// on (Martin, 2026-08-24), and from the master alone those two states
		// are indistinguishable.
		//
		// So ask, in two steps, and push on anything short of a clear yes.
		//
		// First the master: is it still on the very stream we were about to
		// push? This CANNOT be a string compare. A natively started station
		// reports an orion-encoded location while lastPlay holds the loopback
		// proxy URL, and the same station therefore looks different in the two
		// forms (live capture, 2026-09-24). masterResumeForZone is the pure
		// helper that unpacks it, and it is the one the capture already used.
		stillOurs, blocked, _ := masterResumeForZone(settleNP, rz.ref)
		if !blocked && stillOurs != nil && stillOurs.boxURL == lp.boxURL {
			// Then the members, because Martin's case and this one differ only
			// in what THEY are doing.
			served, why := s.everyMemberCarriesTheGroup(context.Background(), rz.members, settleNP.Location)
			if served {
				s.logger.Info("zone: the stream survived the form and every member carries it, not restarting playback",
					"members", len(rz.members), "reason", why)
				return
			}
			s.logger.Info("zone: restarting after the form, a member is not carrying the stream yet",
				"members", len(rz.members), "reason", why)
		}
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	cur := s.play.current()
	if resumeRefSuperseded(rz.ref, cur) {
		s.logger.Info("zone: not restarting playback after forming, a newer play superseded it",
			"captured", lp.boxURL, "current", lastPlayURL(cur))
		return
	}
	if s.spotifyExpectReattach != nil && looksLikeSpotifyStreamURL(lp.boxURL) {
		// Carrying a live Spotify session into the group detaches and
		// re-attaches the Ogg sink within a second or two, which is the exact
		// shape ServeOgg damps as a re-point storm. Mark it as ours.
		s.spotifyExpectReattach(15 * time.Second)
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

// memberResumeForZone derives the stream a freshly formed zone should play when
// the MASTER has nothing of its own to resume but a member is audibly playing.
//
// Forming a group makes the master the source, and /setZone takes the members'
// own sessions down. When the master was the one playing, the capture above
// covers it. When a MEMBER was playing, nobody covered it: the member's station
// was torn down, the re-push on that member correctly stood down because its
// group had just changed, and the master, now the group's source, was never
// given anything to play. The whole group went silent with an amber indicator
// and no error anywhere (#954, three SoundTouch 10s on v0.9.81: master formed
// cleanly at 14:43:53, the player logged "a native station ended right after
// OUR preset write" at 14:43:54 and nothing followed).
//
// This is the same problem the stereo pair had in #705 and the same derivation
// solves it, so partnerResumeForPair is shared rather than copied: the location
// a native station carries is unwrapped, a UPnP push carries its URL directly,
// and the loopback address is rewritten to the member's own so the master can
// fetch it. Verified on hardware 2026-09-13 that a speaker's firmware really
// does fetch a stream through another speaker's agent, including between two
// rhino SoundTouch 10s.
//
// The first audibly playing member wins. With more than one there is no right
// answer to compute, and the alternative, silence, is the outcome being fixed.
// memberNowPlaying is the read seam (memberNowPlayingFn), the same shape
// hushReadNowPlaying uses: the real one reaches :8090 on a fixed port, so a
// test without it would only ever exercise the "nothing there" branch.
func (s *Server) memberNowPlaying(ctx context.Context, host string) nowPlayingSnapshot {
	if s.memberNowPlayingFn != nil {
		return s.memberNowPlayingFn(ctx, host)
	}
	return fetchNowPlaying(ctx, host)
}

func (s *Server) memberResumeForZone(ctx context.Context, members []boxapi.ZoneMember) *lastPlayInfo {
	for _, m := range members {
		ip := strings.TrimSpace(m.IP)
		if ip == "" || ip == s.boxHost {
			continue
		}
		if r := partnerResumeForPair(s.memberNowPlaying(ctx, ip), ip); r != nil {
			s.logger.Info("zone: the master has nothing to play, taking over the stream a member was playing",
				"member", ip, "title", r.title)
			return r
		}
	}
	return nil
}
