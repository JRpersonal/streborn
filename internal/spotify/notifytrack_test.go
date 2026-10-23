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
