package webui

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// Discussion #1983: key 10 held one household member's Liked Songs and showed
// as playing while another member played their own on the speaker's own
// Spotify entry. Both the stored URI and the reported location were this one
// playlist id, which Spotify uses for every account's Liked Songs.
const likedTestURI = "spotify:playlist:37i9dQZF1F5p3rmiWPIYgZ"

var likedKeyPresets = []presets.Preset{
	{Slot: 4, Type: "spotify", Name: "Mix", URI: "spotify:playlist:TestPlaylistMix0001", Account: "first-user"},
	{Slot: 10, Type: "spotify", Name: "Liked Songs", URI: likedTestURI, Account: "first-user"},
}

func account(name string) func() string { return func() string { return name } }

func TestAccountBoundSpotifyURI(t *testing.T) {
	for uri, want := range map[string]bool{
		likedTestURI:                           true,
		"spotify:collection":                   true,
		"spotify:collection:tracks":            true,
		"spotify:user:first-user:collection":   false, // names its owner
		"spotify:playlist:TestPlaylistMix0001": false,
		"":                                     false,
	} {
		if got := accountBoundSpotifyURI(uri); got != want {
			t.Errorf("accountBoundSpotifyURI(%q) = %v, want %v", uri, got, want)
		}
	}
}

func TestLikedSongsKeyLightsOnlyForItsAccount(t *testing.T) {
	const generic = "http://192.0.2.1:8888/spotify/stream.ogg"
	for _, tc := range []struct {
		name, loc, engine string
		acct              func() string
		want              int
	}{
		// The reported case: the speaker's own client names no account, and
		// the engine's login says nothing about who plays there.
		{"speaker's own Spotify plays Liked Songs", container(likedTestURI), "", account("first-user"), 0},
		{"engine plays Liked Songs for the saving account", generic, likedTestURI, account("first-user"), 10},
		{"engine plays Liked Songs for another account", generic, likedTestURI, account("second-user"), 0},
		{"engine plays Liked Songs, account not readable", generic, likedTestURI, account(""), 0},
		{"engine plays Liked Songs, no account source", generic, likedTestURI, nil, 0},
		// A recall of the key itself plays its per-slot stream under the
		// key's own account.
		{"the key's own recall", "http://192.0.2.1:8888/spotify/stream-10.ogg", "", nil, 10},
		// Every other playlist still matches by URI, whoever plays it.
		{"another account plays a shared playlist on the engine", generic, "spotify:playlist:TestPlaylistMix0001", account("second-user"), 4},
		{"speaker's own Spotify plays a shared playlist", container("spotify:playlist:TestPlaylistMix0001"), "", nil, 4},
	} {
		if got := playingSpotifySlot(tc.loc, tc.engine, tc.acct, likedKeyPresets); got != tc.want {
			t.Errorf("%s: got key %d, want %d", tc.name, got, tc.want)
		}
	}
}

// A Liked Songs key saved without an account cannot be told to be anyone's.
func TestLikedSongsKeyWithoutAccountStaysDark(t *testing.T) {
	list := []presets.Preset{{Slot: 10, Type: "spotify", URI: likedTestURI}}
	if got := playingSpotifySlot("http://192.0.2.1:8888/spotify/stream.ogg", likedTestURI, account("first-user"), list); got != 0 {
		t.Errorf("got key %d, want none", got)
	}
}

// The engine is only asked who is logged in when a Liked Songs key is in
// question, and then once.
func TestPlayingKeyAsksForTheAccountOnlyForLikedSongs(t *testing.T) {
	asked := 0
	acct := func() string { asked++; return "first-user" }
	list := append([]presets.Preset{{Slot: 11, Type: "spotify", URI: likedTestURI, Account: "first-user"}}, likedKeyPresets...)
	const generic = "http://192.0.2.1:8888/spotify/stream.ogg"
	if got := playingSpotifySlot(generic, "spotify:playlist:TestPlaylistMix0001", acct, list); got != 4 || asked != 0 {
		t.Errorf("shared playlist: key %d after %d account reads, want key 4 and none", got, asked)
	}
	if got := playingSpotifySlot(generic, likedTestURI, acct, list); got != 10 || asked != 1 {
		t.Errorf("Liked Songs: key %d after %d account reads, want key 10 and one", got, asked)
	}
}

// /api/status, as the phone page reads it: no key for the speaker's own
// Spotify playing Liked Songs, the key for the engine under the saving account.
func TestStatusLikedSongsKeyHeader(t *testing.T) {
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range likedKeyPresets {
		if err := store.SetSlot(p); err != nil {
			t.Fatal(err)
		}
	}
	body := func(loc string) []byte {
		return []byte(`<nowPlaying source="SPOTIFY" sourceAccount="SpotifyConnectUserName"><ContentItem source="SPOTIFY" location="` +
			loc + `" sourceAccount="SpotifyConnectUserName"><itemName>Liked Songs</itemName></ContentItem><playStatus>PLAY_STATE</playStatus></nowPlaying>`)
	}
	user := "first-user"
	s := &Server{
		boxHost:        "127.0.0.1:1",
		presets:        store,
		spotifyContext: func() string { return likedTestURI },
		spotifyUser:    func(context.Context) string { return user },
		statusBody:     body(container(likedTestURI)),
		statusCode:     http.StatusOK,
		statusAt:       time.Now(),
	}
	if got := pollStatus(s).Header().Get(playingKeyHeader); got != "" {
		t.Errorf("speaker's own Spotify: %s = %q, want none", playingKeyHeader, got)
	}
	s.statusBody = body("http://192.0.2.1:8888/spotify/stream.ogg")
	if got := pollStatus(s).Header().Get(playingKeyHeader); got != "10" {
		t.Errorf("engine, saving account: %s = %q, want 10", playingKeyHeader, got)
	}
	// Another household member took the engine over; the cached login has run out.
	user = "second-user"
	s.playingKeyAcctAt = time.Now().Add(-2 * playingKeyAccountTTL)
	if got := pollStatus(s).Header().Get(playingKeyHeader); got != "" {
		t.Errorf("engine, another account: %s = %q, want none", playingKeyHeader, got)
	}
}
