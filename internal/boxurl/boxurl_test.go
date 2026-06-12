package boxurl

import "testing"

func TestURLs(t *testing.T) {
	cases := []struct{ got, want string }{
		{StreamSlot(3), "http://127.0.0.1:8888/stream/3"},
		{SpotifySlot(6), "http://127.0.0.1:8888/spotify/stream-6.ogg"},
		{SpotifyDefault(), "http://127.0.0.1:8888/spotify/stream.ogg"},
		{Preset(2, false), "http://127.0.0.1:8888/stream/2"},
		{Preset(2, true), "http://127.0.0.1:8888/spotify/stream-2.ogg"},
		// base64url of "http://x" with no padding.
		{RawStream("http://x"), "http://127.0.0.1:8888/stream/raw?u=aHR0cDovL3g"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
