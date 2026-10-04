package webui

import (
	"context"
	"strings"
	"time"
)

// A Spotify key pressed while the speaker holds no Spotify login it can use.
//
// The gate before the wake (recallgate.go) refuses this case with the "pick
// this speaker in Spotify once" answer whenever the agent already knows it. It
// cannot know it on the first press after Spotify stopped accepting the saved
// login: the credential is still on disk and untested, the gate lets the recall
// through, and only the engine's re-login in the background finds out. Until
// now the recall then carried on as if the play had merely been slow. The
// verify re-pointed the speaker at a stream that would never carry audio three
// times, the speaker buffered into nothing and settled on "no source" with its
// amber light, and the app showed "stream starting" and then "ready" with no
// word of why (field, ST10 stereo pair, 2026-10-04: several presses in a row).
//
// The background play now ends the recall instead. The speaker is stopped so it
// does not sit buffering, and no retry is fired at an engine that has nothing to
// log in with. The next press meets the gate, which by then knows the login was
// refused (credreject.go sets it aside) and answers with the instruction.

// errNoSpotifySessionText is the message of spotify.ErrNoSpotifySession. webui
// does not import the spotify package, so the error is recognised by its text,
// the same way "audio key denied" is; spotifynosession_test.go pins it.
const errNoSpotifySessionText = "no live device session"

// isNoSpotifySession reports whether a Spotify play failed because the speaker
// has no usable Spotify login.
func isNoSpotifySession(err error) bool {
	return err != nil && strings.Contains(err.Error(), errNoSpotifySessionText)
}

// abandonSessionlessRecall ends a Spotify recall whose play found no usable
// login: it stops the speaker unless something newer has taken over since the
// press, and logs why so a bundle names the cause.
func (s *Server) abandonSessionlessRecall(gen uint64, started time.Time, slot int) {
	if reason := s.recallStandDownReason(gen, started); reason != "" {
		s.logger.Info("spotify recall: no usable Spotify login, and a newer action already took over", "slot", slot, "reason", reason)
		return
	}
	s.logger.Warn("spotify recall: the speaker has no Spotify login it can use, stopping instead of retrying; pick this speaker in the Spotify app once", "slot", slot)
	if s.renderer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.renderer.Stop(ctx); err != nil {
		s.logger.Warn("spotify recall: stopping the speaker after the failed recall did not work", "slot", slot, "err", err)
	}
}
