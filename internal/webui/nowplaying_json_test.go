package webui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/presets"
)

// pollNowPlaying runs one GET /api/nowplaying against the handler.
func pollNowPlaying(s *Server) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.handleNowPlaying(w, httptest.NewRequest(http.MethodGet, "/api/nowplaying", nil))
	return w
}

// decodeNowPlaying decodes an answer into a generic map, so a test sees
// exactly which keys were sent.
func decodeNowPlaying(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON", ct)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("answer is not JSON: %v (%q)", err, w.Body.String())
	}
	return out
}

// nowPlayingTestStore is a preset store with a proxied station on key 2 and a
// plain stream URL on key 5.
func nowPlayingTestStore(t *testing.T) *presets.Store {
	t.Helper()
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []presets.Preset{
		{Slot: 2, Name: "Test Radio", Type: "radio", StreamURL: "https://radio.example.com/live.mp3"},
		{Slot: 5, Name: "Other Radio", Type: "radio", StreamURL: "https://other.example.com/stream"},
	} {
		if err := store.SetSlot(p); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

const cannedNowPlaying = `<?xml version="1.0" encoding="UTF-8" ?>
<nowPlaying deviceID="device-id-here" source="UPNP" sourceAccount="UPnPUserName">
<ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/2" sourceAccount="UPnPUserName" isPresetable="true">
<itemName>Test Radio</itemName><containerArt>http://192.0.2.1/container.png</containerArt></ContentItem>
<track>A Song &amp; More</track><artist>An Artist</artist><album>An Album</album>
<stationName>Test Radio</stationName>
<art artImageStatus="IMAGE_PRESENT">http://192.0.2.1/art.png</art>
<time total="240">31</time>
<playStatus>PLAY_STATE</playStatus>
</nowPlaying>`

// The speaker's now_playing XML comes back as JSON with every field it
// carries, the key that plays and the bus's mute flag.
func TestNowPlayingJSONFromCannedXML(t *testing.T) {
	s := quietServer("192.0.2.1")
	s.presets = nowPlayingTestStore(t)
	s.statusBody, s.statusCode, s.statusAt = []byte(cannedNowPlaying), http.StatusOK, time.Now()
	s.NoteBoxMute(true)

	w := pollNowPlaying(s)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	out := decodeNowPlaying(t, w)
	want := map[string]any{
		"source":      "UPNP",
		"playStatus":  "PLAY_STATE",
		"title":       "A Song & More",
		"artist":      "An Artist",
		"album":       "An Album",
		"station":     "Test Radio",
		"itemName":    "Test Radio",
		"art":         "http://192.0.2.1/art.png",
		"location":    "http://127.0.0.1:8888/stream/2",
		"positionSec": float64(31),
		"durationSec": float64(240),
		"presetSlot":  float64(2),
		"muted":       true,
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s = %#v, want %#v", k, out[k], v)
		}
	}
	for _, k := range []string{"stale", "ageSec"} {
		if _, ok := out[k]; ok {
			t.Errorf("%s sent on a fresh answer", k)
		}
	}
}

// What the speaker does not report, and what STR cannot tell, is left out
// instead of sent empty or as a guess.
func TestNowPlayingJSONOmitsUnknownFields(t *testing.T) {
	s := quietServer("192.0.2.1")
	s.statusBody = []byte(`<nowPlaying deviceID="device-id-here" source="STANDBY"><ContentItem source="STANDBY" isPresetable="false" /></nowPlaying>`)
	s.statusCode, s.statusAt = http.StatusOK, time.Now()

	out := decodeNowPlaying(t, pollNowPlaying(s))
	if len(out) != 1 || out["source"] != "STANDBY" {
		t.Fatalf("answer = %v, want only source STANDBY", out)
	}
}

// A station without a live title: the title falls back to the item name.
func TestNowPlayingJSONTitleFallsBackToItemName(t *testing.T) {
	got := nowPlayingFrom(boxapi.ParseNowPlaying([]byte(
		`<nowPlaying source="LOCAL_INTERNET_RADIO"><ContentItem source="LOCAL_INTERNET_RADIO" location="/station?data=x"><itemName>Station Name</itemName></ContentItem><playStatus>PLAY_STATE</playStatus></nowPlaying>`)))
	if got.Title != "Station Name" || got.ItemName != "Station Name" {
		t.Errorf("title = %q, itemName = %q, want the item name for both", got.Title, got.ItemName)
	}
	if got.PositionSec != nil || got.DurationSec != nil {
		t.Error("position/duration set for a document without <time>")
	}
}

// /api/nowplaying is built from the /api/status micro-cache: while it is warm
// neither endpoint reads the speaker, and both describe the same body.
func TestNowPlayingJSONReusesStatusCache(t *testing.T) {
	reads := 0
	s := quietServer("192.0.2.1")
	s.boxNowReadFn = func(context.Context) (*boxapi.NowPlaying, error) {
		reads++
		return boxapi.ParseNowPlaying([]byte(cannedNowPlaying)), nil
	}
	s.statusBody, s.statusCode, s.statusAt = []byte(cannedNowPlaying), http.StatusOK, time.Now()

	np := pollNowPlaying(s)
	st := pollStatus(s)
	if reads != 0 {
		t.Fatalf("warm cache: %d speaker reads, want none", reads)
	}
	if st.Body.String() != cannedNowPlaying {
		t.Error("/api/status changed its XML body")
	}
	if out := decodeNowPlaying(t, np); out["title"] != "A Song & More" {
		t.Errorf("title = %v, want the cached body's track", out["title"])
	}

	// Cold cache: one read of the speaker for one request, like /api/status.
	s.statusAt = time.Now().Add(-time.Minute)
	if w := pollNowPlaying(s); w.Code != http.StatusOK {
		t.Fatalf("cold cache: status %d", w.Code)
	}
	if reads != 1 {
		t.Errorf("cold cache: %d speaker reads, want exactly one", reads)
	}
}

// No speaker configured: 503, as /api/status, but as JSON.
func TestNowPlayingJSONNoBoxHost(t *testing.T) {
	s := quietServer("")
	w := pollNowPlaying(s)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if out := decodeNowPlaying(t, w); out["error"] == nil {
		t.Errorf("answer %v carries no error", out)
	}
}

// A speaker that does not answer, with nothing cached: 502 with a JSON error.
func TestNowPlayingJSONSpeakerSilent(t *testing.T) {
	s := quietServer("192.0.2.1")
	s.boxNowReadFn = func(context.Context) (*boxapi.NowPlaying, error) {
		return nil, errors.New("connection refused")
	}
	w := pollNowPlaying(s)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	out := decodeNowPlaying(t, w)
	if out["error"] != "the speaker did not answer" || out["detail"] != "connection refused" {
		t.Errorf("answer = %v", out)
	}
}

// A speaker that stopped answering: the last answer is served, marked with its
// age and, past statusStaleAfter, as stale.
func TestNowPlayingJSONStaleFallback(t *testing.T) {
	s := quietServer("192.0.2.1")
	s.boxNowReadFn = func(context.Context) (*boxapi.NowPlaying, error) {
		return nil, errors.New("connection refused")
	}
	s.statusBody, s.statusCode = []byte(cannedNowPlaying), http.StatusOK
	s.statusAt = time.Now().Add(-45 * time.Second)

	w := pollNowPlaying(s)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	out := decodeNowPlaying(t, w)
	if out["stale"] != true {
		t.Errorf("stale = %v, want true", out["stale"])
	}
	if age, _ := out["ageSec"].(float64); age < 44 {
		t.Errorf("ageSec = %v, want >= 44", out["ageSec"])
	}
}

// A firmware error reply is not described as a playing state.
func TestNowPlayingJSONSpeakerErrorReply(t *testing.T) {
	s := quietServer("192.0.2.1")
	s.boxNowReadFn = func(context.Context) (*boxapi.NowPlaying, error) {
		return nil, &boxapi.BoxError{Status: http.StatusInternalServerError, Body: "<errors/>"}
	}
	w := pollNowPlaying(s)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	if out := decodeNowPlaying(t, w); out["speakerStatus"] != float64(500) {
		t.Errorf("answer = %v, want speakerStatus 500", out)
	}
}

// The location rules that name the key that plays.
func TestPresetSlotForLocation(t *testing.T) {
	list := []presets.Preset{
		{Slot: 2, Type: "radio", StreamURL: "https://radio.example.com/live.mp3"},
		{Slot: 4, Type: "radio", StreamURL: "https://four.example.com/live"},
		{Slot: 5, Type: "radio", StreamURL: "https://other.example.com/stream"},
		{Slot: 6, Type: presets.TypeNative, Native: &presets.NativeItem{Source: "PANDORA", Location: "126740707481236361"}},
	}
	station := func(streamURL string) string {
		payload, _ := json.Marshal(map[string]string{"streamUrl": streamURL, "name": "Test"})
		return "/station?data=" + base64.RawURLEncoding.EncodeToString(payload)
	}
	for _, tc := range []struct {
		name, loc string
		want      int
	}{
		{"per-key proxy", boxurl.StreamSlot(2), 2},
		{"per-key proxy of an empty key", boxurl.StreamSlot(3), 0},
		{"plain stream URL a key holds", "https://other.example.com/stream", 5},
		{"Pandora station a key holds", "126740707481236361", 6},
		{"native station on a key's proxy", station(boxurl.StreamSlot(4)), 4},
		{"native station on the raw proxy", station(boxurl.RawStream("https://other.example.com/stream")), 5},
		{"stream no key holds", "https://unknown.example.com/x", 0},
		{"empty", "", 0},
	} {
		if got := presetSlotForLocation(tc.loc, list); got != tc.want {
			t.Errorf("%s: key %d, want %d", tc.name, got, tc.want)
		}
	}
}

// A stopped speaker still reports the station, but no key is playing.
func TestPlayingPresetSlotOnlyWhileAudible(t *testing.T) {
	s := quietServer("192.0.2.1")
	s.presets = nowPlayingTestStore(t)
	loc := boxurl.StreamSlot(2)
	for status, want := range map[string]int{
		"PLAY_STATE": 2, "BUFFERING_STATE": 2, "PAUSE_STATE": 2, "STOP_STATE": 0, "": 0,
	} {
		if got := s.playingPresetSlot(status, loc); got != want {
			t.Errorf("%q: key %d, want %d", status, got, want)
		}
	}
}

// /api/agent/version advertises the endpoint.
func TestAgentVersionAdvertisesNowPlayingJSON(t *testing.T) {
	s := quietServer("127.0.0.1")
	w := httptest.NewRecorder()
	s.handleAgentVersion(w, httptest.NewRequest(http.MethodGet, "/api/agent/version", nil))
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["nowPlayingJSON"] != "true" {
		t.Errorf("nowPlayingJSON = %q, want \"true\"", out["nowPlayingJSON"])
	}
}
