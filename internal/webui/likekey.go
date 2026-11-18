package webui

// The remote's thumbs up key saves the playing Spotify song (#1165).
//
// While STR's own Spotify engine is what the speaker plays (also paused), a
// press of Thumbs up saves the song to the Liked Songs of the account the
// engine is logged in with. The key needs no setting: it applies to a thumbs
// up key that carries nothing else. A saved group, a key action or a webhook
// on the key keeps the key (cmd/agent keytrace.go decides that before this
// runs).
//
// On every other source this does nothing, and STR never consumes the key in
// the first place: it only reads the press from the speaker's key trace, so
// whatever the firmware does with thumbs up on a source of its own still
// happens. Thumbs down has no default: it neither skips nor removes.
//
// Only a press the key trace named counts. The gabbo bus reports both thumbs
// keys as the same bare frame, and a guess there could save a song on a
// thumbs DOWN, so that fallback path never reaches this.
//
// The write is the engine's (spotify.LikeCurrentTrack): no token leaves the
// engine and none is logged. A speaker with a display shows LIKED and the
// song for a moment; one without stays silent, the log line is the record.

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/oled"
)

// likeKeyMinGap drops a second press event inside this window: one press
// reported twice, or a key that bounced. A deliberate second press on the
// same song is answered from memory by the engine side (no second write).
const likeKeyMinGap = time.Second

// likeKeyTimeout bounds one press: the source check, the engine's status and
// its one write to Spotify.
const likeKeyTimeout = 10 * time.Second

// likeKeyState is what the thumbs up key keeps between presses.
type likeKeyState struct {
	mu   sync.Mutex
	last time.Time
}

// LikeKeyPress is the press handler of a thumbs up key that carries nothing
// else. Wired into the key handler in cmd/agent, which calls it off the key
// reader's goroutine.
func (s *Server) LikeKeyPress() {
	k := &s.likeKey
	k.mu.Lock()
	defer k.mu.Unlock()
	now := time.Now()
	if !k.last.IsZero() && now.Sub(k.last) < likeKeyMinGap {
		s.logger.Debug("thumbs up: press ignored, same press reported twice")
		return
	}
	k.last = now

	if s.spotifyLikeCurrent == nil {
		s.logger.Debug("thumbs up pressed, this speaker has no Spotify engine; the key does nothing here")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), likeKeyTimeout)
	defer cancel()
	if !s.spotifyIsStreaming() && !s.boxSourceIsSpotify(ctx) {
		s.logger.Info("thumbs up pressed, but Spotify on STR is not what plays; nothing saved")
		return
	}
	tr, err := s.spotifyLikeCurrent(ctx)
	if err != nil {
		s.logger.Warn("thumbs up pressed, but the song was not saved to Liked Songs",
			"reason", likeFailureReason(err), "err", err, "track", tr.URI)
		return
	}
	if tr.Repeat {
		s.logger.Info("thumbs up pressed again on the same song, already saved to Liked Songs a moment ago", "track", tr.URI)
	} else {
		s.logger.Info("thumbs up: saved the playing song to Liked Songs", "track", tr.URI, "name", tr.Name)
	}
	s.showPlayModeNotice(oled.Liked(tr.Name))
}

// likeFailureReason is the short, stable word for why a like failed, so a
// bundle can be searched for it. The engine side names it (SpotifyLikeError).
func likeFailureReason(err error) string {
	var le *SpotifyLikeError
	if errors.As(err, &le) && le.Reason != "" {
		return le.Reason
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "failed"
}
