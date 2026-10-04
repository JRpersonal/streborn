package webui

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/upnp"
)

// fakeRenderer records every SOAP action the speaker would have received.
type fakeRenderer struct {
	mu      sync.Mutex
	actions []string
	bodies  []string
}

func (f *fakeRenderer) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.actions...)
}

func (f *fakeRenderer) body(action string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.actions {
		if a == action {
			return f.bodies[i]
		}
	}
	return ""
}

// newDisplayMsgServer is a Server whose speaker is fully faked: display yes,
// idle, English, and a stop that runs only when the test fires it.
func newDisplayMsgServer(t *testing.T) (*Server, *fakeRenderer, *func()) {
	t.Helper()
	rec := &fakeRenderer{}
	box := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		action := r.Header.Get("SOAPAction")
		if i := strings.LastIndex(action, "#"); i >= 0 {
			action = strings.Trim(action[i+1:], `"`)
		}
		rec.mu.Lock()
		rec.actions = append(rec.actions, action)
		rec.bodies = append(rec.bodies, string(b))
		rec.mu.Unlock()
	}))
	t.Cleanup(box.Close)
	var pendingStop func()
	s := &Server{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		queue:    newPlayQueue(),
		renderer: &upnp.Renderer{ControlURL: box.URL, Client: box.Client()},
	}
	s.displayMsg.path = filepath.Join(t.TempDir(), "display-messages")
	s.displayMsg.settle = time.Nanosecond
	s.displayMsg.hasDisplayFn = func() (bool, bool) { return true, true }
	s.displayMsg.langFn = func() int { return 3 }
	s.displayMsg.nowPlayingFn = func() (boxNowPlaying, bool) {
		return boxNowPlaying{Source: "INVALID_SOURCE"}, true
	}
	s.displayMsg.after = func(_ time.Duration, f func()) { pendingStop = f }
	return s, rec, &pendingStop
}

func TestDisplayMessageShowsSilentTrackTitledWithTheMessage(t *testing.T) {
	s, rec, _ := newDisplayMsgServer(t)
	if got := s.showKeyMessage(DisplayMsgSpotifyLogin, false); got != "shown" {
		t.Fatalf("outcome = %q, want shown", got)
	}
	if got := rec.seen(); len(got) != 2 || got[0] != "SetAVTransportURI" || got[1] != "Play" {
		t.Fatalf("speaker got %v, want SetAVTransportURI then Play", got)
	}
	body := rec.body("SetAVTransportURI")
	if !strings.Contains(body, "Spotify: tap this speaker once in the Spotify app") {
		t.Errorf("the message is not the track title: %s", body)
	}
	if !strings.Contains(body, displayMsgAudioPath) {
		t.Errorf("the track is not the agent's silent message track: %s", body)
	}
	// Volume is never part of it.
	for _, a := range rec.seen() {
		if strings.Contains(strings.ToLower(a), "volume") {
			t.Fatalf("display message touched the volume: %v", rec.seen())
		}
	}
}

func TestDisplayMessageGuards(t *testing.T) {
	cases := []struct {
		name  string
		setup func(s *Server)
		want  string
	}{
		{"switched off", func(s *Server) { _ = persistFlagFile(s.displayMsg.path, "0") }, "switched off for this speaker"},
		{"no display", func(s *Server) { s.displayMsg.hasDisplayFn = func() (bool, bool) { return false, true } }, "speaker has no display"},
		{"standby", func(s *Server) {
			s.displayMsg.nowPlayingFn = func() (boxNowPlaying, bool) { return boxNowPlaying{Source: "STANDBY"}, true }
		}, "standby"},
		{"playing", func(s *Server) {
			s.displayMsg.nowPlayingFn = func() (boxNowPlaying, bool) {
				return boxNowPlaying{Source: "BLUETOOTH", PlayStatus: "PLAY_STATE"}, true
			}
		}, "something is playing"},
		{"state unknown", func(s *Server) {
			s.displayMsg.nowPlayingFn = func() (boxNowPlaying, bool) { return boxNowPlaying{}, false }
		}, "speaker state unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, rec, _ := newDisplayMsgServer(t)
			tc.setup(s)
			if got := s.showKeyMessage(DisplayMsgNoInternet, false); got != tc.want {
				t.Fatalf("outcome = %q, want %q", got, tc.want)
			}
			if got := rec.seen(); len(got) != 0 {
				t.Fatalf("a refused message still reached the speaker: %v", got)
			}
		})
	}
}

func TestDisplayMessageRepeatLimitAndStop(t *testing.T) {
	s, rec, stop := newDisplayMsgServer(t)
	if got := s.showKeyMessage(DisplayMsgStationDown, false); got != "shown" {
		t.Fatalf("first: %q", got)
	}
	// While it is on screen nothing else is pushed.
	if got := s.showKeyMessage(DisplayMsgSpotifyLogin, false); got != "another message is on screen" {
		t.Fatalf("overlap: %q", got)
	}
	// The stop runs only while the speaker still plays the message.
	s.displayMsg.nowPlayingFn = func() (boxNowPlaying, bool) {
		np := boxNowPlaying{Source: "UPNP", PlayStatus: "PLAY_STATE"}
		np.Item.Location = "http://127.0.0.1:8888" + displayMsgAudioPath + "?n=1"
		return np, true
	}
	(*stop)()
	got := rec.seen()
	if len(got) != 4 || got[2] != "Stop" || got[3] != "SetAVTransportURI" {
		t.Fatalf("after the hold the speaker got %v, want Stop then an emptied transport", got)
	}
	s.displayMsg.nowPlayingFn = func() (boxNowPlaying, bool) { return boxNowPlaying{Source: "INVALID_SOURCE"}, true }
	if got := s.showKeyMessage(DisplayMsgStationDown, false); got != "shown recently" {
		t.Fatalf("repeat: %q", got)
	}
	// The app's preview ignores the repeat limit, nothing else.
	if got := s.showKeyMessage(DisplayMsgStationDown, true); got != "shown" {
		t.Fatalf("preview: %q", got)
	}
}

