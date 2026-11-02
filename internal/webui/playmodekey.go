package webui

// Shuffle and repeat on a remote key (#1679).
//
// A thumbs key bound to "shuffle" (internal/keyactions, saved from the
// desktop app at /api/keyactions) switches the Spotify playlist's shuffle on
// or off on every press; one bound to "repeat" steps its repeat the way the
// Spotify app does: off -> the whole playlist -> the one song -> off. Each
// press reads the state fresh from the engine (the Spotify app may have
// changed it since), so the press always moves on from what is really set.
//
// Outside a Spotify playlist (radio, the music library, AUX, a speaker that
// is off) the key does nothing and says so in the log. A speaker with a
// display shows the new state for a moment, like the sleep and bass steps.
//
// Presses run one at a time (playModeKeyState.mu), so two quick presses each
// read what the one before set. Nothing is persisted: the state lives in the
// engine's session, and a Spotify preset's own shuffle and repeat flags still
// apply at its next recall.

import (
	"context"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/keyactions"
	"github.com/JRpersonal/streborn/internal/oled"
)

// playModeKeyMinGap drops a second press event inside this window, as for the
// sleep and bass keys.
const playModeKeyMinGap = 250 * time.Millisecond

// playModeKeyTimeout bounds one press: the Spotify check, the engine read and
// its one or two writes.
const playModeKeyTimeout = 6 * time.Second

// playModeKeyState is what the shuffle and repeat keys keep between presses.
type playModeKeyState struct {
	mu   sync.Mutex
	last time.Time
}

// PlayModeKeyPress is the press handler of a key bound to shuffle or repeat
// (keyactions.ActionShuffle, ActionRepeat). Wired into the key handler in
// cmd/agent, which calls it off the key reader's goroutine.
func (s *Server) PlayModeKeyPress(action string) {
	if !keyactions.IsPlayMode(action) {
		return
	}
	k := &s.playModeKey
	k.mu.Lock()
	defer k.mu.Unlock()
	now := time.Now()
	if !k.last.IsZero() && now.Sub(k.last) < playModeKeyMinGap {
		s.logger.Debug("play mode key: press ignored, same press reported twice")
		return
	}
	k.last = now

	ctx, cancel := context.WithTimeout(context.Background(), playModeKeyTimeout)
	defer cancel()
	if s.spotifyToggleShuffle == nil || s.spotifyCycleRepeat == nil {
		s.logger.Info(action + " key pressed, but this speaker has no Spotify engine; the key does nothing here")
		return
	}
	if !s.spotifyIsStreaming() && !s.boxSourceIsSpotify(ctx) {
		s.logger.Info(action + " key pressed, but no Spotify playlist plays; the key only works on Spotify")
		return
	}
	if action == keyactions.ActionShuffle {
		on, err := s.spotifyToggleShuffle(ctx)
		if err != nil {
			s.logger.Warn("shuffle key pressed, but Spotify did not take the change", "err", err)
			return
		}
		s.logger.Info("shuffle key pressed", "shuffle", on)
		s.showPlayModeNotice(oled.Shuffle(on))
		return
	}
	mode, err := s.spotifyCycleRepeat(ctx)
	if err != nil {
		s.logger.Warn("repeat key pressed, but Spotify did not take the change", "err", err)
		return
	}
	s.logger.Info("repeat key pressed", "repeat", mode)
	s.showPlayModeNotice(oled.Repeat(mode))
}

// showPlayModeNotice puts the new state on this speaker's panel. Like the
// sleep and bass steps it shows even with the notices switched off: it is the
// only answer to a key the user just pressed. A seam (playModeShowFn).
func (s *Server) showPlayModeNotice(n oled.Notice) {
	if s.playModeShowFn != nil {
		s.playModeShowFn(n)
		return
	}
	if !oled.Supported() {
		return
	}
	n.Bypass = true
	oled.Show(n)
}
