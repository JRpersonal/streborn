package webui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newIndexServer() *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// The page must never be cached without revalidation. It carried no caching
// instruction at all, so browsers applied their own heuristic and could hold a
// stale remote across an agent update: caught live on 2026-08-06 with the box
// already serving a corrected page while the browser showed the old one. On a
// page saved to the home screen there is not even a reload button to reach for.
func TestIndexRevalidatesAndOffersAnETag(t *testing.T) {
	s := newIndexServer()
	w := httptest.NewRecorder()
	s.handleIndex(w, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-cache")
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag; without one every revalidation costs a full page")
	}
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Errorf("ETag %q is not quoted", etag)
	}
	if w.Body.Len() == 0 {
		t.Error("first response carried no page")
	}
}

// A speaker that has not changed must answer 304 with no body, so revalidating
// on every open stays as cheap as a cache hit on hardware this old.
func TestIndexAnswers304ForAKnownETag(t *testing.T) {
	s := newIndexServer()
	first := httptest.NewRecorder()
	s.handleIndex(first, httptest.NewRequest(http.MethodGet, "/", nil))
	etag := first.Header().Get("ETag")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	s.handleIndex(second, req)

	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 carried %d bytes of body, want none", second.Body.Len())
	}
}

// A stale tag must get the new page, not a 304. This is the case that decides
// whether an agent update reaches the user at all.
func TestIndexServesThePageForAStaleETag(t *testing.T) {
	s := newIndexServer()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", `"0000000000000000"`)
	w := httptest.NewRecorder()
	s.handleIndex(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a stale tag", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Error("stale tag got an empty body")
	}
}

// Two speakers serve different bytes from the same build, because the page
// carries the speaker's name. Their tags must differ, or one speaker's cached
// page would be served as valid for the other.
func TestIndexETagCoversTheSpeakerName(t *testing.T) {
	a := indexETag("<title>Kitchen</title>")
	b := indexETag("<title>Bathroom</title>")
	if a == b {
		t.Error("two different pages produced the same ETag")
	}
}
