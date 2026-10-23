// Post-recall verification: confirming the box really switched streams.

package webui

import (
	"context"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// verifyRecall confirms the box reached a playing state ON THE EXPECTED STREAM
// shortly after a recall and re-issues the play a few times if not. Fixes the
// "first press after a reboot does nothing, second press works" race
// (box/go-librespot not ready yet) without any latency on the happy path (the
// initial play already ran).
//
// expectedLocation is the box-side URL this recall pushed (e.g.
// boxurl.StreamSlot(slot)). "Busy" alone used to count as success, which hid
// exactly the #252 failure where a racing wake-resume replaced a just-issued
// recall with the PREVIOUS station: the box was playing, just the wrong
// stream. Now a busy box on a different location is re-issued like a silent
// one. Empty expectedLocation keeps the busy-only check.
//
// retry receives lastAttempt=true only on the final try. Spotify uses it to
// re-point the box without a full re-Play on the early tries (a re-Play
// reshuffles and restarts the track), reserving the disruptive re-Play for the
// last-resort recovery. This is the same policy the hardware recall settled on
// in v0.7.4 (cmd/agent verifySpotifyPlaying); routing both through one contract
// keeps the soft and hardware paths from drifting again.
//
// gen is the recall generation returned by this recall's setLastPlay and
// started is when the recall was issued. They feed the stand-down decision
// (verifyStandDownReason): a newer play supersedes this verify (two rapid
// preset presses used to spawn dueling verifies that ping-ponged the stations
// for ~15s), and a deliberate user stop/pause/power-off after `started` ends
// it (the retry used to audibly restart a station the user had just stopped,
// and re-armed the transport of a box the user had just powered off - the
// #197 vector the hardware verifies were already guarded against).
func (s *Server) verifyRecall(gen uint64, started time.Time, expectedLocation string, retry func(ctx context.Context, lastAttempt bool), working func() bool) {
	const attempts = 3
	nudged := false
	for attempt := 1; attempt <= attempts; attempt++ {
		time.Sleep(5 * time.Second)
		if reason := s.recallStandDownReason(gen, started); reason != "" {
			s.logger.Info("recall verify: standing down", "reason", reason, "attempt", attempt)
			return
		}
		// working() is a source-specific "it is already fine" signal checked
		// before the box now_playing state. For Spotify it reports whether the
		// box is pulling the Ogg stream: now_playing flaps while the box
		// attaches, and a re-issue would reshuffle + restart the track (the
		// audible abort + UI play/stop/play flicker). Don't retry when working.
		if working != nil && working() {
			s.NoteBoxHealthy()
			return
		}
		location, busy := s.boxPlayLocation()
		if busy && recallLocationMatches(expectedLocation, location) {
			s.NoteBoxHealthy()
			return
		}
		// The box just rejected the source because it is not signed in (1036).
		// Re-pushing the same UPnP source only flaps it and can wedge the box
		// (Michal's ST300). A forced re-login was already kicked off by the
		// not-logged-in signal; stand this retry loop down and let the next user
		// recall land once the box is signed back in - no thrashing, no manual
		// "re-pair" step for the user.
		if s.recentLoginError() {
			s.logger.Warn("recall verify: box reported not-logged-in; standing down the retry (a re-login was triggered) instead of re-pushing and risking a wedge",
				"attempt", attempt)
			return
		}
		if busy {
			s.logger.Warn("recall verify: box is playing a different stream than this recall pushed, re-issuing",
				"attempt", attempt, "expected", expectedLocation, "playing", location)
		} else {
			s.logger.Warn("recall did not reach playing, retrying", "attempt", attempt)
		}
		// Last resort before the final re-push: a box parked in INVALID_SOURCE
		// that has fetched nothing since we pushed is not "starting slowly", it
		// is the dead-source state, and re-pushing into it has already failed
		// twice. The hardware-key path has had this escalation for a while; a
		// play started from the app or the phone had none, so a Wave in this
		// state answered every single request with silence until its owner
		// pulled the plug (2026-07-28: seven plays, not one stream fetch, no
		// escalation anywhere in the log).
		//
		// The guard is deliberately narrow, because `sys power` is a TOGGLE and
		// sending it to a healthy box switches it OFF: only on the last attempt,
		// only once, only while the box reports exactly INVALID_SOURCE (no source
		// selected, so there is no playback to interrupt), and only when the
		// stream proxy has served nothing since this recall started.
		if attempt == attempts && !nudged && s.recallBoxInert(started) {
			nudged = true
			s.logger.Warn("recall verify: box sits in INVALID_SOURCE and fetched nothing since the push; sending one power nudge before the final retry")
			s.nudgeDeadSource()
		}
		// Serialize the re-push with every other box command: the Bose
		// firmware mishandles concurrent writes (see boxCmdMu), and a retry
		// SOAP call landing during a volume PUT re-created exactly the
		// collision the mutex was added to prevent.
		s.boxCmdMu.Lock()
		// Re-decide under the lock: a newer play or a user stop may have
		// landed while this retry waited for it, and pushing now would
		// clobber that newer intent.
		if reason := s.recallStandDownReason(gen, started); reason != "" {
			s.boxCmdMu.Unlock()
			s.logger.Info("recall verify: standing down", "reason", reason, "attempt", attempt)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		retry(ctx, attempt == attempts)
		cancel()
		s.boxCmdMu.Unlock()
	}
	// The final re-push was never looked at: the loop ends right after it. Give
	// it one settle beat before declaring the recall dead, so a last attempt
	// that did take is not followed by a clean-up that stops it.
	time.Sleep(exhaustedRecallSettle)
	if reason := s.recallStandDownReason(gen, started); reason != "" {
		s.logger.Info("recall verify: standing down", "reason", reason, "attempt", "final")
		return
	}
	if working != nil && working() {
		s.NoteBoxHealthy()
		return
	}
	if location, busy := s.boxPlayLocation(); busy && recallLocationMatches(expectedLocation, location) {
		s.NoteBoxHealthy()
		return
	}
	s.logger.Warn("recall still not playing after retries")
	// Wedge detection (#power-cycle hint): decide whether this exhaustion
	// looks like the box rather than the station, and count it. Before the
	// clean-up below, which changes the source this reads.
	s.NoteRecallExhausted()
	s.settleExhaustedRecall(gen, started)
}

// exhaustedRecallSettle is the beat between the final re-push and the last look
// at it. A variable so the tests do not wait for real seconds.
var exhaustedRecallSettle = 5 * time.Second

// exhaustedStandbySend is the standby seam for settleExhaustedRecall, for the
// same reason as pairStandbySend: the real call reaches :8090 on a fixed port.
var exhaustedStandbySend = func(ctx context.Context, host string) error {
	return boxapi.New(host).Standby(ctx)
}

// settleExhaustedRecall leaves the speaker in a clean state after a recall that
// ran out of retries, and reports what it did ("standby", "stop" or "" for
// nothing).
//
// Until now an exhausted recall simply ended, and the speaker stayed where the
// failed pushes had left it: in INVALID_SOURCE, which an ST10 shows as a solid
// amber light, with the last-resort power nudge having woken it on the way
// (#1314, an ST10 stereo master that refused every UPnP push). Nothing plays
// in that state and nothing the user sees explains it. So:
//
//   - INVALID_SOURCE (no source selected, nothing to interrupt): back to
//     standby, the state an idle speaker is normally in.
//   - UPNP (still on STR's transport, buffering a stream that never came):
//     stopped, as abandonSessionlessRecall does for a Spotify recall without a
//     login, so it does not sit buffering.
//   - STANDBY, an unreadable source, or anything else: left alone. A sleeping
//     speaker is never woken, and any other source is one the user picked.
//
// A newer play or a user stop/power-off since the press owns the speaker, and
// is never undone. The clean-up is recorded as a deliberate stop so the drop it
// causes is not read as a spontaneous firmware drop and re-pushed (#419).
func (s *Server) settleExhaustedRecall(gen uint64, started time.Time) string {
	if reason := s.recallStandDownReason(gen, started); reason != "" {
		return ""
	}
	src := strings.TrimSpace(s.boxSourceNow())
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	switch src {
	case "INVALID_SOURCE":
		if s.boxHost == "" {
			return ""
		}
		s.boxCmdMu.Lock()
		defer s.boxCmdMu.Unlock()
		if reason := s.recallStandDownReason(gen, started); reason != "" {
			return ""
		}
		s.NoteUserStop()
		if err := exhaustedStandbySend(ctx, s.boxHost); err != nil {
			s.logger.Warn("recall verify: could not put the speaker back to standby after the failed recall", "err", err)
			return ""
		}
		s.logger.Info("recall verify: the failed recall left the speaker with no source, put it back to standby instead of leaving it there")
		return "standby"
	case "UPNP":
		if s.renderer == nil {
			return ""
		}
		s.boxCmdMu.Lock()
		defer s.boxCmdMu.Unlock()
		if reason := s.recallStandDownReason(gen, started); reason != "" {
			return ""
		}
		s.NoteUserStop()
		if err := s.renderer.Stop(ctx); err != nil {
			s.logger.Warn("recall verify: stopping the speaker after the failed recall did not work", "err", err)
			return ""
		}
		s.logger.Info("recall verify: the failed recall left the speaker buffering, stopped it")
		return "stop"
	default:
		return ""
	}
}

// recallStandDownReason gathers the live inputs for verifyStandDownReason: the
// current recall generation and the last user-stop / power-off stamps.
func (s *Server) recallStandDownReason(gen uint64, started time.Time) string {
	s.lastPlayMu.Lock()
	curGen := s.recallGen
	s.lastPlayMu.Unlock()
	s.lastUserStopMu.Lock()
	userStop := s.lastUserStop
	s.lastUserStopMu.Unlock()
	s.standbyStopMu.Lock()
	standbyStop := s.lastStandbyStop
	s.standbyStopMu.Unlock()
	return verifyStandDownReason(gen, curGen, started, userStop, standbyStop)
}

// verifyStandDownReason decides whether an in-flight recall verify must abort
// instead of re-pushing its stream, and why (empty = keep verifying). Pure so
// the policy is unit-testable:
//
//   - A newer play bumped the recall generation: this verify is superseded.
//     Re-pushing would yank the box off the stream the user chose afterwards.
//   - The box was powered off after the recall started (#197): a re-push
//     re-arms the transport and scm ST20 firmware switches back on with it.
//   - The user deliberately stopped/paused after the recall started: the
//     retry would audibly restart what they just stopped. Compared against
//     the recall start, NOT the rolling userStopWindow: that 6s window
//     expires between the verify's 5/10/15s ticks, so a stop at t=1s would
//     have been forgotten by the t=10s retry.
//
// Stops that PRECEDE the recall never stand it down: recalling a preset right
// after stopping another station is a normal action whose verify must run.
func verifyStandDownReason(recallGen, curGen uint64, recallStart, lastUserStop, lastStandbyStop time.Time) string {
	switch {
	case curGen != recallGen:
		return "superseded by a newer play"
	case !lastStandbyStop.IsZero() && lastStandbyStop.After(recallStart):
		return "box powered off after the recall (#197)"
	case !lastUserStop.IsZero() && lastUserStop.After(recallStart):
		return "user stopped playback after the recall"
	}
	return ""
}

// recallLocationMatches reports whether the box's now-playing location is the
// stream a recall pushed. Lenient on missing data: with no expectation or no
// readable location it returns true, so a now_playing read hiccup can never
// turn the verify into a retry storm. Only a POSITIVE "the box is on a
// different URL" counts as a mismatch (#252).
func recallLocationMatches(expected, nowLocation string) bool {
	if expected == "" || nowLocation == "" {
		return true
	}
	return nowLocation == expected
}

// xmlAttrUnescape decodes the predefined XML entities the box emits in
// attribute values (the Wi-Fi profile store, location="...&amp;..."), so
// comparisons see real values, not encodings.
func xmlAttrUnescape(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'")
	return r.Replace(s)
}

// boxPlayLocation reads now_playing once and reports the ContentItem location
// the box is tuned to (entity-decoded, so the verify compares real URLs) plus
// whether it is busy (playing, buffering or paused). Best-effort: ("", false)
// when the box cannot be read; verifyRecall treats an unreadable location as
// a match, so this can only ever ADD a justified retry, never a spurious one.
func (s *Server) boxPlayLocation() (location string, busy bool) {
	np, err := s.readBoxNowWithin(5 * time.Second)
	if err != nil {
		return "", false
	}
	return np.Location(), np.IsBusy()
}
