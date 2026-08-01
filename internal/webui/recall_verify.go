// Post-recall verification: confirming the box really switched streams.

package webui

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
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
	s.logger.Warn("recall still not playing after retries")
	// Wedge detection (#power-cycle hint): decide whether this exhaustion
	// looks like the box rather than the station, and count it.
	s.NoteRecallExhausted()
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
// now_playing attribute values (location="...&amp;..."), so the recall verify
// compares real URLs, not encodings.
func xmlAttrUnescape(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'")
	return r.Replace(s)
}

// boxPlayLocation reads now_playing once and reports the ContentItem location
// the box is tuned to plus whether it is busy (playing, buffering or paused).
// Best-effort: ("", false) when the box cannot be read; verifyRecall treats an
// unreadable location as a match, so this can only ever ADD a justified retry,
// never a spurious one.
func (s *Server) boxPlayLocation() (location string, busy bool) {
	if s.boxHost == "" {
		return "", false
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get("http://" + s.boxHost + ":8090/now_playing")
	if err != nil {
		return "", false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	resp.Body.Close()
	body := string(b)
	if m := reNowPlayLocation.FindStringSubmatch(body); m != nil {
		location = xmlAttrUnescape(m[1])
	}
	busy = strings.Contains(body, "PLAY_STATE") || strings.Contains(body, "BUFFERING_STATE") || strings.Contains(body, "PAUSE_STATE")
	return location, busy
}
