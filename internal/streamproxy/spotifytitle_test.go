package streamproxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
)

// titleReply decodes the /api/stream/title JSON body. The three component
// fields are omitempty on the wire, so an absent one decodes to "".
type titleReply struct {
	Title   string `json:"title"`
	Swapped bool   `json:"swapped"`
	Track   string `json:"track"`
	Artist  string `json:"artist"`
	Art     string `json:"art"`
}

func callTitle(t *testing.T, s *Server) titleReply {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleTitle(w, httptest.NewRequest("GET", "/api/stream/title", nil))
	var got titleReply
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return got
}

// The radio StreamTitle wins whenever it is present: the Spotify fallback is
// not even consulted, so a radio track is never mislabelled and no component
// fields are emitted.
func TestHandleTitleRadioTitleWins(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.curTitle = "Queen - Under Pressure"
	s.SetSpotifyTitleProvider(func() (SpotifyTitleMeta, bool) {
		t.Fatal("provider consulted while a radio title is present")
		return SpotifyTitleMeta{}, false
	})
	got := callTitle(t, s)
	if got.Title != "Queen - Under Pressure" {
		t.Fatalf("title = %q", got.Title)
	}
	if got.Track != "" || got.Artist != "" || got.Art != "" {
		t.Fatalf("radio stream carried component fields: %+v", got)
	}
}

// With no radio title and Spotify playing, the live Spotify song is served and
// marked not swapped (the swap toggle is a per-radio-station setting), with the
// artist, track and cover alongside so a companion app can render the cover.
func TestHandleTitleSpotifyFallback(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.SetSpotifyTitleProvider(func() (SpotifyTitleMeta, bool) {
		return SpotifyTitleMeta{
			Title:  "Miley Cyrus - Flowers",
			Track:  "Flowers",
			Artist: "Miley Cyrus",
			Art:    "https://i.scdn.co/image/flowers.jpg",
		}, true
	})
	got := callTitle(t, s)
	if got.Title != "Miley Cyrus - Flowers" || got.Swapped {
		t.Fatalf("title = %q swapped = %v", got.Title, got.Swapped)
	}
	if got.Track != "Flowers" || got.Artist != "Miley Cyrus" || got.Art != "https://i.scdn.co/image/flowers.jpg" {
		t.Fatalf("components = %+v", got)
	}
}

// With no radio title and nothing playing on Spotify, the reply stays empty and
// no component fields appear.
func TestHandleTitleNoFallback(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.SetSpotifyTitleProvider(func() (SpotifyTitleMeta, bool) { return SpotifyTitleMeta{}, false })
	if got := callTitle(t, s); got.Title != "" || got.Track != "" || got.Artist != "" || got.Art != "" {
		t.Fatalf("got %+v, want empty", got)
	}
	// A provider that returns ok=true but an empty title is also treated as
	// nothing to show, and its components are dropped too.
	s.SetSpotifyTitleProvider(func() (SpotifyTitleMeta, bool) {
		return SpotifyTitleMeta{Track: "x", Artist: "y", Art: "z"}, true
	})
	if got := callTitle(t, s); got.Title != "" || got.Track != "" || got.Artist != "" || got.Art != "" {
		t.Fatalf("empty-title provider: got %+v, want empty", got)
	}
}

// No provider wired (the default): the endpoint behaves exactly as before.
func TestHandleTitleNoProvider(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if got := callTitle(t, s); got.Title != "" || got.Track != "" || got.Artist != "" || got.Art != "" {
		t.Fatalf("got %+v, want empty", got)
	}
}
