package webui

import (
	"encoding/base64"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// container builds the location the speaker's own Spotify client reports.
func container(uri string) string {
	return "/playback/container/" + base64.StdEncoding.EncodeToString([]byte(uri))
}

var playingKeyPresets = []presets.Preset{
	{Slot: 1, Type: "spotify", Name: "Spotify", URI: ""}, // a key literally named "Spotify"
	{Slot: 2, Type: "radio", Name: "Some station", StreamURL: "http://radio.example/stream"},
	{Slot: 4, Type: "spotify", Name: "Nocturnes", URI: "spotify:album:TestAlbumNocturnes01"},
	{Slot: 5, Type: "spotify", Name: "Mix", URI: "spotify:playlist:TestPlaylistMix0001"},
	{Slot: 6, Type: "spotify", Name: "Sonata", URI: "spotify:track:TestTrackSonata00001"},
}

// The same three cases the desktop app lights a Spotify key for (#1077 issues
// 19 and 20), and nothing else.
func TestPlayingSpotifySlot(t *testing.T) {
	for _, tc := range []struct {
		name, loc, engine string
		want              int
	}{
		{"engine per-slot stream", "http://192.0.2.1:8888/spotify/stream-5.ogg", "", 5},
		{"engine generic stream, context on a key", "http://192.0.2.1:8888/spotify/stream.ogg", "spotify:track:TestTrackSonata00001", 6},
		{"engine generic stream, station wrapper", "http://192.0.2.1:8888/spotify/stream.ogg", "spotify:station:playlist:TestPlaylistMix0001", 5},
		{"engine generic stream, empty context", "http://192.0.2.1:8888/spotify/stream.ogg", "", 0},
		{"engine generic stream, context on no key", "http://192.0.2.1:8888/spotify/stream.ogg", "spotify:playlist:SomethingElse", 0},
		{"speaker's own Spotify plays a key's album", container("spotify:album:TestAlbumNocturnes01"), "", 4},
		// The engine still holds an older, paused context; the speaker's own
		// client is what plays, so only its location counts (#1077 issue 15).
		{"speaker's own Spotify, stale engine context ignored", container("spotify:album:OtherAlbum"), "spotify:playlist:TestPlaylistMix0001", 0},
		{"speaker's own Spotify, URL-safe unpadded form", "/playback/container/" + base64.RawURLEncoding.EncodeToString([]byte("spotify:track:TestTrackSonata00001")), "", 6},
		{"radio plays", "http://radio.example/stream", "spotify:playlist:TestPlaylistMix0001", 0},
		{"nothing plays", "", "spotify:playlist:TestPlaylistMix0001", 0},
	} {
		if got := playingSpotifySlot(tc.loc, tc.engine, playingKeyPresets); got != tc.want {
			t.Errorf("%s: got key %d, want %d", tc.name, got, tc.want)
		}
	}
}

// Two keys holding one playlist (an older store): the lowest wins, so the
// highlight does not hop between polls.
func TestPlayingSpotifySlotLowestKeyWins(t *testing.T) {
	list := []presets.Preset{
		{Slot: 5, Type: "spotify", URI: "spotify:playlist:Dup"},
		{Slot: 3, Type: "spotify", URI: "spotify:playlist:Dup"},
	}
	if got := playingSpotifySlot(container("spotify:playlist:Dup"), "", list); got != 3 {
		t.Errorf("got key %d, want 3", got)
	}
}

// /api/status carries the key in a header and leaves the body alone: every
// client regex-parses the speaker's XML.
func TestStatusNamesThePlayingSpotifyKey(t *testing.T) {
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range playingKeyPresets {
		if err := store.SetSlot(p); err != nil {
			t.Fatal(err)
		}
	}
	body := `<nowPlaying source="SPOTIFY"><ContentItem source="SPOTIFY" location="` +
		container("spotify:album:TestAlbumNocturnes01") + `"><itemName>Nocturnes</itemName></ContentItem><playStatus>PLAY_STATE</playStatus></nowPlaying>`
	s := &Server{
		boxHost:        "127.0.0.1:1",
		presets:        store,
		spotifyContext: func() string { return "spotify:playlist:TestPlaylistMix0001" },
		statusBody:     []byte(body),
		statusCode:     http.StatusOK,
		statusAt:       time.Now(),
	}
	w := pollStatus(s)
	if got := w.Header().Get(playingKeyHeader); got != "4" {
		t.Errorf("%s = %q, want 4", playingKeyHeader, got)
	}
	if w.Body.String() != body {
		t.Errorf("body changed: %q", w.Body.String())
	}

	s.statusBody = []byte(`<nowPlaying source="LOCAL_INTERNET_RADIO"><ContentItem location="http://radio.example/stream"></ContentItem></nowPlaying>`)
	s.statusAt = time.Now()
	if got := pollStatus(s).Header().Get(playingKeyHeader); got != "" {
		t.Errorf("radio playing: %s = %q, want none", playingKeyHeader, got)
	}
}

// The phone page lights the key the agent names, and a hold saves the URI the
// desktop app would: the speaker's own location while its own Spotify plays,
// the engine's context only for the engine's stream (#1077 issues 19 to 21).
func TestPhoneRemoteFollowsTheSpotifyKeyRules(t *testing.T) {
	for _, want := range []string{
		"r.headers.get('X-STR-Playing-Key')",
		"markPlaying(nowLoc, queue, spKey, state);",
		"(spKey >= 1 && spKey <= 12 ? spKey : 0)",
		"var ctxUri = spotifySaveContextURI(src, loc, (info && info.context) || '');",
		"function spotifyURIFromContainer(loc)",
		"if (src === 'SPOTIFY' || boxURI) return boxURI;",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("phone page is missing %q", want)
		}
	}
	if strings.Contains(indexHTML, "var ctxUri = (info && info.context) || '';") {
		t.Error("the phone hold still saves the engine's context whatever plays")
	}
}
