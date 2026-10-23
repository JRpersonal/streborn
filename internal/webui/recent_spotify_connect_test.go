package webui

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
)

// A session started in the Spotify app (Connect) never opens a Recently played
// card through a recall, and the hook used to drop every song for want of one
// (#135; the maintainer's ST30, 2026-10-24). These tests cover the card the
// hook now opens from the engine's context.

// fakeSpotifyMeta is the oEmbed seam: counts lookups per URI and answers from
// a table, no network.
type fakeSpotifyMeta struct {
	mu    sync.Mutex
	calls map[string]int
	table map[string][2]string // uri -> {cover, title}
}

func (f *fakeSpotifyMeta) meta(_ context.Context, uri string) (cover, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[uri]++
	m := f.table[uri]
	return m[0], m[1]
}

func (f *fakeSpotifyMeta) count(uri string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[uri]
}

func newConnectRecentServer(meta *fakeSpotifyMeta) *Server {
	s := &Server{recent: recent.New(), recentSpotifyMetaTimeout: time.Second}
	if meta != nil {
		s.spotifyMeta = meta.meta
	}
	s.spotifyUser = func(context.Context) string { return "jens-main" }
	return s
}

// entriesFor returns the ring's entries filed under key, oldest first.
func entriesFor(s *Server, key string) []recent.Entry {
	var out []recent.Entry
	for _, e := range s.recent.All() {
		if e.CardKey == key {
			out = append(out, e)
		}
	}
	return out
}

func TestRecentSpotifyConnectOpensPlaylistCard(t *testing.T) {
	t.Parallel()
	meta := &fakeSpotifyMeta{table: map[string][2]string{
		"spotify:playlist:chill": {"https://i.scdn.co/chill.jpg", "Jens Chill"},
	}}
	s := newConnectRecentServer(meta)

	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Weightless", Artist: "Marconi Union", Cover: "https://i.scdn.co/t1.jpg",
		TrackURI: "spotify:track:t1", ContextURI: "spotify:playlist:chill"})
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Intro", Artist: "The xx", Cover: "https://i.scdn.co/t2.jpg",
		TrackURI: "spotify:track:t2", ContextURI: "spotify:playlist:chill"})

	got := entriesFor(s, "spotify:playlist:chill")
	if len(got) != 2 {
		t.Fatalf("want the two songs under the playlist card, got %+v", s.recent.All())
	}
	first := got[0]
	if first.Source != "spotify" || first.CardName != "Jens Chill" || first.CardArt != "https://i.scdn.co/chill.jpg" ||
		first.CardURL != "spotify:playlist:chill" || first.Account != "jens-main" || first.Track != "Marconi Union - Weightless" {
		t.Fatalf("card from the engine's context is wrong: %+v", first)
	}
	if got[1].Track != "The xx - Intro" || got[1].CardName != "Jens Chill" {
		t.Fatalf("second song not filed under the same card: %+v", got[1])
	}
	// One oEmbed lookup per playlist, not per song.
	if n := meta.count("spotify:playlist:chill"); n != 1 {
		t.Fatalf("oEmbed asked %d times for one playlist, want 1", n)
	}
}

func TestRecentSpotifyConnectContextChangeStartsNewCard(t *testing.T) {
	t.Parallel()
	meta := &fakeSpotifyMeta{table: map[string][2]string{
		"spotify:playlist:chill": {"https://i.scdn.co/chill.jpg", "Jens Chill"},
		"spotify:album:rumours":  {"https://i.scdn.co/rumours.jpg", "Rumours"},
	}}
	s := newConnectRecentServer(meta)

	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Weightless", ContextURI: "spotify:playlist:chill"})
	// The station wrapper the engine sometimes announces is the same playlist.
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Intro", ContextURI: "spotify:station:playlist:chill"})
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Dreams", Artist: "Fleetwood Mac", ContextURI: "spotify:album:rumours"})

	if got := entriesFor(s, "spotify:playlist:chill"); len(got) != 2 {
		t.Fatalf("want two songs under the playlist, got %+v", s.recent.All())
	}
	got := entriesFor(s, "spotify:album:rumours")
	if len(got) != 1 || got[0].CardName != "Rumours" || got[0].Track != "Fleetwood Mac - Dreams" {
		t.Fatalf("the switched-to album did not get its own card: %+v", s.recent.All())
	}
	if meta.count("spotify:playlist:chill") != 1 || meta.count("spotify:album:rumours") != 1 {
		t.Fatalf("oEmbed calls: chill=%d rumours=%d, want 1 each", meta.count("spotify:playlist:chill"), meta.count("spotify:album:rumours"))
	}
}

