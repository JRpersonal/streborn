package webui

// The remote's thumbs keys save and remove the playing Spotify song (#1165).
//
// While STR's own Spotify engine is what the speaker plays (also paused), a
// press of Thumbs up saves the song to the Liked Songs of the account the
// engine is logged in with, and a press of Thumbs down removes it from
// there. Thumbs down only removes: it does not skip, Forward does that. The
// keys need no setting: this applies to a thumbs key that carries nothing
// else. A saved group, a key action or a webhook on a key keeps that key
// (cmd/agent keytrace.go decides that before this runs), independently of
// the other one.
//
// On every other source this does nothing, and STR never consumes the keys
// in the first place: it only reads the press from the speaker's key trace,
// so whatever the firmware does with a thumbs key on a source of its own
// still happens.
//
// Only a press the key trace named counts. The gabbo bus reports both thumbs
// keys as the same bare frame, and a guess there could remove a song on a
// thumbs UP, so that fallback path never reaches this.
//
// The last press wins. Each key drops its own doubled event (likeKeyMinGap)
// and the engine side answers a repeat of the SAME action on the same song
// from memory, but neither window holds back the opposite key: up then down
// removes, down then up saves.
//
// The write is the engine's (spotify.LikeCurrentTrack, UnlikeCurrentTrack):
// no token leaves the engine and none is logged. A speaker with a display
// shows LIKED or REMOVED and the song for a moment; one without stays
// silent, the log line is the record.

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/oled"
)

// likeKeyMinGap drops a second press event of the same key inside this
// window: one press reported twice, or a key that bounced. A deliberate
// second press on the same song is answered from memory by the engine side
// (no second write).
const likeKeyMinGap = time.Second

// likeKeyTimeout bounds one press: the source check, the engine's status and
// its one write to Spotify.
const likeKeyTimeout = 10 * time.Second

// likeKeyState is what the thumbs keys keep between presses. The presses of
// both keys run one at a time (mu), in the order they arrived, so an up and
// a down in quick succession reach Spotify in that order.
type likeKeyState struct {
	mu       sync.Mutex
	lastUp   time.Time
	lastDown time.Time
}

// LikeKeyPress is the press handler of a thumbs up key that carries nothing
// else. Wired into the key handler in cmd/agent, which calls it off the key
// reader's goroutine.
func (s *Server) LikeKeyPress() { s.thumbsKeyPress(true) }

// UnlikeKeyPress is the press handler of a thumbs down key that carries
// nothing else: it removes the playing song from Liked Songs.
func (s *Server) UnlikeKeyPress() { s.thumbsKeyPress(false) }

func (s *Server) thumbsKeyPress(up bool) {
	key, write, last := "thumbs down", s.spotifyUnlikeCurrent, &s.likeKey.lastDown
	if up {
		key, write, last = "thumbs up", s.spotifyLikeCurrent, &s.likeKey.lastUp
	}
	k := &s.likeKey
	k.mu.Lock()
	defer k.mu.Unlock()
	now := time.Now()
	if !last.IsZero() && now.Sub(*last) < likeKeyMinGap {
		s.logger.Debug(key + ": press ignored, same press reported twice")
		return
	}
	*last = now

	if write == nil {
		s.logger.Debug(key + " pressed, this speaker has no Spotify engine; the key does nothing here")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), likeKeyTimeout)
	defer cancel()
	if !s.spotifyIsStreaming() && !s.boxSourceIsSpotify(ctx) {
		if up {
			s.logger.Info("thumbs up pressed, but Spotify on STR is not what plays; nothing saved")
		} else {
			s.logger.Info("thumbs down pressed, but Spotify on STR is not what plays; nothing removed")
		}
		return
	}
	tr, err := write(ctx)
	if err != nil {
		msg := "thumbs down pressed, but the song was not removed from Liked Songs"
		if up {
			msg = "thumbs up pressed, but the song was not saved to Liked Songs"
		}
		s.logger.Warn(msg, "reason", likeFailureReason(err), "err", err, "track", tr.URI)
		return
	}
	switch {
	case up && tr.Repeat:
		s.logger.Info("thumbs up pressed again on the same song, already saved to Liked Songs a moment ago", "track", tr.URI)
	case up:
		s.logger.Info("thumbs up: saved the playing song to Liked Songs", "track", tr.URI, "name", tr.Name)
	case tr.Repeat:
		s.logger.Info("thumbs down pressed again on the same song, already removed from Liked Songs a moment ago", "track", tr.URI)
	default:
		s.logger.Info("thumbs down: removed the playing song from Liked Songs", "track", tr.URI, "name", tr.Name)
	}
	if up {
		s.showPlayModeNotice(oled.Liked(tr.Name))
	} else {
		s.showPlayModeNotice(oled.Unliked(tr.Name))
	}
}

// likeFailureReason is the short, stable word for why a save or a removal
// failed, so a bundle can be searched for it. The engine side names it
// (SpotifyLikeError).
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
