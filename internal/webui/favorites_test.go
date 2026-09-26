package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func favServer(t *testing.T) *Server {
	t.Helper()
	old := favoritesPath
	favoritesPath = filepath.Join(t.TempDir(), "favorites.json")
	t.Cleanup(func() { favoritesPath = old })
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func putFavs(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, "/api/favorites", strings.NewReader(body))
	r.RemoteAddr = "192.168.178.20:5555"
	w := httptest.NewRecorder()
	s.handleFavorites(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func getFavs(t *testing.T, s *Server) []Favorite {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/favorites", nil)
	w := httptest.NewRecorder()
	s.handleFavorites(w, r)
	var out []Favorite
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET did not return a list: %s", w.Body.String())
	}
	return out
}

// A speaker with no favourites answers an empty LIST, never null. The phone
// page renders a list, and null is the shape that makes it render nothing at
// all without saying why.
func TestFavoritesEmptyIsAList(t *testing.T) {
	s := favServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/favorites", nil)
	w := httptest.NewRecorder()
	s.handleFavorites(w, r)
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("want [], got %q", body)
	}
}

func TestFavoritesRoundTrip(t *testing.T) {
	s := favServer(t)
	code, out := putFavs(t, s, `[{"stationuuid":"a","name":"1LIVE","url_resolved":"http://example/1live"}]`)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if out["count"] != float64(1) || out["written"] != true {
		t.Errorf("unexpected answer: %v", out)
	}
	got := getFavs(t, s)
	if len(got) != 1 || got[0].Name != "1LIVE" {
		t.Errorf("stored list = %+v", got)
	}
}

// A row with no name or nothing to play is not a station and must not reach
// the speaker's flash.
func TestFavoritesDropsUnplayableRows(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, `[
	  {"name":"good","url_resolved":"http://example/ok"},
	  {"name":"","url_resolved":"http://example/noname"},
	  {"name":"no stream"},
	  {"name":"  ","url":"http://example/blank"}
	]`)
	got := getFavs(t, s)
	if len(got) != 1 || got[0].Name != "good" {
		t.Errorf("only the playable row should survive, got %+v", got)
	}
}

func TestFavoritesDeduplicates(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, `[
	  {"stationuuid":"x","name":"A","url":"http://e/a"},
	  {"stationuuid":"x","name":"A again","url":"http://e/a"},
	  {"name":"B","url":"http://e/b"},
	  {"name":"B","url":"http://e/b"}
	]`)
	if got := getFavs(t, s); len(got) != 2 {
		t.Errorf("duplicates should collapse, got %d: %+v", len(got), got)
	}
}

// The speaker's flash is not unlimited and a broken client must not be able to
// grow the file for ever.
func TestFavoritesCapsTheList(t *testing.T) {
	s := favServer(t)
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < maxFavorites+50; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"name":"s`)
		sb.WriteString(strings.Repeat("x", 1))
		sb.WriteString(string(rune('A' + i%26)))
		sb.WriteString(`","url":"http://e/`)
		sb.WriteString(string(rune('A' + i%26)))
		sb.WriteString(strings.Repeat("y", i%7+1))
		sb.WriteString(`"}`)
	}
	sb.WriteString("]")
	putFavs(t, s, sb.String())
	if got := getFavs(t, s); len(got) > maxFavorites {
		t.Errorf("list must be capped at %d, got %d", maxFavorites, len(got))
	}
}

// The app re-pushes on every discovery cycle. An unchanged list must not touch
// the flash, or that is a steady drip of NAND wear for nothing.
func TestFavoritesUnchangedListIsNotWritten(t *testing.T) {
	s := favServer(t)
	body := `[{"name":"1LIVE","url_resolved":"http://example/1live"}]`
	if _, out := putFavs(t, s, body); out["written"] != true {
		t.Fatal("the first write must happen")
	}
	if _, out := putFavs(t, s, body); out["written"] != false {
		t.Error("an identical list must not be written again")
	}
}

// A damaged file reads as empty. The stars are a convenience and must never
// stop the page from loading.
func TestFavoritesDamagedFileReadsAsEmpty(t *testing.T) {
	s := favServer(t)
	if err := os.WriteFile(favoritesPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := getFavs(t, s); len(got) != 0 {
		t.Errorf("a damaged file must read as empty, got %+v", got)
	}
}

// Reading is open to the phone page; writing stays on the LAN like every other
// write to the speaker.
func TestFavoritesWriteIsLANOnly(t *testing.T) {
	s := favServer(t)
	r := httptest.NewRequest(http.MethodPut, "/api/favorites", strings.NewReader(`[]`))
	r.RemoteAddr = "203.0.113.9:5555"
	w := httptest.NewRecorder()
	s.handleFavorites(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 off-LAN, got %d", w.Code)
	}
}
