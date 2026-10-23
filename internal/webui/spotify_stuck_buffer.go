package webui

// The stuck-buffering rescue for a Spotify key (#1077).
//
// Field trace (ST10 stereo pair, v1.0.12, Spotify key 4 pressed in the app
// while the pair was in standby): the master's recall worked and filled its
// buffer, but the RIGHT follower logged "Could not obtain first-frame" and
// ERROR_NO_DECODED_DATA. About a minute after the press the master let go of
// the Ogg stream with ERROR_SLAVE_PLAYBACK_FAILURE and "m_nMaxRetryAttempt =0",
// and then sat in BUFFERING_STATE (white blinking "Loading, please wait") for
// good. The detach reached maybeRePush, which read BUFFERING as busy and
// returned without a word, so nothing ever moved the box out of that state.
//
// The rescue is deliberately small and bounded:
//   - one re-check after stuckBufRecheck, because a box can buffer a few
//     seconds and then play on its own;
//   - still buffering: ONE clean recall of the same slot, the path every other
//     Spotify recovery takes;
//   - still buffering after that: stop the transport so the speaker leaves the
//     stuck state, mark the stream given up, and put the "no internet" message
//     on a display that has one (only when that is the reason; the station-down
//     text names a radio station, which a Spotify key is not);
//   - one rescue per episode: only a newer user play opens the next one;
//   - every step stands down on standby, a deliberate stop, a newer user play,
//     or a source the user picked. It never wakes a speaker, and it runs only
//     inside the detach that already happened (no timer of its own).

import (
	"context"
	"strings"
	"time"
)

const (
	// stuckBufRecheck is the pause before the first re-check.
	stuckBufRecheck = 12 * time.Second
	// stuckBufAfterRecall is how long the recall gets before the final check.
	// The recall's own verify runs three 5 s ticks; this outlasts it.
	stuckBufAfterRecall = 25 * time.Second
)

// stuckBufStep is what the rescue does next.
type stuckBufStep int

const (
	stuckBufLeave     stuckBufStep = iota // recovered or idle: nothing to rescue
	stuckBufStandDown                     // standby or the user acted: hands off
	stuckBufRecall                        // still stuck: one clean recall
	stuckBufStop                          // the recall did not help: stop the transport
)

func (st stuckBufStep) String() string {
	switch st {
	case stuckBufStandDown:
		return "stand-down"
	case stuckBufRecall:
		return "recall"
	case stuckBufStop:
		return "stop"
	}
	return "leave"
}

// stuckBufNext is the rescue's decision, pure so it can be tested on its own.
// afterRecall says whether the one recall has already been spent.
func stuckBufNext(afterRecall, standby, busy, buffering, userActed bool) stuckBufStep {
	switch {
	case standby || userActed:
		return stuckBufStandDown
	case !buffering:
		// Playing, paused, or idle. Playing and paused are a recovery; an idle
		// box is not stuck, and the regular watchdog owns an idle drop.
		return stuckBufLeave
	case afterRecall:
		return stuckBufStop
	}
	return stuckBufRecall
}

// stuckBufEpisodeOpen reports whether a rescue may run: none has run yet, or
// the user started a play after the last one did.
func stuckBufEpisodeOpen(lastRescue, lastUserPlay time.Time) bool {
	return lastRescue.IsZero() || lastUserPlay.After(lastRescue)
}

func (s *Server) stuckBufDur(def time.Duration) time.Duration {
	if s.stuckBufWait > 0 {
		return s.stuckBufWait
	}
	return def
}

// stuckBufUserActed reports whether anything since t says the user (or the
// speaker's own power button) took over: a newer play, a stop, a power-off, a
// source of the user's own, or a different stream on record.
func (s *Server) stuckBufUserActed(since time.Time, boxURL string) (bool, string) {
	switch {
	case s.standbyStop.userPlayedAfter(since):
		return true, "a newer play started"
	case s.userStoppedAfter(since):
		return true, "the user stopped"
	case s.StandbyStoppedAfter(since):
		return true, "the speaker was switched off"
	}
	if src := s.boxSourceNow(); !resumeSafeSource(src) {
		return true, "the speaker is on a source the user chose: " + src
	}
	if s.play.currentURL() != boxURL {
		return true, "a different stream is on record"
	}
	return false, ""
}

