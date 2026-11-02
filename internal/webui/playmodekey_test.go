package webui

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/keyactions"
	"github.com/JRpersonal/streborn/internal/oled"
)

type playModeFake struct {
	shuffle  bool
	repeat   string
	notices  []oled.Notice
	shuffled int
}

func newPlayModeServer(streaming bool, source, location string) (*Server, *playModeFake) {
	f := &playModeFake{repeat: "off"}
	s := &Server{
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		spotifyStreaming: func() bool { return streaming },
		boxNowReadFn:     scriptNowPlaying(source, "PAUSE_STATE", location),
		spotifyToggleShuffle: func(context.Context) (bool, error) {
			f.shuffled++
			f.shuffle = !f.shuffle
			return f.shuffle, nil
		},
		spotifyCycleRepeat: func(context.Context) (string, error) {
			f.repeat = map[string]string{"off": "context", "context": "track", "track": "off"}[f.repeat]
			return f.repeat, nil
		},
		playModeShowFn: func(n oled.Notice) { f.notices = append(f.notices, n) },
	}
	return s, f
}

func TestShuffleKeyFlipsAndShows(t *testing.T) {
	s, f := newPlayModeServer(true, "UPNP", "")
	s.PlayModeKeyPress(keyactions.ActionShuffle)
	if !f.shuffle || len(f.notices) != 1 || f.notices[0].Line1 != "SHUFFLE ON" {
		t.Fatalf("shuffle=%v notices=%v", f.shuffle, f.notices)
	}
}

func TestRepeatKeyStepsAndShows(t *testing.T) {
	s, f := newPlayModeServer(true, "UPNP", "")
	for _, want := range []string{"REPEAT ALL", "REPEAT ONE", "REPEAT OFF"} {
		s.playModeKey.last = time.Time{} // presses a second apart
		s.PlayModeKeyPress(keyactions.ActionRepeat)
		if got := f.notices[len(f.notices)-1].Line1; got != want {
			t.Fatalf("notice %q, want %q", got, want)
		}
	}
}

// A paused playlist is still a Spotify playlist: the speaker's location says so.
func TestPlayModeKeyWorksOnAPausedPlaylist(t *testing.T) {
	s, f := newPlayModeServer(false, "UPNP", "http://127.0.0.1:8888/spotify/stream-4.ogg")
	s.PlayModeKeyPress(keyactions.ActionShuffle)
	if f.shuffled != 1 {
		t.Fatal("a paused Spotify playlist did not take the shuffle key")
	}
}

func TestPlayModeKeyDoesNothingOffSpotify(t *testing.T) {
	s, f := newPlayModeServer(false, "UPNP", "http://127.0.0.1:8888/stream/1")
	s.PlayModeKeyPress(keyactions.ActionShuffle)
	s.playModeKey.last = time.Time{}
	s.PlayModeKeyPress(keyactions.ActionRepeat)
	if f.shuffled != 0 || f.repeat != "off" || len(f.notices) != 0 {
		t.Fatalf("radio took the play mode keys: shuffled=%d repeat=%q notices=%d", f.shuffled, f.repeat, len(f.notices))
	}
}

func TestPlayModeKeyDebouncesARepeatedEvent(t *testing.T) {
	s, f := newPlayModeServer(true, "UPNP", "")
	s.PlayModeKeyPress(keyactions.ActionShuffle)
	s.PlayModeKeyPress(keyactions.ActionShuffle)
	if f.shuffled != 1 {
		t.Fatalf("one press reported twice flipped shuffle %d times", f.shuffled)
	}
}
