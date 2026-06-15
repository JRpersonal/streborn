package webui

import "testing"

// A LAN library file (plain http://) is played by the box directly; radio and
// HTTPS sources keep going through the stream proxy (#139).
func TestIsPlainHTTPURL(t *testing.T) {
	cases := map[string]bool{
		"http://192.0.2.10:50002/track.flac":  true,
		"HTTP://192.0.2.10/track.flac":        true, // scheme is case-insensitive
		"https://192.0.2.10/track.flac":       false,
		"http://stream.example.com/radio":     true,
		"spotify:playlist:37i9dQZF1DWX7rdRjO": false,
		"":                                    false,
	}
	for in, want := range cases {
		if got := isPlainHTTPURL(in); got != want {
			t.Errorf("isPlainHTTPURL(%q) = %v, want %v", in, got, want)
		}
	}
}

// A saved library preset does not record its codec MIME, so recall re-derives it
// from the file extension to advertise the right protocolInfo to the box.
func TestMimeFromURL(t *testing.T) {
	cases := map[string]string{
		"http://nas/music/song.flac":          "audio/flac",
		"http://nas/music/song.FLAC":          "audio/flac", // case-insensitive
		"http://nas/music/song.wav":           "audio/wav",
		"http://nas/music/song.m4a":           "audio/mp4",
		"http://nas/music/song.aac":           "audio/mp4",
		"http://nas/music/song.ogg":           "audio/ogg",
		"http://nas/music/song.aiff":          "audio/aiff",
		"http://nas/music/song.mp3":           "audio/mpeg",
		"http://nas/music/song.flac?sid=abc":  "audio/flac", // query stripped
		"http://nas/music/song.flac#frag":     "audio/flac", // fragment stripped
		"http://nas/stream/raw":               "",           // no extension
		"http://nas/music/cover.png":          "",           // not audio
	}
	for in, want := range cases {
		if got := mimeFromURL(in); got != want {
			t.Errorf("mimeFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}
