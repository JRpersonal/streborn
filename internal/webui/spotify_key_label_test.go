package webui

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
)

// A Spotify key stored as "Spotify" with no cover stayed that way for good
// (discussion #1870). These cover what the song hook and the save now do for
// it, and above all how rarely they write.

const (
	keyLabelURI   = "spotify:playlist:private"
	keyLabelCover = "https://i.scdn.co/image/song"
)

// newKeyLabelServer is a server with a preset store on disk and the lookup
// seam; it returns the store's path so a test can watch the file.
func newKeyLabelServer(t *testing.T, meta *fakeSpotifyMeta) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "presets.json")
	store, err := presets.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s := newConnectRecentServer(meta)
	s.presets = store
	s.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return s, path
}

func mustSetSlot(t *testing.T, s *Server, p presets.Preset) {
	t.Helper()
	if err := s.presets.SetSlot(p); err != nil {
		t.Fatal(err)
	}
}

func TestSpotifyKeyWithoutCoverTakesThePlayingSongsCover(t *testing.T) {
	t.Parallel()
	// The lookup knows nothing about this playlist, as for a private one.
	meta := &fakeSpotifyMeta{}
	s, _ := newKeyLabelServer(t, meta)
	mustSetSlot(t, s, presets.Preset{Slot: 4, Type: "spotify", URI: keyLabelURI, Name: "Spotify", Account: "jens-main"})
	rev := s.presets.Revision()

	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Song One", Artist: "A", Cover: keyLabelCover, ContextURI: keyLabelURI})

	p, _ := s.presets.Get(4)
	if p.Art != keyLabelCover {
		t.Fatalf("art = %q, want the playing song's cover", p.Art)
	}
	if p.Name != "Spotify" || p.URI != keyLabelURI || p.Account != "jens-main" {
		t.Fatalf("the heal changed more than the cover: %+v", p)
	}
	if got := s.presets.Revision(); got != rev+1 {
		t.Fatalf("store written %d times for one cover, want 1", got-rev)
	}
	// The card opened for this song carries the cover too.
	if got := entriesFor(s, keyLabelURI); len(got) != 1 || got[0].CardArt != keyLabelCover {
		t.Fatalf("recently played card: %+v", s.recent.All())
	}

	// Every later song of the playlist: no write, and no second lookup, though
	// the name is still the placeholder.
	rev = s.presets.Revision()
	for _, song := range []string{"Song Two", "Song Three", "Song Four"} {
		s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: song, Cover: "https://i.scdn.co/image/other-" + song, ContextURI: keyLabelURI})
	}
	if got := s.presets.Revision(); got != rev {
		t.Fatalf("later songs wrote the store %d times, want 0", got-rev)
	}
	if p, _ := s.presets.Get(4); p.Art != keyLabelCover {
		t.Fatalf("the stored cover followed the playing song: %q", p.Art)
	}
	if n := meta.count(keyLabelURI); n != 1 {
		t.Fatalf("lookup asked %d times for one playlist, want 1", n)
	}
}

func TestSpotifyKeyGetsPlaylistNameAndCoverWhenTheLookupAnswers(t *testing.T) {
	t.Parallel()
	meta := &fakeSpotifyMeta{table: map[string][2]string{
		keyLabelURI: {"https://mosaic.scdn.co/300/list", "Worship Hits"},
	}}
	s, _ := newKeyLabelServer(t, meta)
	mustSetSlot(t, s, presets.Preset{Slot: 4, Type: "spotify", URI: keyLabelURI, Name: "Spotify"})
	// The recall opened the card under the old label.
	s.recentNoteEntry(recent.Entry{Source: "spotify", CardKey: keyLabelURI, CardName: "Spotify", CardURL: keyLabelURI})
	rev := s.presets.Revision()

	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Song One", Cover: keyLabelCover, ContextURI: keyLabelURI})

	p, _ := s.presets.Get(4)
	if p.Name != "Worship Hits" || p.Art != "https://mosaic.scdn.co/300/list" {
		t.Fatalf("key = %q / %q, want the playlist's own name and cover", p.Name, p.Art)
	}
	if got := s.presets.Revision(); got != rev+1 {
		t.Fatalf("name and cover took %d writes, want 1", got-rev)
	}
	// The first song is merged into the row the recall started (the ring
	// keeps that row's name); from the next song on the rows carry the label.
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Song Two", Cover: keyLabelCover, ContextURI: keyLabelURI})
	got := entriesFor(s, keyLabelURI)
	if last := got[len(got)-1]; last.CardName != "Worship Hits" || last.CardArt != "https://mosaic.scdn.co/300/list" {
		t.Fatalf("the open card kept the old label: %+v", last)
	}
	if got := s.presets.Revision(); got != rev+1 {
		t.Fatalf("the second song wrote again: %d writes", got-rev)
	}
}

