package spotify

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// newTestManagerAt builds a Manager pointed at a fake go-librespot API.
func newTestManagerAt(t *testing.T, apiURL string) *Manager {
	t.Helper()
	m := New("", filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.apiAddr = strings.TrimPrefix(apiURL, "http://")
	return m
}

// The context a preset save writes onto a key must never be a memory.
//
// /spotify/info hands out m.lastContext, which has three write sites and no
// clearing site: once the engine has seen a playlist, that URI is reported for
// the rest of the agent's life, whether anything is playing or not. The desktop
// app reads exactly this field at save time and writes it onto the key.
//
// On a speaker where Spotify refuses every track (a restricted account, a free
// account, a dropped session) the engine never loads one, so every save captured
// the same old playlist. The visible symptom is the save being refused as
// "already on key N" once that playlist is on a key; the invisible one is worse,
// because without that collision the key is silently written with the wrong
// playlist. Marten Meeuw, two SoundTouch 10s, 2026-09-26.
func TestInfoDropsTheContextWhenNothingIsLoaded(t *testing.T) {
	cases := []struct {
		name        string
		status      string
		wantContext string
		wantTrack   string
	}{
		{
			// A track IS loaded: the context is real and must survive.
			name:        "track loaded",
			status:      `{"track":{"uri":"spotify:track:1","name":"Song","artist_names":["A"],"album_cover_url":"http://c/1.jpg"}}`,
			wantContext: "spotify:playlist:OLD",
			wantTrack:   "Song",
		},
		{
			// The engine answered and holds nothing. The remembered playlist is
			// from an older session and must not reach a save.
			name:        "engine idle",
			status:      `{"track":null}`,
			wantContext: "",
			wantTrack:   "Cached Song",
		},
		{
			// Same shape a stopped go-librespot sends: a track object with no name.
			name:        "track without a name",
			status:      `{"track":{"uri":"","name":""}}`,
			wantContext: "",
			wantTrack:   "Cached Song",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/status") {
					_, _ = w.Write([]byte(c.status))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer srv.Close()

			m := newTestManagerAt(t, srv.URL)
			m.mu.Lock()
			m.lastContext = "spotify:playlist:OLD"
			m.curName, m.curArtist = "Cached Song", "Cached Artist"
			m.mu.Unlock()

			rr := httptest.NewRecorder()
			m.ServeInfo(rr, httptest.NewRequest("GET", "/spotify/info", nil))
			var got struct {
				Track   string `json:"track"`
				Context string `json:"context"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v (%s)", err, rr.Body.String())
			}
			if got.Context != c.wantContext {
				t.Errorf("context = %q, want %q", got.Context, c.wantContext)
			}
			// The DISPLAY may keep the cached track: that is cosmetic. Only what
			// gets written to a key has to be certain.
			if got.Track != c.wantTrack {
				t.Errorf("track = %q, want %q", got.Track, c.wantTrack)
			}
		})
	}
}

// An engine that cannot be reached is the opposite case and must NOT be treated
// as "nothing is loaded": the cache is then the best answer there is, and
// blanking the context on a failed read would break saving a preset every time
// the status call happens to time out.
func TestInfoKeepsTheContextWhenTheEngineCannotBeAsked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	m := newTestManagerAt(t, srv.URL)
	m.mu.Lock()
	m.lastContext = "spotify:playlist:OLD"
	m.curName = "Cached Song"
	m.mu.Unlock()

	rr := httptest.NewRecorder()
	m.ServeInfo(rr, httptest.NewRequest("GET", "/spotify/info", nil))
	var got struct {
		Context string `json:"context"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Context != "spotify:playlist:OLD" {
		t.Errorf("context = %q, want the cached one: a failed read is not proof of an idle engine", got.Context)
	}
}
