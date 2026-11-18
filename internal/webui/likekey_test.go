package webui

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/oled"
)

type likeFake struct {
	calls   int
	unlikes int
	// order is every engine call in sequence: "like" or "unlike".
	order   []string
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
			f.order = append(f.order, "like")
			return f.track, f.err
		},
		spotifyUnlikeCurrent: func(context.Context) (SpotifyLikedTrack, error) {
			f.unlikes++
			f.order = append(f.order, "unlike")
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

func TestUnlikeKeyRemovesAndShows(t *testing.T) {
	s, f := newLikeServer(true, "UPNP", "")
	s.UnlikeKeyPress()
	if f.unlikes != 1 || f.calls != 0 {
		t.Fatalf("unlikes=%d likes=%d, want 1 and 0", f.unlikes, f.calls)
	}
	if len(f.notices) != 1 || f.notices[0].Line1 != "REMOVED" || f.notices[0].Line2 != "A Song" {
		t.Fatalf("notices = %+v", f.notices)
	}
	out := f.log.String()
	if !strings.Contains(out, "thumbs down: removed the playing song from Liked Songs") ||
		!strings.Contains(out, "track=spotify:track:4uLU6hMCjMI75M1A2tKUQC") || !strings.Contains(out, `name="A Song"`) {
		t.Fatalf("no removal line with track and name in the log: %s", out)
	}
}

func TestUnlikeKeyWorksOnAPausedPlaylist(t *testing.T) {
	s, f := newLikeServer(false, "UPNP", "http://127.0.0.1:8888/spotify/stream-4.ogg")
	s.UnlikeKeyPress()
	if f.unlikes != 1 {
		t.Fatal("a paused Spotify playlist did not take the thumbs down")
	}
}

func TestUnlikeKeyDoesNothingOffSTRSpotify(t *testing.T) {
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
			s.UnlikeKeyPress()
			if f.unlikes != 0 || f.calls != 0 || len(f.notices) != 0 {
				t.Fatalf("unlikes=%d likes=%d notices=%d", f.unlikes, f.calls, len(f.notices))
			}
			if !strings.Contains(f.log.String(), "nothing removed") {
				t.Fatalf("log: %s", f.log.String())
			}
		})
	}
}

// One key's doubled event is dropped, but the other key pressed right after
// is not: the last press wins, in the order pressed.
func TestThumbsKeysDebouncePerKey(t *testing.T) {
	s, f := newLikeServer(true, "UPNP", "")
	s.LikeKeyPress()
	s.UnlikeKeyPress()
	s.UnlikeKeyPress() // the same press reported twice
	s.LikeKeyPress()   // inside thumbs up's own gap: dropped
	if got := strings.Join(f.order, ","); got != "like,unlike" {
		t.Fatalf("engine calls = %q, want like,unlike", got)
	}
	s.likeKey.lastUp = time.Time{} // a second apart
	s.LikeKeyPress()
	if got := strings.Join(f.order, ","); got != "like,unlike,like" {
		t.Fatalf("engine calls = %q, want like,unlike,like", got)
	}
	if n := len(f.notices); n != 3 || f.notices[0].Line1 != "LIKED" || f.notices[1].Line1 != "REMOVED" || f.notices[2].Line1 != "LIKED" {
		t.Fatalf("notices = %+v", f.notices)
	}
}

func TestUnlikeKeyRepeatStillConfirms(t *testing.T) {
	s, f := newLikeServer(true, "UPNP", "")
	f.track.Repeat = true
	s.UnlikeKeyPress()
	if len(f.notices) != 1 || f.notices[0].Line1 != "REMOVED" {
		t.Fatalf("notices = %+v", f.notices)
	}
	if !strings.Contains(f.log.String(), "already removed") {
		t.Fatalf("log: %s", f.log.String())
	}
}

func TestUnlikeKeyFailureWarnsWithReason(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{&SpotifyLikeError{Reason: "forbidden-403", Err: errors.New("refused")}, "reason=forbidden-403"},
		{&SpotifyLikeError{Reason: "unauthorized-401", Err: errors.New("no")}, "reason=unauthorized-401"},
		{&SpotifyLikeError{Reason: "rate-limited-429", Err: errors.New("slow down")}, "reason=rate-limited-429"},
		{&SpotifyLikeError{Reason: "engine-too-old", Err: errors.New("no route")}, "reason=engine-too-old"},
		{errors.New("boom"), "reason=failed"},
	} {
		s, f := newLikeServer(true, "UPNP", "")
		f.err = tc.err
		s.UnlikeKeyPress()
		out := f.log.String()
		if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "was not removed from Liked Songs") || !strings.Contains(out, tc.reason) {
			t.Errorf("%v: log = %s", tc.err, out)
		}
		if len(f.notices) != 0 {
			t.Errorf("%v: a failed removal showed a notice", tc.err)
		}
	}
}

func TestUnlikeKeyWithoutAnEngine(t *testing.T) {
	s, f := newLikeServer(true, "UPNP", "")
	s.spotifyUnlikeCurrent = nil
	s.UnlikeKeyPress()
	if f.unlikes != 0 || f.calls != 0 || len(f.notices) != 0 {
		t.Fatal("a speaker without the engine did something")
	}
}
