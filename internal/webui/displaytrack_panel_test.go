package webui

import (
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

	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/upnp"
)

// fakeTrackPanel records what the setting asked of the speaker's panel.
type fakeTrackPanel struct {
	mu        sync.Mutex
	supported bool
	shown     []oled.TrackText
	cleared   []string
}

func (f *fakeTrackPanel) Supported() bool { return f.supported }
func (f *fakeTrackPanel) Show(t oled.TrackText) {
	f.mu.Lock()
	f.shown = append(f.shown, t)
	f.mu.Unlock()
}
func (f *fakeTrackPanel) Clear(reason string) {
	f.mu.Lock()
	f.cleared = append(f.cleared, reason)
	f.mu.Unlock()
}

func (f *fakeTrackPanel) snapshot() ([]oled.TrackText, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]oled.TrackText(nil), f.shown...), append([]string(nil), f.cleared...)
}

// newTrackServer is a Server with the setting on, a fake panel and a box that
// records every SOAP action it is sent.
func newTrackServer(t *testing.T, panel bool) (*Server, *fakeTrackPanel, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var actions []string
	box := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("SOAPAction")
		if i := strings.LastIndex(action, "#"); i >= 0 {
			action = strings.Trim(action[i+1:], `"`)
		}
		mu.Lock()
		actions = append(actions, action)
		mu.Unlock()
	}))
	t.Cleanup(box.Close)
	fp := &fakeTrackPanel{supported: panel}
	s := &Server{
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		queue:            newPlayQueue(),
		renderer:         &upnp.Renderer{ControlURL: box.URL, Client: box.Client()},
		displayTrackPath: filepath.Join(t.TempDir(), "display-track-on-box"),
		trackPanel:       fp,
	}
	if err := writeFlagFile(s.displayTrackPath, "1"); err != nil {
		t.Fatal(err)
	}
	s.lastPlay = &lastPlayInfo{boxURL: "http://192.0.2.1:8888/stream/1", title: "Radio Bob", mime: "audio/mpeg"}
	return s, fp, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), actions...)
	}
}

// The point of the change: on a panel STR draws on, a song change reaches the
// panel and the speaker is sent nothing, so it never re-buffers.
func TestStreamTitleGoesToThePanelWithoutTouchingTheStream(t *testing.T) {
	s, fp, actions := newTrackServer(t, true)
	s.HandleStreamTitle("Queen - Under Pressure")
	s.HandleStreamTitle("Queen - Bohemian Rhapsody")
	shown, _ := fp.snapshot()
	want := []oled.TrackText{
		{Station: "Radio Bob", Artist: "Queen", Title: "Under Pressure"},
		{Station: "Radio Bob", Artist: "Queen", Title: "Bohemian Rhapsody"},
	}
	if len(shown) != 2 || shown[0] != want[0] || shown[1] != want[1] {
		t.Fatalf("panel got %+v, want %+v (no rate limit: a change costs no audio)", shown, want)
	}
	if a := actions(); len(a) != 0 {
		t.Fatalf("the speaker was sent %v; the panel path must never re-send the stream", a)
	}
}

// Without a panel STR can draw on, the old path stays exactly as it was.
func TestStreamTitleWithoutPanelKeepsTheRePush(t *testing.T) {
	s, fp, actions := newTrackServer(t, false)
	s.HandleStreamTitle("Queen - Under Pressure")
	if shown, _ := fp.snapshot(); len(shown) != 0 {
		t.Fatalf("panel used on a speaker without one: %+v", shown)
	}
	if a := actions(); len(a) == 0 || a[0] != "SetAVTransportURI" {
		t.Fatalf("speaker got %v, want the metadata re-push", a)
	}
}

func TestStreamTitleSettingOffLeavesThePanelAlone(t *testing.T) {
	s, fp, _ := newTrackServer(t, true)
	if err := writeFlagFile(s.displayTrackPath, "0"); err != nil {
		t.Fatal(err)
	}
	s.HandleStreamTitle("Queen - Under Pressure")
	if shown, _ := fp.snapshot(); len(shown) != 0 {
		t.Fatalf("setting off, panel got %+v", shown)
	}
}

