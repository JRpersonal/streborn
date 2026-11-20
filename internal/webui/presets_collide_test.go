package webui

import (
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

func TestPresetsCollide(t *testing.T) {
	const liked = "spotify:playlist:37i9dQZF1F5p3rmiWPIYgZ"
	sp := func(uri, acct string) presets.Preset {
		return presets.Preset{Type: "spotify", URI: uri, Account: acct}
	}
	radio := func(url string) presets.Preset {
		return presets.Preset{Type: "radio", StreamURL: url}
	}
	cases := []struct {
		name        string
		p, other    presets.Preset
		wantCollide bool
	}{
		// #1983: two accounts' Liked Songs share the alias URI but must not collide.
		{"liked different accounts", sp(liked, "alice"), sp(liked, "bob"), false},
		{"liked same account", sp(liked, "alice"), sp(liked, "alice"), true},
		// Native entry reports no account: fall back to URI so two unnamed Liked
		// Songs keys still cannot both exist.
		{"liked one account unknown", sp(liked, ""), sp(liked, "bob"), true},
		{"liked both unknown", sp(liked, ""), sp(liked, ""), true},
		// bare collection is account-bound too.
		{"collection different accounts", sp("spotify:collection", "alice"), sp("spotify:collection", "bob"), false},
		// A normal playlist is URI-only, account ignored (unchanged behaviour).
		{"normal playlist same uri different accounts", sp("spotify:playlist:abc", "alice"), sp("spotify:playlist:abc", "bob"), true},
		{"normal playlist different uri", sp("spotify:playlist:abc", "alice"), sp("spotify:playlist:xyz", "alice"), false},
		// Radio still collides by stream URL.
		{"radio same url", radio("http://host/stream"), radio("http://host/stream"), true},
		{"radio different url", radio("http://host/a"), radio("http://host/b"), false},
		// A Spotify preset never collides with a radio one.
		{"mixed types", sp(liked, "alice"), radio("http://host/stream"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := presetsCollide(tc.p, tc.other); got != tc.wantCollide {
				t.Errorf("presetsCollide = %v, want %v", got, tc.wantCollide)
			}
		})
	}
}
