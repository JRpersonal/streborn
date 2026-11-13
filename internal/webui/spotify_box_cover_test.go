package webui

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/presets"
)

// #1870, 2026-11-13: a private playlist saved to a key while the speaker's own
// Spotify played it was stored without a cover.

const boxCoverURI = "spotify:playlist:50u88Vj9v0gTgn38dd8YCK"

func boxCoverNowPlaying(uri, containerArt, art string) string {
	loc := "/playback/container/" + base64.RawStdEncoding.EncodeToString([]byte(uri))
	ca := ""
	if containerArt != "" {
		ca = "<containerArt>" + containerArt + "</containerArt>"
	}
	return `<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="DEV" source="SPOTIFY" sourceAccount="SpotifyConnectUserName">` +
		`<ContentItem source="SPOTIFY" type="DO_NOT_RESUME" location="` + loc + `" sourceAccount="SpotifyConnectUserName" isPresetable="false">` +
		`<itemName>Mj Lenderman universe</itemName>` + ca + `</ContentItem>` +
		`<track>Song</track><art artImageStatus="IMAGE_PRESENT">` + art + `</art><playStatus>PLAY_STATE</playStatus></nowPlaying>`
}

func newBoxCoverServer(t *testing.T, doc string) *Server {
	t.Helper()
	s := newHoldTestServer(t, "SPOTIFY", "")
	s.boxNowReadFn = func(context.Context) (*boxapi.NowPlaying, error) {
		return boxapi.ParseNowPlaying([]byte(doc)), nil
	}
	// the engine plays nothing, and the public lookup cannot see the playlist
	s.spotifyContext = func() string { return "" }
	s.spotifyMeta = func(context.Context, string) (string, string) { return "", "" }
	return s
}

func saveSpotifyKey(t *testing.T, s *Server, slot int, live bool, name, uri string) presets.Preset {
	t.Helper()
	saved, ref := s.savePresetSlot(context.Background(), presetSaveRequest{
		Slot: slot, Live: live, UserAgent: "test",
		Preset: presets.Preset{Name: name, Type: "spotify", URI: uri},
	})
	if ref != nil {
		t.Fatalf("save refused: %+v", ref)
	}
	return saved
}

func TestNativeSpotifySaveTakesTheCoverTheSpeakerShows(t *testing.T) {
	s := newBoxCoverServer(t, boxCoverNowPlaying(boxCoverURI, "", "https://i.scdn.co/image/song"))
	saved := saveSpotifyKey(t, s, 10, true, "Mj Lenderman universe  ", boxCoverURI)
	if saved.Art != "https://i.scdn.co/image/song" {
		t.Fatalf("art = %q, want the cover the speaker shows", saved.Art)
	}
	if saved.Name != "Mj Lenderman universe" {
		t.Fatalf("name = %q, want it without the trailing spaces", saved.Name)
	}
}

func TestNativeSpotifySavePrefersThePlaylistPicture(t *testing.T) {
	s := newBoxCoverServer(t, boxCoverNowPlaying(boxCoverURI, "https://i.scdn.co/image/playlist", "https://i.scdn.co/image/song"))
	if got := saveSpotifyKey(t, s, 10, true, "Mix", boxCoverURI).Art; got != "https://i.scdn.co/image/playlist" {
		t.Fatalf("art = %q, want the playlist's own picture", got)
	}
}

func TestNativeSpotifyCoverOnlyForWhatPlays(t *testing.T) {
	s := newBoxCoverServer(t, boxCoverNowPlaying(boxCoverURI, "", "https://i.scdn.co/image/song"))
	// another playlist than the one the speaker plays
	if got := saveSpotifyKey(t, s, 9, true, "Other", "spotify:playlist:other").Art; got != "" {
		t.Fatalf("a playlist that does not play got the speaker's cover: %q", got)
	}
	// a save that is not a "save what plays" gesture (rename, restore)
	if got := saveSpotifyKey(t, s, 8, false, "Mix", boxCoverURI).Art; got != "" {
		t.Fatalf("a non-live save took the speaker's cover: %q", got)
	}
}

func TestNativeSpotifyCoverNeedsAPicture(t *testing.T) {
	s := newBoxCoverServer(t, boxCoverNowPlaying(boxCoverURI, "", ""))
	if got := saveSpotifyKey(t, s, 10, true, "Mix", boxCoverURI).Art; got != "" {
		t.Fatalf("art = %q, want none when the speaker shows none", got)
	}
}

func TestBoxSpotifyContainerURI(t *testing.T) {
	// the firmware's standard alphabet can carry '/' and '+'
	uri := "spotify:playlist:??>>"
	std := base64.StdEncoding.EncodeToString([]byte(uri))
	cases := map[string]string{
		// live #950 location from the firmware
		"/playback/container/c3BvdGlmeTpwbGF5bGlzdDo1MHU4OFZqOXYwZ1RnbjM4ZGQ4WUNL": boxCoverURI,
		"/playback/container/" + std: uri,
		"/playback/container/" + base64.RawURLEncoding.EncodeToString([]byte(uri)): uri,
		"/playback/container/bm90LXNwb3RpZnk":                                      "",
		"http://example.com/stream":                                                "",
	}
	for loc, want := range cases {
		if got := boxSpotifyContainerURI(loc); got != want {
			t.Errorf("%s: %q, want %q", loc, got, want)
		}
	}
}
