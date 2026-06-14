package webui

import (
	"encoding/base64"
	"testing"
)

func TestPlayableSpotifyURI(t *testing.T) {
	cases := map[string]bool{
		// Replayable contexts go-librespot accepts.
		"spotify:playlist:37i9dQZF1DWX7rdRjOECPW": true,
		"spotify:album:1DFixLWuPkv3KT3TnV35m3":    true,
		"spotify:track:4uLU6hMCjMI75M1A2tKUQC":    true,
		"spotify:artist:0OdUWJ0sBjDrqHygGUXeCF":   true,
		"spotify:show:4rOoJ6Egrf8K2IrywzwOMk":     true, // podcast
		"spotify:episode:512ojhOuo1ktJprKbVcKyQ":  true,
		"spotify:collection":                      true, // Liked Songs
		"spotify:user:abc:playlist:37i9dQZF1DX":   true,
		"spotify:user:abc:collection":             true,
		// Not replayable -> reject (the dead-preset cause).
		"":                        false,
		"spotify:":                false,
		"spotify:playlist:":       false,
		"http://host/stream.mp3":  false,
		"/spotify/stream.ogg":     false,
		"/playback/container/abc": false,
	}
	for uri, want := range cases {
		if got := playableSpotifyURI(uri); got != want {
			t.Errorf("playableSpotifyURI(%q) = %v, want %v", uri, got, want)
		}
	}
}

func TestLegacySpotifyURI(t *testing.T) {
	uri := "spotify:playlist:37i9dQZF1DWX7rdRjOECPW"
	enc := base64.RawURLEncoding.EncodeToString([]byte(uri))
	cases := map[string]string{
		"/playback/container/" + enc:           uri,
		"/playback/container/" + enc + "/more": uri, // trailing path segment ignored
		"http://host/stream.mp3":               "",
		"/spotify/stream.ogg":                  "",
		"/playback/container/not base64!!":     "",
		"":                                     "",
	}
	for in, want := range cases {
		if got := legacySpotifyURI(in); got != want {
			t.Errorf("legacySpotifyURI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestURLClassifiers(t *testing.T) {
	if !isHTTPURL("http://x") || !isHTTPURL("https://x") || isHTTPURL("/spotify/stream.ogg") {
		t.Error("isHTTPURL classification wrong")
	}
	if !looksLikeSpotifyStreamURL("http://127.0.0.1:8888/spotify/stream.ogg") ||
		!looksLikeSpotifyStreamURL("/playback/container/abc") ||
		looksLikeSpotifyStreamURL("http://host/news.mp3") {
		t.Error("looksLikeSpotifyStreamURL classification wrong")
	}
}
