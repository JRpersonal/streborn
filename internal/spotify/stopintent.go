package spotify

import (
	"context"
	"time"
)

// A Stop pressed shortly after a Spotify preset recall did not hold. The
// speaker stopped, and a few seconds later the music came back by itself
// (fleet roll 2026-10-04: three speakers, three times).
//
// Two things combined. STR's Stop and Pause only stop the speaker's UPnP
// transport and leave the engine alone, counting on the drain to pause it once
// the speaker stops pulling. Inside a recall window (SetRecalling,
// engineHotUntil) the drain deliberately keeps the engine running, so after the
// Stop it went on playing to nobody. Then, at the next track boundary, the
// engine reported "playing", maybeActivate read that as somebody starting
// Spotify from the app with the speaker on another source, and pointed the
// speaker back at the stream.
//
// UserStopped closes the first half: it ends the recall window and pauses the
// engine. The latch below closes the second, in case the pause does not land
// (engine slow, API hiccup): until a real new play intent arrives, an engine
// that simply kept playing through the stop is not allowed to bring the
// speaker back.
//
// What counts as a real new intent:
//   - an "active" event: the Spotify app selected this speaker;
//   - a "playing" event after the engine reported a pause or stop since the
//     user's stop, i.e. somebody pressed play again in the Spotify app;
//   - a "playing" event when the engine was not running at the time of the
//     stop at all, so it cannot be the old playback carrying on;
//   - any recall or play STR itself starts (SetRecalling clears the latch).

// UserStopped records that the user stopped or paused the speaker through STR
// (the app's Stop or Pause, standby). It ends any recall window so the drain
// pauses the engine as it would outside one, and pauses the engine directly
// when it was producing. Best effort and quiet: an engine that is not running
// needs no pause, and a failed pause is covered by the latch.
func (m *Manager) UserStopped(ctx context.Context, reason string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	now := time.Now()
	running := m.engineActive || m.sink != nil || now.Before(m.engineHotUntil)
	m.userStopAt = now
	m.engineRunningAtStop = running
	m.engineEndedSinceStop = false
	m.stopHoldLogged = false
	m.engineHotUntil = time.Time{}
	m.recallUntil = time.Time{}
	hasEngine := m.client != nil
	m.mu.Unlock()
	if !running || !hasEngine {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := m.Pause(pctx); err != nil {
		m.logger.Info("spotify: could not pause the engine after the user's stop, the stop latch still holds the speaker",
			"reason", reason, "err", err)
		return
	}
	// The engine is paused now, whether or not it sends a "paused" event (an
	// engine that was idle already does not), so the next "playing" can only
	// be somebody starting it again.
	m.mu.Lock()
	if m.userStopAt.Equal(now) {
		m.engineEndedSinceStop = true
	}
	m.mu.Unlock()
	m.logger.Info("spotify: paused the engine because the user stopped the speaker", "reason", reason)
}

// stopAttachGrace keeps a stream fetch that was already under way when the
// user pressed Stop from counting as a new play intent.
const stopAttachGrace = 2 * time.Second

// noteSinkAttachedLocked clears the latch when the speaker fetches the stream
// again after the stop: that only happens on a resume or a new play. mu held.
func (m *Manager) noteSinkAttachedLocked(now time.Time) {
	if !m.userStopAt.IsZero() && now.Sub(m.userStopAt) > stopAttachGrace {
		m.clearUserStopLocked()
	}
}

// clearUserStopLocked drops the latch (mu held): a real new play intent arrived.
func (m *Manager) clearUserStopLocked() {
	m.userStopAt = time.Time{}
	m.engineRunningAtStop = false
	m.engineEndedSinceStop = false
	m.stopHoldLogged = false
}

// noteEngineEnded records a pause, stop or inactive event from the engine.
func (m *Manager) noteEngineEnded() {
	m.mu.Lock()
	m.engineActive = false
	if !m.userStopAt.IsZero() {
		m.engineEndedSinceStop = true
	}
	m.mu.Unlock()
}

// engineStartAllowed is consulted on every "active" and "playing" event. It
// returns false when the event is the engine carrying on with the playback
// the user just stopped, in which case nothing may point the speaker back at
// the stream or lift STR's own stop latches. Otherwise it clears the latch
// and returns true.
func (m *Manager) engineStartAllowed(evType string) bool {
	m.mu.Lock()
	m.engineActive = true
	if m.userStopAt.IsZero() || evType == "active" || !m.engineRunningAtStop || m.engineEndedSinceStop {
		m.clearUserStopLocked()
		m.mu.Unlock()
		return true
	}
	logIt := !m.stopHoldLogged
	m.stopHoldLogged = true
	since := time.Since(m.userStopAt)
	m.mu.Unlock()
	if logIt {
		m.logger.Info("spotify: the engine kept playing through the user's stop, not bringing the speaker back",
			"event", evType, "sinceStopMs", since.Milliseconds())
		// The pause in UserStopped did not land, or the engine resumed on its
		// own. One more, so it stops decoding to nobody.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = m.Pause(ctx)
		}()
	}
	return false
}
