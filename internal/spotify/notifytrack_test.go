package spotify

import (
	"io"
	"log/slog"
	"testing"
)

// TestNotifyTrackCarriesContext: the onTrack hook hands over the engine's
// context, cover and track URI with the song, so Recently played can open a
// card for a session started in the Spotify app (#135, maintainer's ST30
// 2026-10-24), and still fires once per distinct song.
func TestNotifyTrackCarriesContext(t *testing.T) {
	t.Parallel()
	m := &Manager{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var got []TrackChange
	m.SetOnTrack(func(tc TrackChange) { got = append(got, tc) })

	m.mu.Lock()
	m.curName, m.curArtist, m.curCover = "Weightless", "Marconi Union", "https://i.scdn.co/t1.jpg"
	m.curTrackURI, m.lastContext = "spotify:track:t1", "spotify:playlist:chill"
	m.mu.Unlock()
	m.notifyTrack()
	m.notifyTrack() // a repeated status poll of the same song: no second fire

	if len(got) != 1 {
		t.Fatalf("want one notification, got %d: %+v", len(got), got)
	}
	want := TrackChange{Track: "Weightless", Artist: "Marconi Union", Cover: "https://i.scdn.co/t1.jpg", TrackURI: "spotify:track:t1", ContextURI: "spotify:playlist:chill"}
	if got[0] != want {
		t.Fatalf("hook payload = %+v, want %+v", got[0], want)
	}

	// The next song carries the context it was announced under.
	m.mu.Lock()
	m.curName, m.lastContext = "Dreams", "spotify:album:rumours"
	m.mu.Unlock()
	m.notifyTrack()
	if len(got) != 2 || got[1].Track != "Dreams" || got[1].ContextURI != "spotify:album:rumours" {
		t.Fatalf("second song: %+v", got)
	}
}

// #1592: the same album recalled twice starts on the same song. The second
// recall opens a new Recently played card, and the name dedup used to swallow
// its first song, so the card showed no song and no time. The engine's
// metadata after a recall must fire once more; a /status poll must not, or the
// previous track still cached in curName would land under the new card.
func TestRecallFilesItsFirstSongEvenWhenTheNameRepeats(t *testing.T) {
	t.Parallel()
	m := &Manager{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var got []TrackChange
	m.SetOnTrack(func(tc TrackChange) { got = append(got, tc) })

	m.applyTrackMetadata("spotify:track:t1", "YOU MEAN MORE", []string{"Artist"}, "")
	if len(got) != 1 {
		t.Fatalf("first play: want one notification, got %d", len(got))
	}

	// A recall starts: a status poll of the cached song must not file it.
	m.mu.Lock()
	m.recallTrackArmed = true
	m.mu.Unlock()
	m.notifyTrack()
	if len(got) != 1 {
		t.Fatalf("a status poll after the recall re-filed the cached song: %+v", got)
	}

	// The engine names the recall's first song, the same one as before.
	m.applyTrackMetadata("spotify:track:t1", "YOU MEAN MORE", []string{"Artist"}, "")
	if len(got) != 2 || got[1].Track != "YOU MEAN MORE" {
		t.Fatalf("the recall's first song was not filed: %+v", got)
	}

	// Further metadata for the same song (pause, resume) still dedups.
	m.applyTrackMetadata("spotify:track:t1", "YOU MEAN MORE", []string{"Artist"}, "")
	if len(got) != 2 {
		t.Fatalf("the arm was not consumed: %d notifications", len(got))
	}
}
