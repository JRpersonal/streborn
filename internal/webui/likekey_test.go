package webui

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/oled"
)

type likeFake struct {
	calls   int
	track   SpotifyLikedTrack
	err     error
	notices []oled.Notice
	log     bytes.Buffer
}

func newLikeServer(streaming bool, source, location string) (*Server, *likeFake) {
	f := &likeFake{track: SpotifyLikedTrack{URI: "spotify:track:4uLU6hMCjMI75M1A2tKUQC", Name: "A Song"}}
	s := &Server{
		logger:           slog.New(slog.NewTextHandler(&f.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
		spotifyStreaming: func() bool { return streaming },
		boxNowReadFn:     scriptNowPlaying(source, "PLAY_STATE", location),
		spotifyLikeCurrent: func(context.Context) (SpotifyLikedTrack, error) {
			f.calls++
			return f.track, f.err
		},
		playModeShowFn: func(n oled.Notice) { f.notices = append(f.notices, n) },
	}
	return s, f
}

func TestLikeKeySavesAndShows(t *testing.T) {
	s, f := newLikeServer(true, "UPNP", "")
	s.LikeKeyPress()
	if f.calls != 1 {
		t.Fatalf("engine asked %d times, want 1", f.calls)
	}
	if len(f.notices) != 1 || f.notices[0].Line1 != "LIKED" || f.notices[0].Line2 != "A Song" {
		t.Fatalf("notices = %+v", f.notices)
	}
	if !strings.Contains(f.log.String(), "saved the playing song to Liked Songs") {
		t.Fatalf("no success line in the log: %s", f.log.String())
	}
}

// A paused Spotify playlist is still STR's Spotify: the location says so.
func TestLikeKeyWorksOnAPausedPlaylist(t *testing.T) {
	s, f := newLikeServer(false, "UPNP", "http://127.0.0.1:8888/spotify/stream-4.ogg")
	s.LikeKeyPress()
	if f.calls != 1 {
		t.Fatal("a paused Spotify playlist did not take the thumbs up")
	}
}

// Radio, the box's own sources and an unreadable speaker never reach the
// engine: the key keeps whatever meaning it has there.
func TestLikeKeyDoesNothingOffSTRSpotify(t *testing.T) {
	for _, tc := range []struct{ name, source, location string }{
		{"radio on STR", "UPNP", "http://127.0.0.1:8888/stream/1"},
		{"the box's own Spotify", "SPOTIFY", ""},
		{"pandora", "PANDORA", ""},
		{"aux", "AUX", ""},
		{"standby", "STANDBY", ""},
		{"unreadable", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newLikeServer(false, tc.source, tc.location)
			s.LikeKeyPress()
			if f.calls != 0 || len(f.notices) != 0 {
				t.Fatalf("calls=%d notices=%d", f.calls, len(f.notices))
			}
		})
	}
}

func TestLikeKeyDebouncesARepeatedEvent(t *testing.T) {
	s, f := newLikeServer(true, "UPNP", "")
	s.LikeKeyPress()
	s.LikeKeyPress()
	if f.calls != 1 {
		t.Fatalf("one press reported twice asked the engine %d times", f.calls)
	}
}

// A second deliberate press on the same song still confirms on the panel.
func TestLikeKeyRepeatStillConfirms(t *testing.T) {
	s, f := newLikeServer(true, "UPNP", "")
	f.track.Repeat = true
	s.LikeKeyPress()
	if len(f.notices) != 1 {
		t.Fatalf("notices = %d, want 1", len(f.notices))
	}
	if !strings.Contains(f.log.String(), "already saved") {
		t.Fatalf("log: %s", f.log.String())
	}
}

// A failure warns with its reason, shows nothing, and never panics on a
// plain error.
func TestLikeKeyFailureWarnsWithReason(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{&SpotifyLikeError{Reason: "forbidden-403", Err: errors.New("refused")}, "reason=forbidden-403"},
		{&SpotifyLikeError{Reason: "rate-limited-429", Err: errors.New("slow down")}, "reason=rate-limited-429"},
		{&SpotifyLikeError{Reason: "engine-too-old", Err: errors.New("no route")}, "reason=engine-too-old"},
		{context.DeadlineExceeded, "reason=timeout"},
		{errors.New("boom"), "reason=failed"},
	} {
		s, f := newLikeServer(true, "UPNP", "")
		f.err = tc.err
		s.LikeKeyPress()
		out := f.log.String()
		if !strings.Contains(out, "level=WARN") || !strings.Contains(out, tc.reason) {
			t.Errorf("%v: log = %s", tc.err, out)
		}
		if len(f.notices) != 0 {
			t.Errorf("%v: a failed like showed a notice", tc.err)
		}
	}
}

func TestLikeKeyWithoutAnEngine(t *testing.T) {
	s, f := newLikeServer(true, "UPNP", "")
	s.spotifyLikeCurrent = nil
	s.LikeKeyPress()
	if f.calls != 0 || len(f.notices) != 0 {
		t.Fatal("a speaker without the engine did something")
	}
}
