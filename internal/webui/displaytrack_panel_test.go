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
		// 1LIVE, 2026-10-20: the slash in the artist name is not the separator
		{"both", "Axwell / Ingrosso - More Than You Know", oled.TrackText{Station: "Radio Bob", Artist: "Axwell / Ingrosso", Title: "More Than You Know"}},
		{"both", `Axwell /\ Ingrosso - More Than You Know`, oled.TrackText{Station: "Radio Bob", Artist: `Axwell /\ Ingrosso`, Title: "More Than You Know"}},
	}
	for _, c := range cases {
		s, _, _ := newTrackServer(t, true)
		if err := writeFlagFile(modePathFor(s.displayTrackPath), c.mode); err != nil {
			t.Fatal(err)
		}
		s.panelRadioTitle(c.in)
		if got, _ := s.panelText(); got != c.want {
			t.Errorf("mode %s, %q: got %+v, want %+v", c.mode, c.in, got, c.want)
		}
	}
}

// A placeholder StreamTitle is no song (#1305): the station name fills the big
// title area instead, a song before it is cleared, and neither Recently played
// nor the remembered live title keep it.
func TestPlaceholderStreamTitleShowsTheStationName(t *testing.T) {
	for _, in := range []string{"Now playing info goes here", "Unknown - Unknown", " - ", "radio bob"} {
		s, fp, actions := newTrackServer(t, true)
		s.HandleStreamTitle("Queen - Under Pressure")
		s.HandleStreamTitle(in)
		shown, _ := fp.snapshot()
		if want := (oled.TrackText{Title: "Radio Bob"}); len(shown) != 2 || shown[1] != want {
			t.Errorf("%q: panel got %+v, want the station name alone last", in, shown)
		}
		s.lastPlayMu.Lock()
		last := s.lastICYTitle
		s.lastPlayMu.Unlock()
		if last != "" {
			t.Errorf("%q: lastICYTitle = %q, want it forgotten", in, last)
		}
		if a := actions(); len(a) != 0 {
			t.Errorf("%q: the speaker was sent %v", in, a)
		}
	}
}

// A new stream replaces the old track with its own name until its first
// song; the same stream pushed again (a recovery) keeps the track, because
// its title is not reported again.
func TestNewStreamShowsItsNameUntilTheFirstSong(t *testing.T) {
	s, fp, _ := newTrackServer(t, true)
	s.HandleStreamTitle("Queen - Under Pressure")
	s.setLastPlay("http://192.0.2.1:8888/stream/1", "Radio Bob", "", "audio/mpeg")
	if shown, _ := fp.snapshot(); len(shown) != 1 {
		t.Fatalf("same stream redrew the panel: %+v", shown)
	}
	s.setLastPlay("http://192.0.2.1:8888/stream/2", "Other", "", "audio/mpeg")
	shown, cleared := fp.snapshot()
	if len(cleared) != 0 || len(shown) != 2 || shown[1] != (oled.TrackText{Title: "Other"}) {
		t.Fatalf("new stream: shown %+v cleared %v, want the station name alone", shown, cleared)
	}
	s.HandleStreamEnded()
	if _, cleared := fp.snapshot(); len(cleared) != 1 || cleared[0] != "stream ended" {
		t.Fatalf("cleared %v, want the stream-ended clear", cleared)
	}
	// ended stays off, even when a setting redraws
	s.refreshTrackPanel()
	if shown, _ := fp.snapshot(); len(shown) != 2 {
		t.Fatalf("an ended stream came back: %+v", shown)
	}
	// until its title arrives again (the speaker reconnected)
	s.HandleStreamTitle("Queen - Under Pressure")
	if shown, _ := fp.snapshot(); len(shown) != 3 || shown[2].Title != "Under Pressure" {
		t.Fatalf("reconnect: %+v", shown)
	}
}

// The four combinations of the two settings.
func TestPanelTrackAndClockCombinations(t *testing.T) {
	cases := []struct {
		track, clock bool
		want         oled.TrackText
		show         bool
	}{
		{true, false, oled.TrackText{Station: "Radio Bob", Artist: "Queen", Title: "Under Pressure"}, true},
		{true, true, oled.TrackText{Station: "Radio Bob", Artist: "Queen", Title: "Under Pressure", Clock: true, Hour12: true}, true},
		{false, true, oled.TrackText{Station: "Radio Bob", Clock: true, Hour12: true}, true},
		{false, false, oled.TrackText{}, false},
	}
	for _, c := range cases {
		s, _, _ := newTrackServer(t, true)
		s.hour12Read = func() (bool, bool) { return true, true }
		setFlag(t, s.displayTrackPath, c.track)
		setFlag(t, clockPathFor(s.displayTrackPath), c.clock)
		s.panelRadioTitle("Queen - Under Pressure")
		got, ok := s.panelText()
		if ok != c.show || got != c.want {
			t.Errorf("track=%v clock=%v: got %+v %v, want %+v %v", c.track, c.clock, got, ok, c.want, c.show)
		}
	}
}

func setFlag(t *testing.T, path string, on bool) {
	t.Helper()
	v := "0"
	if on {
		v = "1"
	}
	if err := writeFlagFile(path, v); err != nil {
		t.Fatal(err)
	}
}

