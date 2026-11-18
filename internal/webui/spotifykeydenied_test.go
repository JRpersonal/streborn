package webui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/spotify"
)

// The engine's messages for a refused audio key, as its two hints word them.
// Only the second contains the text the recall used to look for.
const (
	testBackoffHint = "Spotify refused the audio key for several tracks in a row, so the speaker's Spotify engine stopped"
	testItemHint    = "Spotify refused to deliver the audio for this item (audio key denied)."
)

// webui recognises the spotify package's refused-key error by its method, not
// by its text, and must agree with spotify.IsAudioKeyDenied on every error.
// Discussion #1947: the back-off refusal went unrecognised (keyDenied=false in
// the log) and the recall's last attempt played into it again.
func TestSpotifyKeyDeniedIsRecognised(t *testing.T) {
	cause := errors.New("spotify api /player/play: 500")
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"refused during the engine's back-off", spotify.NewAudioKeyDeniedError(cause, testBackoffHint), true},
		{"back-off after an accepted play", spotify.NewAudioKeyDeniedError(nil, testBackoffHint), true},
		{"a single refused key", spotify.NewAudioKeyDeniedError(cause, testItemHint), true},
		{"wrapped on the way up", fmt.Errorf("recall: %w", spotify.NewAudioKeyDeniedError(cause, testBackoffHint)), true},
		{"no usable login", spotify.ErrNoSpotifySession, false},
		{"an unrelated failure", cause, false},
		{"no error", nil, false},
	}
	for _, c := range cases {
		if got := isSpotifyKeyDenied(c.err); got != c.want {
			t.Errorf("%s: isSpotifyKeyDenied = %v, want %v", c.name, got, c.want)
		}
		if got := spotify.IsAudioKeyDenied(c.err); got != c.want {
			t.Errorf("%s: spotify.IsAudioKeyDenied = %v, want %v", c.name, got, c.want)
		}
	}
	// The case from the field: the old text match misses the back-off message.
	if strings.Contains(cases[0].err.Error(), "audio key denied") {
		t.Fatal("setup: the back-off message contains the old match text")
	}
}