func TestRecentSpotifyConnectNoContextFallsBackToSessionCard(t *testing.T) {
	t.Parallel()
	meta := &fakeSpotifyMeta{}
	s := newConnectRecentServer(meta)

	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Weightless", Artist: "Marconi Union", Cover: "https://i.scdn.co/t1.jpg", TrackURI: "spotify:track:t1"})
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Intro", Artist: "The xx", Cover: "https://i.scdn.co/t2.jpg", TrackURI: "spotify:track:t2"})

	got := entriesFor(s, recentSpotifySessionKey)
	if len(got) != 2 {
		t.Fatalf("want both songs under the session card, got %+v", s.recent.All())
	}
	if got[0].CardName != "Spotify" || got[0].CardArt != "https://i.scdn.co/t1.jpg" || got[0].CardURL != "spotify:track:t1" || got[0].Account != "jens-main" {
		t.Fatalf("session card is wrong: %+v", got[0])
	}
	if got[1].CardURL != "spotify:track:t2" {
		t.Fatalf("a later song's row must point at that song: %+v", got[1])
	}
	if len(meta.calls) != 0 {
		t.Fatalf("oEmbed must not be asked without a context: %v", meta.calls)
	}

	// A playlist appearing afterwards leaves the session card for its own.
	meta.table = map[string][2]string{"spotify:playlist:chill": {"", "Jens Chill"}}
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Breathe", Cover: "https://i.scdn.co/t3.jpg", ContextURI: "spotify:playlist:chill"})
	pl := entriesFor(s, "spotify:playlist:chill")
	if len(pl) != 1 || pl[0].CardName != "Jens Chill" || pl[0].CardArt != "https://i.scdn.co/t3.jpg" {
		t.Fatalf("playlist after the session card: %+v", s.recent.All())
	}
}

func TestRecentSpotifyConnectMetaFailureNamesCardSpotify(t *testing.T) {
	t.Parallel()
	meta := &fakeSpotifyMeta{} // answers "", "" for everything
	s := newConnectRecentServer(meta)

	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Weightless", Cover: "https://i.scdn.co/t1.jpg", ContextURI: "spotify:playlist:unknown"})
	got := entriesFor(s, "spotify:playlist:unknown")
	if len(got) != 1 || got[0].CardName != "Spotify" || got[0].CardArt != "https://i.scdn.co/t1.jpg" || got[0].CardURL != "spotify:playlist:unknown" {
		t.Fatalf("card without a resolved title: %+v", s.recent.All())
	}
	// A failed lookup is not cached: the next song asks again.
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Intro", ContextURI: "spotify:playlist:unknown"})
	if n := meta.count("spotify:playlist:unknown"); n != 1 {
		// The card stays open for the second song, so no second lookup happens
		// here: the retry is per card opening, not per song.
		t.Fatalf("lookups for an open card: %d, want 1", n)
	}

	// No engine meta hook at all (a reduced engine) still files the song.
	s2 := &Server{recent: recent.New()}
	s2.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Weightless", ContextURI: "spotify:playlist:chill"})
	if got := entriesFor(s2, "spotify:playlist:chill"); len(got) != 1 || got[0].CardName != "Spotify" || got[0].Account != "" {
		t.Fatalf("without meta/user hooks: %+v", s2.recent.All())
	}
}