func TestDisplayMessageStopLeavesANewPlayAlone(t *testing.T) {
	s, rec, stop := newDisplayMsgServer(t)
	if got := s.showKeyMessage(DisplayMsgNoInternet, false); got != "shown" {
		t.Fatalf("outcome %q", got)
	}
	// The user pressed another key: the speaker plays something else now.
	s.displayMsg.nowPlayingFn = func() (boxNowPlaying, bool) {
		np := boxNowPlaying{Source: "UPNP", PlayStatus: "PLAY_STATE"}
		np.Item.Location = "http://127.0.0.1:8888/stream/3"
		return np, true
	}
	(*stop)()
	if got := rec.seen(); len(got) != 2 {
		t.Fatalf("the stop cut off the user's new play: %v", got)
	}
	// The slot is free again.
	s.displayMsg.nowPlayingFn = func() (boxNowPlaying, bool) { return boxNowPlaying{Source: "INVALID_SOURCE"}, true }
	if got := s.showKeyMessage(DisplayMsgSpotifyLogin, false); got != "shown" {
		t.Fatalf("after the stop: %q", got)
	}
}

func TestDisplayMessageStationDownBecomesNoInternetWhenOffline(t *testing.T) {
	s, rec, _ := newDisplayMsgServer(t)
	s.onlineFn = func() bool { return false }
	if got := s.showKeyMessage(DisplayMsgStationDown, false); got != "shown" {
		t.Fatalf("outcome %q", got)
	}
	if body := rec.body("SetAVTransportURI"); !strings.Contains(body, "No internet connection") {
		t.Fatalf("offline station failure did not say so: %s", body)
	}
}

func TestDisplayMessageIfOfflineIsQuietWhenOnline(t *testing.T) {
	s, rec, _ := newDisplayMsgServer(t)
	s.onlineFn = func() bool { return true }
	if got := s.showKeyMessage(DisplayMsgIfOffline, false); got != "speaker is online, nothing to say" {
		t.Fatalf("outcome %q", got)
	}
	if len(rec.seen()) != 0 {
		t.Fatal("an online speaker was sent a message")
	}
}

func TestDisplayMessageLanguage(t *testing.T) {
	if got := displayMsgText(DisplayMsgNoInternet, 2); got != "Keine Internetverbindung" {
		t.Errorf("German: %q", got)
	}
	// A language without a table falls back to English (22 = Russian).
	if got := displayMsgText(DisplayMsgNoInternet, 22); got != "No internet connection" {
		t.Errorf("fallback: %q", got)
	}
	for lang, texts := range displayMsgTexts {
		for _, k := range []DisplayMsgKind{DisplayMsgSpotifyLogin, DisplayMsgStationDown, DisplayMsgNoInternet} {
			if texts[k] == "" {
				t.Errorf("sysLanguage %d has no text for %s", lang, k)
			}
		}
	}
}

func TestModelHasDisplay(t *testing.T) {
	cases := map[string]bool{
		"SoundTouch 10": false, "SoundTouch 300": false, "SoundTouch SA-5 amplifier": false,
		"SoundTouch 20": true, "SoundTouch 30": true, "SoundTouch Portable": true, "Wave SoundTouch music system IV": true,
	}
	for model, want := range cases {
		if got, known := modelHasDisplay(model); got != want || !known {
			t.Errorf("%s: (%v, %v), want (%v, true)", model, got, known, want)
		}
	}
	if got, known := modelHasDisplay("Lifestyle 650"); got || known {
		t.Errorf("an unknown model must read as unknown, no display")
	}
}

// The track must be a valid MPEG-1 Layer III stream of silent frames.
func TestSilentMP3(t *testing.T) {
	b := silentMP3(displayMsgSilence)
	if len(b)%96 != 0 || len(b) < 96*int(displayMsgSilence.Seconds()*41) {
		t.Fatalf("unexpected length %d", len(b))
	}
	for i := 0; i < len(b); i += 96 {
		if !bytes.Equal(b[i:i+4], []byte{0xFF, 0xFB, 0x14, 0xC0}) {
			t.Fatalf("frame %d header % x", i/96, b[i:i+4])
		}
		for _, c := range b[i+4 : i+96] {
			if c != 0 {
				t.Fatalf("frame %d carries audio data", i/96)
			}
		}
	}
}

func TestDisplayMessagesSetting(t *testing.T) {
	s, _, _ := newDisplayMsgServer(t)
	do := func(method, body string) map[string]any {
		r := httptest.NewRequest(method, "/api/box/display-messages", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:5000"
		w := httptest.NewRecorder()
		s.handleDisplayMessages(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, body, w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	if got := do(http.MethodGet, ""); got["enabled"] != true || got["hasDisplay"] != true {
		t.Fatalf("default must be on: %v", got)
	}
	do(http.MethodPost, `{"enabled":false}`)
	if got := do(http.MethodGet, ""); got["enabled"] != false {
		t.Fatalf("switch did not stick: %v", got)
	}
	do(http.MethodPost, `{"enabled":true}`)
	if got := do(http.MethodPost, `{"preview":"no-internet"}`); got["outcome"] != "shown" {
		t.Fatalf("preview: %v", got)
	}
	if got := do(http.MethodGet, ""); got["lastShown"] == nil {
		t.Fatalf("the last message is not reported: %v", got)
	}
}
