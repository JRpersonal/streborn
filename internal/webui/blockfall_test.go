package webui

import "testing"

func TestBlockfallTakenOver(t *testing.T) {
	cases := []struct {
		name   string
		np     nowPlayingSnapshot
		inGame bool
		want   bool
	}{
		{"start not taken yet", nowPlayingSnapshot{Source: "INVALID_SOURCE"}, false, false},
		{"intro music", nowPlayingSnapshot{Source: "UPNP", Location: blockfallIntroURL}, false, false},
		{"game-over jingle", nowPlayingSnapshot{Source: "UPNP", Location: blockfallOverURL}, false, false},
		{"silent game, skip key on the stopped source", nowPlayingSnapshot{Source: "UPNP"}, true, false},
		{"preset pressed during the game", nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", Location: "http://radio.example/stream"}, true, true},
		{"app plays a DLNA track in the intro", nowPlayingSnapshot{Source: "UPNP", Location: "http://192.0.2.1/track.mp3"}, false, true},
		{"standby from the app", nowPlayingSnapshot{Source: "STANDBY"}, true, true},
	}
	for _, c := range cases {
		if got := blockfallTakenOver(c.np, c.inGame); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