func TestRecentSpotifyConnectRecallCardUntouched(t *testing.T) {
	t.Parallel()
	meta := &fakeSpotifyMeta{table: map[string][2]string{
		"spotify:playlist:chill": {"https://i.scdn.co/other.jpg", "Other Title"},
	}}
	s := newConnectRecentServer(meta)

	// STR's own recall opened the card (playback.go), with the preset's name,
	// art and account, under the preset's URI spelling.
	s.NoteRecentPreset(presets.Preset{Slot: 2, Type: "spotify", URI: "spotify:playlist:chill", Name: "Jens Chill", Art: "https://i.scdn.co/chill.jpg", Account: "jens-main"})
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Weightless", Artist: "Marconi Union", ContextURI: "spotify:station:playlist:chill"})
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Intro", Artist: "The xx", ContextURI: "spotify:playlist:chill"})

	got := entriesFor(s, "spotify:playlist:chill")
	if len(got) != 2 || got[0].CardName != "Jens Chill" || got[0].CardArt != "https://i.scdn.co/chill.jpg" ||
		got[0].Track != "Marconi Union - Weightless" || got[1].Track != "The xx - Intro" {
		t.Fatalf("songs after a recall must stay under the recall card unchanged: %+v", s.recent.All())
	}
	if n := meta.count("spotify:playlist:chill"); n != 0 {
		t.Fatalf("a recall card never asks oEmbed, got %d lookups", n)
	}
	// Switching playlists in the app after a recall opens a new card; the
	// recall card keeps what it had.
	meta.table["spotify:album:rumours"] = [2]string{"", "Rumours"}
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Dreams", ContextURI: "spotify:album:rumours"})
	if got := entriesFor(s, "spotify:album:rumours"); len(got) != 1 || got[0].CardName != "Rumours" {
		t.Fatalf("playlist switch after a recall: %+v", s.recent.All())
	}
	if got := entriesFor(s, "spotify:playlist:chill"); len(got) != 2 {
		t.Fatalf("recall card changed by the switch: %+v", got)
	}
}

func TestRecentSpotifyConnectReusesPresetForKnownPlaylist(t *testing.T) {
	t.Parallel()
	meta := &fakeSpotifyMeta{table: map[string][2]string{
		"spotify:playlist:chill": {"https://i.scdn.co/other.jpg", "Other Title"},
	}}
	s := newConnectRecentServer(meta)
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.presets = store
	// The preset still carries the station wrapper an old save wrote; the
	// engine announces the bare playlist. The card must take the preset's
	// spelling as its key so the apps find the slot and show Play.
	if err := s.presets.SetSlot(presets.Preset{Slot: 3, Type: "spotify", URI: "spotify:station:playlist:chill", Name: "Jens Chill", Art: "https://i.scdn.co/chill.jpg", Account: "jens-main"}); err != nil {
		t.Fatal(err)
	}

	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Weightless", ContextURI: "spotify:playlist:chill"})

	got := entriesFor(s, "spotify:station:playlist:chill")
	if len(got) != 1 || got[0].CardName != "Jens Chill" || got[0].CardArt != "https://i.scdn.co/chill.jpg" || got[0].CardURL != "spotify:station:playlist:chill" {
		t.Fatalf("card for a playlist on a key must mirror the preset: %+v", s.recent.All())
	}
	if n := meta.count("spotify:playlist:chill"); n != 0 {
		t.Fatalf("a playlist on a key never asks oEmbed, got %d lookups", n)
	}
}

func TestRecentSpotifyConnectNoStoreOrEmptyTrackIsNoop(t *testing.T) {
	t.Parallel()
	meta := &fakeSpotifyMeta{}
	s := &Server{spotifyMeta: meta.meta}
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Weightless", ContextURI: "spotify:playlist:chill"})
	s2 := newConnectRecentServer(meta)
	s2.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "", ContextURI: "spotify:playlist:chill"})
	if len(meta.calls) != 0 || len(s2.recent.All()) != 0 {
		t.Fatalf("no store / empty track must do nothing: calls=%v ring=%+v", meta.calls, s2.recent.All())
	}
}