func TestSpotifyKeyCompleteOrRenamedIsLeftAlone(t *testing.T) {
	t.Parallel()
	meta := &fakeSpotifyMeta{table: map[string][2]string{
		keyLabelURI:              {"https://mosaic.scdn.co/300/list", "Worship Hits"},
		"spotify:playlist:other": {"https://mosaic.scdn.co/300/other", "Other"},
	}}
	s, path := newKeyLabelServer(t, meta)
	// Key 1: complete. Key 2: the user's own name, no cover.
	mustSetSlot(t, s, presets.Preset{Slot: 1, Type: "spotify", URI: keyLabelURI, Name: "Sunday", Art: "https://i.scdn.co/image/kept"})
	mustSetSlot(t, s, presets.Preset{Slot: 2, Type: "spotify", URI: "spotify:playlist:other", Name: "Mum's list"})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rev := s.presets.Revision()

	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Song One", Cover: keyLabelCover, ContextURI: keyLabelURI})
	if got := s.presets.Revision(); got != rev {
		t.Fatalf("a complete key was rewritten %d times", got-rev)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("presets.json changed for a key that had its name and cover")
	}
	if n := meta.count(keyLabelURI); n != 0 {
		t.Fatalf("a complete key asked the lookup %d times", n)
	}

	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Song Two", Cover: keyLabelCover, ContextURI: "spotify:playlist:other"})
	p, _ := s.presets.Get(2)
	if p.Name != "Mum's list" {
		t.Fatalf("a name the user typed was replaced by %q", p.Name)
	}
	if p.Art != "https://mosaic.scdn.co/300/other" {
		t.Fatalf("art = %q, want the playlist cover", p.Art)
	}
}

func TestSpotifyKeyHealNeedsAMatchingContext(t *testing.T) {
	t.Parallel()
	meta := &fakeSpotifyMeta{}
	s, _ := newKeyLabelServer(t, meta)
	mustSetSlot(t, s, presets.Preset{Slot: 4, Type: "spotify", URI: keyLabelURI, Name: "Spotify"})
	// A radio key must never be touched, whatever plays.
	mustSetSlot(t, s, presets.Preset{Slot: 5, Type: "radio", Name: "Spotify", StreamURL: "http://radio.example/stream"})
	rev := s.presets.Revision()

	// No context: the song cannot be tied to a key.
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Loose Song", Cover: keyLabelCover})
	// Another playlist.
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Elsewhere", Cover: keyLabelCover, ContextURI: "spotify:album:elsewhere"})
	// The right playlist, but a cover the apps could not draw.
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Odd Cover", Cover: "spotify:image:abc", ContextURI: keyLabelURI})

	if got := s.presets.Revision(); got != rev {
		t.Fatalf("store written %d times, want 0", got-rev)
	}
	if p, _ := s.presets.Get(4); p.Art != "" || p.Name != "Spotify" {
		t.Fatalf("key changed: %+v", p)
	}
}

// The station wrapper an old save left on the key is the same playlist.
func TestSpotifyKeyHealMatchesTheStationWrapper(t *testing.T) {
	t.Parallel()
	s, _ := newKeyLabelServer(t, &fakeSpotifyMeta{})
	mustSetSlot(t, s, presets.Preset{Slot: 3, Type: "spotify", URI: "spotify:station:playlist:private", Name: "Spotify"})
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Song One", Cover: keyLabelCover, ContextURI: keyLabelURI})
	if p, _ := s.presets.Get(3); p.Art != keyLabelCover || p.URI != "spotify:station:playlist:private" {
		t.Fatalf("key = %+v", p)
	}
}

// Saving what plays: a playlist the lookup has no cover for is stored with the
// playing song's cover right away, the reporter's delete-and-save-again case.
func TestSpotifyLiveSaveFallsBackToThePlayingSongsCover(t *testing.T) {
	s := newHoldTestServer(t, "UPNP", holdTestSpotifyLoc)
	s.spotifyContext = func() string { return holdTestSpotifyURI }
	s.spotifyUser = func(context.Context) string { return "second-user" }
	s.spotifyMeta = func(context.Context, string) (string, string) { return "", "" }
	// The engine announced the playing song before the save.
	s.NoteRecentSpotifyTrack(SpotifyNowPlaying{Track: "Song One", Cover: keyLabelCover, ContextURI: holdTestSpotifyURI})

	saved, ref := s.savePresetSlot(context.Background(), presetSaveRequest{
		Slot: 4, Live: true, UserAgent: "test",
		Preset: presets.Preset{Name: "Spotify", Type: "spotify", URI: holdTestSpotifyURI},
	})
	if ref != nil {
		t.Fatalf("save refused: %+v", ref)
	}
	if saved.Art != keyLabelCover {
		t.Fatalf("art = %q, want the playing song's cover", saved.Art)
	}

	// A save of something that is NOT what plays gets no song cover.
	saved, ref = s.savePresetSlot(context.Background(), presetSaveRequest{
		Slot: 5, Live: true, UserAgent: "test",
		Preset: presets.Preset{Name: "Spotify", Type: "spotify", URI: "spotify:playlist:notplaying"},
	})
	if ref != nil {
		t.Fatalf("save refused: %+v", ref)
	}
	if saved.Art != "" {
		t.Fatalf("a playlist that does not play got the playing song's cover: %q", saved.Art)
	}
}
