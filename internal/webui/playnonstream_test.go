package webui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/JRpersonal/streborn/internal/recent"
	"github.com/JRpersonal/streborn/internal/upnp"
)

// A spotify: URI sent to POST /api/play is no stream. It used to wake the
// speaker, end in AUDIO_ERROR_BAD_URL and still be recorded as a "radio"
// Recently-played card, which then relabelled the real Spotify card of the
// same URI as "Radio" (#1077). It must be refused before the box is touched
// and before anything is recorded.
func TestPlayRefusesANonStreamURL(t *testing.T) {
	var boxHits atomic.Int32
	box := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		boxHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(box.Close)
	s := &Server{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		queue:    newPlayQueue(),
		renderer: &upnp.Renderer{ControlURL: box.URL, Client: box.Client()},
		recent:   recent.New(),
	}

	for _, u := range []string{"spotify:playlist:abc", "ftp://example.invalid/x"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/play",
			strings.NewReader(`{"url":"`+u+`","title":"Gypsy Soul"}`))
		s.handlePlay(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (body %q)", u, w.Code, w.Body.String())
		}
	}
	if n := boxHits.Load(); n != 0 {
		t.Fatalf("the speaker was contacted %d times for a non-stream URL", n)
	}
	if all := s.recent.All(); len(all) != 0 {
		t.Fatalf("a refused play was recorded: %+v", all)
	}
}
