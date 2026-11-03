package spotify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The last-resort re-Play of a recall must only run when the playlist never
// loaded. A loaded playlist whose box merely failed to attach keeps its song:
// a re-Play of a shuffle preset picks a new random one, and the Spotify app
// shows the first as skipped (maintainer's ST30, 2026-11-02).
func TestHoldsContextOnlyWhenTheEngineLoadedThatPlaylist(t *testing.T) {
	const playlist = "spotify:playlist:abc"
	withTrack := `{"track":{"uri":"spotify:track:1","name":"Song"}}`
	cases := []struct {
		name      string
		announced string
		status    string
		down      bool
		want      bool
	}{
		{name: "loaded, track in it", announced: playlist, status: withTrack, want: true},
		{name: "station wrapper of the same playlist", announced: "spotify:station:playlist:abc", status: withTrack, want: true},
		{name: "engine never announced anything", announced: "", status: withTrack, want: false},
		{name: "another playlist is loaded", announced: "spotify:playlist:other", status: withTrack, want: false},
		{name: "announced but no track (session transferred away)", announced: playlist, status: `{"track":null}`, want: false},
		{name: "engine does not answer", announced: playlist, down: true, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.down {
					http.Error(w, "down", http.StatusServiceUnavailable)
					return
				}
				_, _ = w.Write([]byte(c.status))
			}))
			defer srv.Close()
			m := newTestManagerAt(t, srv.URL)
			m.mu.Lock()
			m.engineContext = c.announced
			// A recall writes lastContext before anything loads; it must not
			// count as the engine holding the playlist.
			m.lastContext = playlist
			m.mu.Unlock()
			if got := m.HoldsContext(context.Background(), playlist); got != c.want {
				t.Fatalf("HoldsContext = %v, want %v", got, c.want)
			}
		})
	}
}