// Spotify: its songs come from the engine, a radio title arriving late from
// the stream before is dropped, and the radio proxy ending behind it does not
// take Spotify's screen away.
func TestPanelSpotify(t *testing.T) {
	s, fp, _ := newTrackServer(t, true)
	s.HandleStreamTitle("Queen - Under Pressure")
	s.PanelSpotifyTrack("Flowers", "Miley Cyrus") // engine song while radio plays: kept, not shown
	if shown, _ := fp.snapshot(); len(shown) != 1 {
		t.Fatalf("Spotify song shown over the radio: %+v", shown)
	}
	s.PanelSpotifyActive()
	want := oled.TrackText{Station: "Spotify", Artist: "Miley Cyrus", Title: "Flowers"}
	if shown, _ := fp.snapshot(); len(shown) != 2 || shown[1] != want {
		t.Fatalf("Spotify activation: %+v", shown)
	}
	s.HandleStreamTitle("Queen - Bohemian Rhapsody")
	s.HandleStreamEnded()
	if shown, cleared := fp.snapshot(); len(shown) != 2 || len(cleared) != 0 {
		t.Fatalf("radio leftovers reached Spotify's screen: shown %+v cleared %v", shown, cleared)
	}
	s.PanelSpotifyTrack("Espresso", "Sabrina Carpenter")
	if shown, _ := fp.snapshot(); len(shown) != 3 || shown[2].Title != "Espresso" {
		t.Fatalf("next Spotify song: %+v", shown)
	}
	// a Spotify preset from STR names the preset until the first song
	s.setLastPlay("http://192.0.2.1:8888/spotify/slot/5.ogg", "Test Playlist", "", "audio/ogg")
	if shown, _ := fp.snapshot(); shown[len(shown)-1] != (oled.TrackText{Title: "Test Playlist"}) {
		t.Fatalf("Spotify preset: %+v", shown[len(shown)-1])
	}
}

func TestPanelLibrary(t *testing.T) {
	s, fp, _ := newTrackServer(t, true)
	s.panelLibraryTrack("Best of 80s", "a-ha", "Take On Me")
	want := oled.TrackText{Station: "Best of 80s", Artist: "a-ha", Title: "Take On Me"}
	if shown, _ := fp.snapshot(); len(shown) != 1 || shown[0] != want {
		t.Fatalf("library: %+v", shown)
	}
	s.HandleStreamEnded()
	if _, cleared := fp.snapshot(); len(cleared) != 0 {
		t.Fatalf("radio end cleared the library screen: %v", cleared)
	}
}

func TestParseClockHour12(t *testing.T) {
	for _, c := range []struct {
		body    string
		h12, ok bool
	}{
		{`<?xml version="1.0" encoding="UTF-8" ?><clockDisplay><clockConfig timezoneInfo="Europe/Berlin" userEnable="true" timeFormat="TIME_FORMAT_24HOUR_ID" /></clockDisplay>`, false, true},
		{`<clockDisplay><clockConfig timeFormat="TIME_FORMAT_12HOUR_ID" /></clockDisplay>`, true, true},
		{`<clockDisplay/>`, false, false},
		{`garbage`, false, false},
	} {
		if h12, ok := parseClockHour12([]byte(c.body)); h12 != c.h12 || ok != c.ok {
			t.Errorf("%s: %v %v", c.body, h12, ok)
		}
	}
}

// A failed format read is retried after a pause, not on every draw.
func TestSpeakerHour12ReadsOnce(t *testing.T) {
	s, _, _ := newTrackServer(t, true)
	reads := 0
	s.hour12Read = func() (bool, bool) { reads++; return false, false }
	s.speakerHour12()
	s.speakerHour12()
	if reads != 1 {
		t.Fatalf("failed read retried at once: %d reads", reads)
	}
	s.hour12Read = func() (bool, bool) { reads++; return true, true }
	s.forgetSpeakerHour12()
	first, second := s.speakerHour12(), s.speakerHour12()
	if !first || !second || reads != 2 {
		t.Fatalf("reads %d", reads)
	}
}

func TestDisplayClockEndpoint(t *testing.T) {
	s, fp, _ := newTrackServer(t, true)
	s.hour12Read = func() (bool, bool) { return false, true }
	s.playStateDetailFn = func() (bool, bool, bool) { return false, true, false }
	s.HandleStreamTitle("Queen - Under Pressure")
	call := func(method, body string) map[string]any {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/box/display-clock", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		s.handleDisplayClock(rr, req)
		var got map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &got)
		return got
	}
	if got := call(http.MethodGet, ""); got["supported"] != true || got["enabled"] != false {
		t.Fatalf("GET %v", got)
	}
	call(http.MethodPost, `{"enabled":true}`)
	waitUntil(t, func() bool { shown, _ := fp.snapshot(); return len(shown) == 2 })
	if shown, _ := fp.snapshot(); !shown[1].Clock || shown[1].Title != "Under Pressure" {
		t.Fatalf("clock on: %+v", shown[1])
	}
	// the track off with the clock on keeps the panel, now with the clock alone
	setFlag(t, s.displayTrackPath, false)
	s.applyPanelSetting()
	waitUntil(t, func() bool { shown, _ := fp.snapshot(); return len(shown) == 3 })
	if shown, _ := fp.snapshot(); shown[2] != (oled.TrackText{Station: "Radio Bob", Clock: true}) {
		t.Fatalf("clock alone: %+v", shown[2])
	}
	// both off hands the panel back
	call(http.MethodPost, `{"enabled":false}`)
	waitUntil(t, func() bool { _, cleared := fp.snapshot(); return len(cleared) == 1 })
	if got := call(http.MethodGet, ""); got["enabled"] != false {
		t.Fatalf("GET after off %v", got)
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
	// the song is known while the setting is off
	setFlag(t, s.displayTrackPath, false)
	s.HandleStreamTitle("Queen - Under Pressure")

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
	// (the title that arrived with the setting off already let it go once)
	waitUntil(t, func() bool { _, cleared := fp.snapshot(); return len(cleared) == 2 })
	if _, cleared := fp.snapshot(); cleared[1] != "setting off" {
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
