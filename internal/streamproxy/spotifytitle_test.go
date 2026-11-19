package streamproxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
)

// titleReply decodes the /api/stream/title JSON body.
type titleReply struct {
	Title   string `json:"title"`
	Swapped bool   `json:"swapped"`
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
// not even consulted, so a radio track is never mislabelled.
func TestHandleTitleRadioTitleWins(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.curTitle = "Queen - Under Pressure"
	s.SetSpotifyTitleProvider(func() (string, bool) {
		t.Fatal("provider consulted while a radio title is present")
		return "", false
	})
	if got := callTitle(t, s); got.Title != "Queen - Under Pressure" {
		t.Fatalf("title = %q", got.Title)
	}
}

// With no radio title and Spotify playing, the live Spotify song is served and
// marked not swapped (the swap toggle is a per-radio-station setting).
func TestHandleTitleSpotifyFallback(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.SetSpotifyTitleProvider(func() (string, bool) { return "Miley Cyrus - Flowers", true })
	got := callTitle(t, s)
	if got.Title != "Miley Cyrus - Flowers" || got.Swapped {
		t.Fatalf("title = %q swapped = %v", got.Title, got.Swapped)
	}
}

// With no radio title and nothing playing on Spotify, the reply stays empty.
func TestHandleTitleNoFallback(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.SetSpotifyTitleProvider(func() (string, bool) { return "", false })
	if got := callTitle(t, s); got.Title != "" {
		t.Fatalf("title = %q, want empty", got.Title)
	}
	// A provider that returns ok=true but an empty string is also treated as
	// nothing to show.
	s.SetSpotifyTitleProvider(func() (string, bool) { return "", true })
	if got := callTitle(t, s); got.Title != "" {
		t.Fatalf("empty-string provider: title = %q, want empty", got.Title)
	}
}

// No provider wired (the default): the endpoint behaves exactly as before.
func TestHandleTitleNoProvider(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if got := callTitle(t, s); got.Title != "" {
		t.Fatalf("title = %q, want empty", got.Title)
	}
}
