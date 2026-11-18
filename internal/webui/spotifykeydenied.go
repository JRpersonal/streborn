package webui

import "errors"

// isSpotifyKeyDenied reports whether a Spotify play failed because Spotify
// refused the audio key. webui does not import the spotify package, so it asks
// the error for the AudioKeyDenied method the spotify package's refusal error
// carries (spotify.IsAudioKeyDenied makes the identical test); no wording is
// compared. spotifykeydenied_test.go pins it against the real errors.
//
// It used to look for the text "audio key denied". The engine has two messages
// for a refused key, and the one it gives while it backs off after a run of
// refusals ("Spotify refused the audio key for several tracks in a row ...")
// does not contain that text, so the recall's last attempt fired another play
// into the back-off (discussion #1947).
func isSpotifyKeyDenied(err error) bool {
	var denied interface{ AudioKeyDenied() bool }
	return errors.As(err, &denied) && denied.AudioKeyDenied()
}