// rescueStuckSpotifyBuffering runs on maybeRePush's goroutine (the rePushInFlight
// latch is held for its whole length, so a second detach cannot stack a second
// rescue) when a detach found the box still BUFFERING. Non-Spotify streams and
// app-driven Spotify sessions are left as before.
func (s *Server) rescueStuckSpotifyBuffering() {
	boxURL, lastRescue := s.play.stuckBufEpisode()
	slot := slotFromSpotifyStreamURL(boxURL)
	if slot <= 0 || !strings.Contains(boxURL, "spotify/stream") {
		return
	}
	if !stuckBufEpisodeOpen(lastRescue, s.standbyStop.userPlayStart()) {
		s.logger.Warn("spotify stuck buffering: already rescued once since the last key press, leaving it", "slot", slot)
		return
	}

	start := time.Now()
	time.Sleep(s.stuckBufDur(stuckBufRecheck))
	standby, busy, buffering := s.boxPlayStateDetail()
	acted, why := s.stuckBufUserActed(start, boxURL)
	step := stuckBufNext(false, standby, busy, buffering, acted)
	s.logger.Warn("spotify stuck buffering: re-checked",
		"slot", slot, "standby", standby, "busy", busy, "buffering", buffering, "userActed", why, "next", step.String())
	if step != stuckBufRecall {
		return
	}

	// A follower's audio comes from its master; recalling a preset on it
	// fights the group. Same stand-down as the regular watchdog.
	if standDown, why := s.zonePushWouldFightGroup(); standDown {
		s.logger.Info("spotify stuck buffering: not recalling, " + why)
		return
	}
	s.play.noteStuckBufRescue(time.Now())
	s.logger.Warn("spotify stuck buffering: recalling the preset once, cleanly", "slot", slot)
	rctx, rcancel := context.WithTimeout(context.Background(), 30*time.Second)
	status := s.recallSlotClean(rctx, slot)
	rcancel()
	// recallSlotClean stamps a user play of its own (handlePlaySlot ->
	// NoteUserPlay). Re-stamp after it so that stamp does not reopen the
	// episode, and anchor the "did the user act" check here.
	recalled := time.Now()
	s.play.noteStuckBufRescue(recalled)
	if status >= 400 {
		s.logger.Warn("spotify stuck buffering: the recall was refused", "slot", slot, "status", status)
	}

	time.Sleep(s.stuckBufDur(stuckBufAfterRecall))
	standby, busy, buffering = s.boxPlayStateDetail()
	acted, why = s.stuckBufUserActed(recalled, s.currentLastPlayURL())
	step = stuckBufNext(true, standby, busy, buffering, acted)
	s.logger.Warn("spotify stuck buffering: checked after the recall",
		"slot", slot, "standby", standby, "busy", busy, "buffering", buffering, "userActed", why, "next", step.String())
	if step != stuckBufStop {
		return
	}
	s.stopStuckBuffering(slot)
}

// currentLastPlayURL returns the stream on record ("" when none). The recall
// records its own stream, which may carry a different URL form than the one
// the detach saw; comparing against itself keeps that from reading as "the
// user switched".
func (s *Server) currentLastPlayURL() string {
	return s.play.currentURL()
}

// stopStuckBuffering ends the stuck state: a plain transport stop (no power
// change, the speaker stays exactly as awake as it is), the stream marked
// given up so no later detach re-arms it, and the "no internet" message when
// that is the reason and the speaker has a display.
func (s *Server) stopStuckBuffering(slot int) {
	if s.renderer == nil {
		return
	}
	s.play.markFailed()
	s.boxCmdMu.Lock()
	// Re-check under the command mutex: a key press that held it meanwhile
	// may have started the box.
	standby, busy, buffering := s.boxPlayStateDetail()
	if standby || (busy && !buffering) {
		s.boxCmdMu.Unlock()
		s.logger.Info("spotify stuck buffering: the speaker moved on by itself, not stopping", "slot", slot)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	err := s.renderer.Stop(ctx)
	cancel()
	s.boxCmdMu.Unlock()
	if err != nil {
		s.logger.Warn("spotify stuck buffering: stopping the transport failed", "slot", slot, "err", err)
		return
	}
	s.logger.Warn("spotify stuck buffering: the recall did not start either, stopped the transport so the speaker leaves the loading state", "slot", slot)
	s.ShowKeyMessage(DisplayMsgIfOffline)
}
