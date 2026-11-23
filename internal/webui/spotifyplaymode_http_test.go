package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/keyactions"
)

// newPlayModeAPIServer is the key tests' server plus the state read the app's
// answer comes from.
func newPlayModeAPIServer(streaming bool, source, location string) (*Server, *playModeFake) {
	s, f := newPlayModeServer(streaming, source, location)
	s.spotifyPlayModes = func(context.Context) (bool, string, error) {
		return f.shuffle, f.repeat, nil
	}
	return s, f
}

func callPlayMode(s *Server, method, body string) (*httptest.ResponseRecorder, map[string]any) {
	w := httptest.NewRecorder()
	s.handleSpotifyPlayMode(w, httptest.NewRequest(method, "/api/spotify/playmode", strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestPlayModeAPIShuffleFlipsWithoutANotice(t *testing.T) {
	s, f := newPlayModeAPIServer(true, "UPNP", "")
	f.repeat = "context"
	w, out := callPlayMode(s, http.MethodPost, `{"action":"shuffle"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if out["ok"] != true || out["shuffle"] != true || out["repeat"] != "context" {
		t.Fatalf("answer = %v", out)
	}
	if f.shuffled != 1 || len(f.notices) != 0 {
		t.Fatalf("shuffled=%d notices=%v: the app's press must not show a notice", f.shuffled, f.notices)
	}
	_, out = callPlayMode(s, http.MethodPost, `{"action":"shuffle"}`)
	if out["shuffle"] != false {
		t.Fatalf("second press: answer = %v", out)
	}
}

func TestPlayModeAPIRepeatStepsLikeTheKey(t *testing.T) {
	s, f := newPlayModeAPIServer(true, "UPNP", "")
	for _, want := range []string{"context", "track", "off"} {
		w, out := callPlayMode(s, http.MethodPost, `{"action":"repeat"}`)
		if w.Code != http.StatusOK || out["repeat"] != want || out["shuffle"] != false {
			t.Fatalf("status %d answer %v, want repeat %q", w.Code, out, want)
		}
	}
	if len(f.notices) != 0 {
		t.Fatalf("notices = %v", f.notices)
	}
}

// An app is not a bouncing key: two presses in a row both step, also right
// after a key press.
func TestPlayModeAPIIsNotDebouncedLikeAKey(t *testing.T) {
	s, f := newPlayModeAPIServer(true, "UPNP", "")
	s.PlayModeKeyPress(keyactions.ActionShuffle)
	callPlayMode(s, http.MethodPost, `{"action":"shuffle"}`)
	callPlayMode(s, http.MethodPost, `{"action":"shuffle"}`)
	if f.shuffled != 3 {
		t.Fatalf("shuffled %d times, want 3", f.shuffled)
	}
}

// A paused playlist still counts, as for the key.
func TestPlayModeAPIWorksOnAPausedPlaylist(t *testing.T) {
	s, f := newPlayModeAPIServer(false, "UPNP", "http://127.0.0.1:8888/spotify/stream-4.ogg")
	if w, _ := callPlayMode(s, http.MethodPost, `{"action":"repeat"}`); w.Code != http.StatusOK || f.repeat != "context" {
		t.Fatalf("status %d repeat %q", w.Code, f.repeat)
	}
}

func TestPlayModeAPIRefusesOffSpotify(t *testing.T) {
	s, f := newPlayModeAPIServer(false, "UPNP", "http://127.0.0.1:8888/stream/1")
	w, out := callPlayMode(s, http.MethodPost, `{"action":"shuffle"}`)
	if w.Code != http.StatusConflict || out["reason"] != "not-playing" {
		t.Fatalf("status %d answer %v", w.Code, out)
	}
	if f.shuffled != 0 {
		t.Fatal("radio took the shuffle")
	}
}

func TestPlayModeAPIWithoutEngine(t *testing.T) {
	s, _ := newPlayModeAPIServer(true, "UPNP", "")
	s.spotifyToggleShuffle, s.spotifyCycleRepeat, s.spotifyPlayModes = nil, nil, nil
	w, out := callPlayMode(s, http.MethodPost, `{"action":"repeat"}`)
	if w.Code != http.StatusConflict || out["reason"] != "no-engine" {
		t.Fatalf("POST: status %d answer %v", w.Code, out)
	}
	w, out = callPlayMode(s, http.MethodGet, "")
	if w.Code != http.StatusConflict || out["reason"] != "no-engine" {
		t.Fatalf("GET: status %d answer %v", w.Code, out)
	}
}

func TestPlayModeAPIEngineRefusal(t *testing.T) {
	s, _ := newPlayModeAPIServer(true, "UPNP", "")
	s.spotifyCycleRepeat = func(context.Context) (string, error) { return "", errors.New("engine down") }
	w, out := callPlayMode(s, http.MethodPost, `{"action":"repeat"}`)
	if w.Code != http.StatusBadGateway || out["reason"] != "engine-refused" {
		t.Fatalf("status %d answer %v", w.Code, out)
	}
}

// The mode a press changed comes from the step; when the read of the other
// one fails it is left out, never guessed.
func TestPlayModeAPIAnswersTheStepWhenTheReadFails(t *testing.T) {
	s, _ := newPlayModeAPIServer(true, "UPNP", "")
	s.spotifyPlayModes = func(context.Context) (bool, string, error) { return false, "", errors.New("timeout") }
	w, out := callPlayMode(s, http.MethodPost, `{"action":"shuffle"}`)
	if w.Code != http.StatusOK || out["shuffle"] != true {
		t.Fatalf("status %d answer %v", w.Code, out)
	}
	if _, ok := out["repeat"]; ok {
		t.Fatalf("repeat guessed: %v", out)
	}
}

func TestPlayModeAPIBadBody(t *testing.T) {
	s, f := newPlayModeAPIServer(true, "UPNP", "")
	for _, body := range []string{`{}`, `{"action":"sleep"}`} {
		w, out := callPlayMode(s, http.MethodPost, body)
		if w.Code != http.StatusBadRequest || out["reason"] != "bad-body" {
			t.Fatalf("%s: status %d answer %v", body, w.Code, out)
		}
	}
	if w, _ := callPlayMode(s, http.MethodPut, `{"action":"shuffle"}`); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT: status %d", w.Code)
	}
	if f.shuffled != 0 || f.repeat != "off" {
		t.Fatal("a refused body changed the engine")
	}
}

func TestPlayModeAPIGetReadsWithoutChanging(t *testing.T) {
	s, f := newPlayModeAPIServer(false, "STANDBY", "")
	f.shuffle, f.repeat = true, "track"
	w, out := callPlayMode(s, http.MethodGet, "")
	if w.Code != http.StatusOK || out["shuffle"] != true || out["repeat"] != "track" {
		t.Fatalf("status %d answer %v", w.Code, out)
	}
	if f.shuffled != 0 {
		t.Fatal("a GET changed the shuffle")
	}
}

type playModeReaderFake struct{ baseSpotifyFake }

func (playModeReaderFake) PlayModes(context.Context) (bool, string, error) {
	return true, "context", nil
}

func TestWithSpotifyWiresThePlayModeReader(t *testing.T) {
	s := &Server{}
	WithSpotify(baseSpotifyFake{})(s)
	if s.spotifyPlayModes != nil {
		t.Fatal("set although the engine has no reader")
	}
	WithSpotify(playModeReaderFake{})(s)
	if s.spotifyPlayModes == nil {
		t.Fatal("the reader was not wired")
	}
	if on, mode, err := s.spotifyPlayModes(context.Background()); !on || mode != "context" || err != nil {
		t.Fatalf("got %v %q %v", on, mode, err)
	}
}

// The route is registered on every speaker, so an app never gets the index's
// 200 + HTML for it, and the version endpoint says whether the steps exist.
func TestPlayModeAPIAdvertisedInVersion(t *testing.T) {
	s, _ := newPlayModeAPIServer(true, "UPNP", "")
	w := httptest.NewRecorder()
	s.handleAgentVersion(w, httptest.NewRequest(http.MethodGet, "/api/agent/version", nil))
	var out map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["spotifyPlayModes"] != "true" {
		t.Fatalf("version = %v, want spotifyPlayModes", out)
	}
	s.spotifyToggleShuffle = nil
	w = httptest.NewRecorder()
	s.handleAgentVersion(w, httptest.NewRequest(http.MethodGet, "/api/agent/version", nil))
	out = nil
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if _, ok := out["spotifyPlayModes"]; ok {
		t.Fatalf("advertised without the engine's steps: %v", out)
	}
}
