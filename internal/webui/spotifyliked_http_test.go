package webui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postLiked(s *Server, method, body string) (*httptest.ResponseRecorder, map[string]any) {
	w := httptest.NewRecorder()
	s.handleSpotifyLiked(w, httptest.NewRequest(method, "/api/spotify/liked", strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestSpotifyLikedSavesThroughTheKeyPath(t *testing.T) {
	s, f := newLikeServer(true, "UPNP", "")
	w, out := postLiked(s, http.MethodPost, `{"liked":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if out["ok"] != true || out["liked"] != true || out["uri"] != f.track.URI || out["name"] != "A Song" || out["repeat"] != false {
		t.Fatalf("answer = %v", out)
	}
	if f.calls != 1 || f.unlikes != 0 {
		t.Fatalf("likes=%d unlikes=%d, want 1 and 0", f.calls, f.unlikes)
	}
	if len(f.notices) != 1 || f.notices[0].Line1 != "LIKED" {
		t.Fatalf("notices = %+v", f.notices)
	}
	if !strings.Contains(f.log.String(), "control API: saved the playing song to Liked Songs") {
		t.Fatalf("log: %s", f.log.String())
	}
}

func TestSpotifyLikedFalseRemoves(t *testing.T) {
	s, f := newLikeServer(false, "UPNP", "http://127.0.0.1:8888/spotify/stream-4.ogg")
	f.track.Repeat = true
	w, out := postLiked(s, http.MethodPost, `{"liked":false}`)
	if w.Code != http.StatusOK || out["liked"] != false || out["repeat"] != true {
		t.Fatalf("status = %d, answer = %v", w.Code, out)
	}
	if f.calls != 0 || f.unlikes != 1 {
		t.Fatalf("likes=%d unlikes=%d, want 0 and 1", f.calls, f.unlikes)
	}
	if len(f.notices) != 1 || f.notices[0].Line1 != "REMOVED" {
		t.Fatalf("notices = %+v", f.notices)
	}
}

// An app is not a bouncing key: a save right after a key press, and two
// requests in a row, all reach the engine (which keeps the repeat memory).
func TestSpotifyLikedIsNotDebouncedLikeAKey(t *testing.T) {
	s, f := newLikeServer(true, "UPNP", "")
	s.LikeKeyPress()
	postLiked(s, http.MethodPost, `{"liked":true}`)
	postLiked(s, http.MethodPost, `{"liked":true}`)
	if f.calls != 3 {
		t.Fatalf("engine asked %d times, want 3", f.calls)
	}
}

func TestSpotifyLikedNotPlayingIs409(t *testing.T) {
	for _, tc := range []struct{ name, source, location string }{
		{"radio on STR", "UPNP", "http://127.0.0.1:8888/stream/1"},
		{"the box's own Spotify", "SPOTIFY", ""},
		{"standby", "STANDBY", ""},
		{"unreadable", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newLikeServer(false, tc.source, tc.location)
			w, out := postLiked(s, http.MethodPost, `{"liked":true}`)
			if w.Code != http.StatusConflict || out["reason"] != "not-playing" || out["error"] == "" {
				t.Fatalf("status = %d, answer = %v", w.Code, out)
			}
			if f.calls != 0 || len(f.notices) != 0 {
				t.Fatalf("calls=%d notices=%d", f.calls, len(f.notices))
			}
		})
	}
}

func TestSpotifyLikedWithoutEngineIs409(t *testing.T) {
	s, _ := newLikeServer(true, "UPNP", "")
	s.spotifyLikeCurrent = nil
	w, out := postLiked(s, http.MethodPost, `{"liked":true}`)
	if w.Code != http.StatusConflict || out["reason"] != "no-engine" {
		t.Fatalf("status = %d, answer = %v", w.Code, out)
	}
}

func TestSpotifyLikedEngineRefusal(t *testing.T) {
	for _, tc := range []struct {
		reason string
		code   int
	}{
		{"forbidden-403", http.StatusBadGateway},
		{"engine-too-old", http.StatusBadGateway},
		{"no-track", http.StatusConflict},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			s, f := newLikeServer(true, "UPNP", "")
			f.err = &SpotifyLikeError{Reason: tc.reason, Err: errors.New("refused")}
			w, out := postLiked(s, http.MethodPost, `{"liked":true}`)
			if w.Code != tc.code || out["reason"] != tc.reason {
				t.Fatalf("status = %d, answer = %v", w.Code, out)
			}
			if len(f.notices) != 0 {
				t.Fatalf("a refused save showed a notice: %+v", f.notices)
			}
		})
	}
}

func TestSpotifyLikedBadBodyIs400(t *testing.T) {
	for _, body := range []string{``, `not json`, `{}`, `{"liked":"yes"}`, `{"liked":null}`} {
		s, f := newLikeServer(true, "UPNP", "")
		w, _ := postLiked(s, http.MethodPost, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d", body, w.Code)
		}
		if f.calls != 0 || f.unlikes != 0 {
			t.Fatalf("body %q reached the engine", body)
		}
	}
}

func TestSpotifyLikedWrongMethodIs405(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		s, f := newLikeServer(true, "UPNP", "")
		w, _ := postLiked(s, m, `{"liked":true}`)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: status = %d", m, w.Code)
		}
		if f.calls != 0 {
			t.Fatalf("%s reached the engine", m)
		}
	}
}