func TestPanelTrackTextModes(t *testing.T) {
	cases := []struct {
		mode, in string
		want     oled.TrackText
	}{
		{"both", "Queen - Under Pressure", oled.TrackText{Station: "Radio Bob", Artist: "Queen", Title: "Under Pressure"}},
		{"title", "Queen - Under Pressure", oled.TrackText{Station: "Radio Bob", Title: "Under Pressure"}},
		{"artist", "Queen - Under Pressure", oled.TrackText{Station: "Radio Bob", Title: "Queen"}},
		// no separator: the whole text, whatever the mode
		{"both", "Morning Show", oled.TrackText{Station: "Radio Bob", Title: "Morning Show"}},
		{"artist", "Morning Show", oled.TrackText{Station: "Radio Bob", Title: "Morning Show"}},
		{"title", "Morning Show", oled.TrackText{Station: "Radio Bob", Title: "Morning Show"}},
	}
	for _, c := range cases {
		s, _, _ := newTrackServer(t, true)
		if err := writeFlagFile(modePathFor(s.displayTrackPath), c.mode); err != nil {
			t.Fatal(err)
		}
		if got := s.panelTrackText(c.in); got != c.want {
			t.Errorf("mode %s, %q: got %+v, want %+v", c.mode, c.in, got, c.want)
		}
	}
}

// A new stream takes the old track off the panel; the same stream pushed
// again (a recovery) keeps it, because its title is not reported again.
func TestNewStreamClearsThePanel(t *testing.T) {
	s, fp, _ := newTrackServer(t, true)
	s.setLastPlay("http://192.0.2.1:8888/stream/1", "Radio Bob", "", "audio/mpeg")
	if _, cleared := fp.snapshot(); len(cleared) != 0 {
		t.Fatalf("same stream cleared the panel: %v", cleared)
	}
	s.setLastPlay("http://192.0.2.1:8888/stream/2", "Other", "", "audio/mpeg")
	if _, cleared := fp.snapshot(); len(cleared) != 1 || cleared[0] != "new stream" {
		t.Fatalf("cleared %v, want one new-stream clear", cleared)
	}
	s.HandleStreamEnded()
	if _, cleared := fp.snapshot(); len(cleared) != 2 || cleared[1] != "stream ended" {
		t.Fatalf("cleared %v, want the stream-ended clear", cleared)
	}
}

func TestClearIsANoOpWithoutPanel(t *testing.T) {
	s, fp, _ := newTrackServer(t, false)
	s.HandleStreamEnded()
	s.ClearTrackPanel("source AUX")
	if _, cleared := fp.snapshot(); len(cleared) != 0 {
		t.Fatalf("cleared %v on a speaker without a panel", cleared)
	}
}

func TestDisplayTrackEndpointOnPanel(t *testing.T) {
	s, fp, actions := newTrackServer(t, true)
	s.playStateDetailFn = func() (bool, bool, bool) { return false, true, false }
	s.lastICYTitle = "Queen - Under Pressure"

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/box/display-track", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	s.handleDisplayTrack(rr, req)
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["onPanel"] != true {
		t.Fatalf("GET = %v, want onPanel true", got)
	}

	post := func(body string) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/box/display-track", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		s.handleDisplayTrack(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("POST %s: %d %s", body, rr.Code, rr.Body.String())
		}
	}
	// switching it on shows the current song at once
	post(`{"enabled":true,"mode":"title"}`)
	waitUntil(t, func() bool { shown, _ := fp.snapshot(); return len(shown) == 1 })
	if shown, _ := fp.snapshot(); shown[0].Title != "Under Pressure" || shown[0].Artist != "" {
		t.Fatalf("shown %+v", shown[0])
	}
	// switching it off clears the panel, and neither touched the stream
	post(`{"enabled":false}`)
	waitUntil(t, func() bool { _, cleared := fp.snapshot(); return len(cleared) == 1 })
	if _, cleared := fp.snapshot(); cleared[0] != "setting off" {
		t.Fatalf("cleared %v", cleared)
	}
	if a := actions(); len(a) != 0 {
		t.Fatalf("the speaker was sent %v", a)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
