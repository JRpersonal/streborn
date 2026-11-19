package webui

import "testing"

// CurrentSpotifyTitle is the fallback the stream proxy's /api/stream/title
// consults during a Spotify preset-key play. It serves the live song only
// while Spotify is the speaker's current playback, and never a stale one.
func TestCurrentSpotifyTitle(t *testing.T) {
	s, _, _ := newTrackServer(t, true)

	// Nothing has played: no title.
	if got, ok := s.CurrentSpotifyTitle(); ok {
		t.Fatalf("idle: got %q, want none", got)
	}

	// Spotify is the active source and a song is playing: "Artist - Title".
	s.PanelSourceChanged(panelUPnPSource)
	s.PanelSpotifyActive()
	s.PanelSpotifyTrack("Flowers", "Miley Cyrus")
	if got, ok := s.CurrentSpotifyTitle(); !ok || got != "Miley Cyrus - Flowers" {
		t.Fatalf("playing: got %q ok=%v, want %q", got, ok, "Miley Cyrus - Flowers")
	}

	// The engine named no artist: just the track.
	s.PanelSpotifyTrack("Weightless", "")
	if got, ok := s.CurrentSpotifyTitle(); !ok || got != "Weightless" {
		t.Fatalf("no artist: got %q ok=%v, want %q", got, ok, "Weightless")
	}

	// The speaker goes to standby: the song is stale now, so none is served.
	s.PanelSourceChanged("STANDBY")
	if got, ok := s.CurrentSpotifyTitle(); ok {
		t.Fatalf("standby: got %q, want none", got)
	}

	// Back on the Spotify stream: the song returns.
	s.PanelSourceChanged(panelUPnPSource)
	if got, ok := s.CurrentSpotifyTitle(); !ok || got != "Weightless" {
		t.Fatalf("resumed: got %q ok=%v, want %q", got, ok, "Weightless")
	}

	// The user starts a radio station (still on UPnP): it is not Spotify's
	// song any more, so the fallback stands down.
	s.setLastPlay("http://192.0.2.1:8888/stream/9", "Radio X", "", "audio/mpeg")
	if got, ok := s.CurrentSpotifyTitle(); ok {
		t.Fatalf("radio: got %q, want none", got)
	}
}
