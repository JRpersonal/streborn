package webui

import "testing"

// CurrentSpotifyMeta is the fallback the stream proxy's /api/stream/title
// consults during a Spotify preset-key play. It serves the live song and its
// cover only while Spotify is the speaker's current playback, and never a stale
// one.
func TestCurrentSpotifyMeta(t *testing.T) {
	s, _, _ := newTrackServer(t, true)

	// Nothing has played: no title.
	if got, ok := s.CurrentSpotifyMeta(); ok {
		t.Fatalf("idle: got %+v, want none", got)
	}

	// Spotify is the active source and a song is playing: title plus the
	// components, cover included.
	s.PanelSourceChanged(panelUPnPSource)
	s.PanelSpotifyActive()
	s.PanelSpotifyTrack("Flowers", "Miley Cyrus", "https://i.scdn.co/image/flowers.jpg")
	got, ok := s.CurrentSpotifyMeta()
	if !ok || got.Title != "Miley Cyrus - Flowers" || got.Track != "Flowers" || got.Artist != "Miley Cyrus" || got.Art != "https://i.scdn.co/image/flowers.jpg" {
		t.Fatalf("playing: got %+v ok=%v", got, ok)
	}

	// The engine named no artist: just the track, and Artist empty while Track
	// and the cover still ride along.
	s.PanelSpotifyTrack("Weightless", "", "https://i.scdn.co/image/weightless.jpg")
	got, ok = s.CurrentSpotifyMeta()
	if !ok || got.Title != "Weightless" || got.Track != "Weightless" || got.Artist != "" || got.Art != "https://i.scdn.co/image/weightless.jpg" {
		t.Fatalf("no artist: got %+v ok=%v", got, ok)
	}

	// The speaker goes to standby: the song is stale now, so none is served and
	// the cover does not leak either.
	s.PanelSourceChanged("STANDBY")
	if got, ok := s.CurrentSpotifyMeta(); ok {
		t.Fatalf("standby: got %+v, want none", got)
	}

	// Back on the Spotify stream: the song and its cover return.
	s.PanelSourceChanged(panelUPnPSource)
	got, ok = s.CurrentSpotifyMeta()
	if !ok || got.Track != "Weightless" || got.Art != "https://i.scdn.co/image/weightless.jpg" {
		t.Fatalf("resumed: got %+v ok=%v", got, ok)
	}

	// The user starts a radio station (still on UPnP): it is not Spotify's
	// song any more, so the fallback stands down.
	s.setLastPlay("http://192.0.2.1:8888/stream/9", "Radio X", "", "audio/mpeg")
	if got, ok := s.CurrentSpotifyMeta(); ok {
		t.Fatalf("radio: got %+v, want none", got)
	}
}
