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
	"time"
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

// A preset switch must not pair the new playlist with the old song.
//
// A cold recall names the new context at once, while the cache and /status
// still describe the previous track until the paused load lands. The desktop
// app printed that reply as it came: `Playlist: "Purpose for Pain" · Rush - Tom
// Sawyer` for about ten seconds after switching keys (discussion #1077).
func TestInfoReportsNoSongWhileARecallIsLoading(t *testing.T) {
	const (
		oldTrack = `{"track":{"uri":"spotify:track:OLD","name":"Tom Sawyer","artist_names":["Rush"],"album_cover_url":"http://c/old.jpg"}}`
		newTrack = `{"track":{"uri":"spotify:track:NEW","name":"Purpose","artist_names":["B"],"album_cover_url":"http://c/new.jpg"}}`
	)
	cases := []struct {
		name        string
		status      string
		code        int
		wantTrack   string
		wantCover   string
		wantContext string
	}{
		{name: "engine still on the old track", status: oldTrack, code: 200, wantContext: "spotify:playlist:NEW"},
		{name: "engine idle mid-load", status: `{"track":null}`, code: 200},
		{name: "engine not answering", code: 500, wantContext: "spotify:playlist:NEW"},
		{name: "new track loaded", status: newTrack, code: 200, wantTrack: "Purpose", wantCover: "http://c/new.jpg", wantContext: "spotify:playlist:NEW"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.code != 200 {
					w.WriteHeader(c.code)
					return
				}
				_, _ = w.Write([]byte(c.status))
			}))
			defer srv.Close()

			m := newTestManagerAt(t, srv.URL)
			m.mu.Lock()
			m.lastContext = "spotify:playlist:OLD"
			m.curName, m.curArtist, m.curCover = "Tom Sawyer", "Rush", "http://c/old.jpg"
			m.curTrackURI = "spotify:track:OLD"
			// What Play does on a cold recall.
			m.armRecallDisplayLocked("spotify:playlist:NEW", time.Now())
			m.lastContext = "spotify:playlist:NEW"
			m.curTrackURI = ""
			m.mu.Unlock()

			got := serveInfoOnce(t, m)
			if got.Track != c.wantTrack || got.Cover != c.wantCover {
				t.Errorf("track/cover = %q/%q, want %q/%q", got.Track, got.Cover, c.wantTrack, c.wantCover)
			}
			if c.wantTrack == "" && got.Artist != "" {
				t.Errorf("artist = %q, want empty while the recall loads", got.Artist)
			}
			if got.Context != c.wantContext {
				t.Errorf("context = %q, want %q", got.Context, c.wantContext)
			}
		})
	}
}

// The gate is bounded: a recall that never confirms (the engine resumed the
// very track that was already playing, an event got lost) must not blank the
// song for good.
func TestRecallDisplayGateExpires(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"track":{"uri":"spotify:track:SAME","name":"Song","artist_names":["A"]}}`))
	}))
	defer srv.Close()
	m := newTestManagerAt(t, srv.URL)
	m.mu.Lock()
	m.curName, m.curTrackURI = "Song", "spotify:track:SAME"
	m.armRecallDisplayLocked("spotify:playlist:NEW", time.Now().Add(-recallDisplayWindow-time.Second))
	m.mu.Unlock()
	if got := serveInfoOnce(t, m); got.Track != "Song" {
		t.Errorf("track = %q after the window, want the live one", got.Track)
	}
}

// will_play naming the recalled context makes the next metadata event the
// confirmation, even when that track has the same name as the old one; a
// metadata event for the OLD track before that does not end the gate.
func TestRecallDisplayConfirmsOnTheNewContextsMetadata(t *testing.T) {
	m := newTestManagerAt(t, "http://127.0.0.1:1")
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.curName, m.curTrackURI = "Tom Sawyer", "spotify:track:OLD"
	m.armRecallDisplayLocked("spotify:playlist:NEW", now)

	m.noteRecallTrackLocked("spotify:track:OLD", "Tom Sawyer", true)
	if !m.recallDisplayPendingLocked(now) {
		t.Fatal("a late metadata event for the old track ended the gate")
	}
	m.noteRecallContextLocked("spotify:playlist:OTHER")
	m.noteRecallTrackLocked("spotify:track:OLD", "Tom Sawyer", true)
	if !m.recallDisplayPendingLocked(now) {
		t.Fatal("will_play for another context armed the confirmation")
	}
	// The engine announces the station wrapper for the same playlist.
	m.noteRecallContextLocked("spotify:station:playlist:NEW")
	m.noteRecallTrackLocked("spotify:track:OLD", "Tom Sawyer", true)
	if m.recallDisplayPendingLocked(now) {
		t.Fatal("metadata after will_play for the recalled context did not end the gate")
	}
}

type infoReply struct {
	Track   string `json:"track"`
	Artist  string `json:"artist"`
	Cover   string `json:"cover"`
	Context string `json:"context"`
}

func serveInfoOnce(t *testing.T, m *Manager) infoReply {
	t.Helper()
	rr := httptest.NewRecorder()
	m.ServeInfo(rr, httptest.NewRequest("GET", "/spotify/info", nil))
	var got infoReply
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rr.Body.String())
	}
	return got
}
