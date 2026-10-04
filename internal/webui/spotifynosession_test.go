package webui

import (
	"errors"
	"fmt"
	"testing"

	"github.com/JRpersonal/streborn/internal/spotify"
)

// webui recognises the spotify package's no-session error by its text; this
// pins the two together so a rewording on one side fails here.
func TestNoSpotifySessionIsRecognised(t *testing.T) {
	if !isNoSpotifySession(spotify.ErrNoSpotifySession) {
		t.Fatal("spotify.ErrNoSpotifySession is not recognised")
	}
	if !isNoSpotifySession(fmt.Errorf("recall: %w", spotify.ErrNoSpotifySession)) {
		t.Fatal("a wrapped ErrNoSpotifySession is not recognised")
	}
	if isNoSpotifySession(errors.New("spotify: audio key denied")) || isNoSpotifySession(nil) {
		t.Fatal("an unrelated error was taken for a missing login")
	}
}
