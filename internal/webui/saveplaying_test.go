package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
)

// A hold on a key of the phone page saves what the speaker plays (#1407),
// in the desktop app's order. Port 1 refuses at once, so the save's web-page
// probe lets these streams through without touching the network.

const savePlayingStation = "http://127.0.0.1:1/live/radio-one.mp3"

func nowPlayingDoc(source, location, name, art string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="device-id-here" source="` + source +
		`"><ContentItem source="` + source + `" location="` + html.EscapeString(location) +
		`" isPresetable="true"><itemName>` + html.EscapeString(name) + `</itemName></ContentItem>` +
		`<track>` + html.EscapeString(name) + `</track><art artImageStatus="IMAGE_PRESENT">` + html.EscapeString(art) +
		`</art><playStatus>PLAY_STATE</playStatus></nowPlaying>`)
}

func newSavePlayingServer(t *testing.T, doc []byte) *Server {
	t.Helper()
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		presets: store,
		queue:   newPlayQueue(),
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		boxNowReadFn: func(context.Context) (*boxapi.NowPlaying, error) {
			if doc == nil {
				return nil, errors.New("connection refused")
			}
			return boxapi.ParseNowPlaying(doc), nil
		},
	}
}

func postSavePlaying(t *testing.T, s *Server, slot int) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]int{"slot": slot})
	req := httptest.NewRequest(http.MethodPost, "/api/box/save-playing", bytes.NewReader(body))
	req.RemoteAddr = "192.0.2.10:51234"
	rec := httptest.NewRecorder()
	s.handleSavePlaying(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// The reported case: a station started from the desktop app plays through the
// ad-hoc proxy. The hold stores the station itself, never the proxy, under
// the speaker's name for it.
func TestSavePlayingStoresAStationStartedElsewhere(t *testing.T) {
	s := newSavePlayingServer(t, nowPlayingDoc("UPNP", boxurl.RawStream(savePlayingStation), "Radio One", ""))
	rec, out := postSavePlaying(t, s, 3)
	if rec.Code != http.StatusOK || out["kind"] != "stream" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	p, ok := s.presets.Get(3)
	if !ok || p.Type != "radio" || p.StreamURL != savePlayingStation || p.Name != "Radio One" {
		t.Fatalf("stored %+v", p)
	}
}

// The agent's own record of the play names it the way the app started it.
func TestSavePlayingPrefersTheAgentsRecordOfThePlay(t *testing.T) {
	loc := boxurl.RawStream(savePlayingStation)
	s := newSavePlayingServer(t, nowPlayingDoc("UPNP", loc, "radio-one.mp3", ""))
	s.play.last = &lastPlayInfo{boxURL: loc, title: "Radio One", art: "https://img.example/r1.png", ts: time.Now()}
	if rec, _ := postSavePlaying(t, s, 2); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if p, _ := s.presets.Get(2); p.Name != "Radio One" || p.Art != "https://img.example/r1.png" {
		t.Fatalf("stored %+v", p)
	}
}

// A native station descriptor (a station the speaker plays itself) is
// unwrapped to its stream.
func TestSavePlayingUnwrapsANativeStationDescriptor(t *testing.T) {
	loc := OrionStationLocation(boxurl.RawStream(savePlayingStation), "Radio One", "")
	s := newSavePlayingServer(t, nowPlayingDoc("LOCAL_INTERNET_RADIO", loc, "", ""))
	if rec, _ := postSavePlaying(t, s, 5); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if p, _ := s.presets.Get(5); p.StreamURL != savePlayingStation || p.Name != "Radio One" {
		t.Fatalf("stored %+v", p)
	}
}

// A hardware key plays its own proxy stream: the hold copies that key, the
// speaker's now-playing only knows the proxy.
func TestSavePlayingCopiesTheKeyThatPlays(t *testing.T) {
	for name, loc := range map[string]string{
		"proxy":   boxurl.StreamSlot(1),
		"station": OrionStationLocation(boxurl.StreamSlot(1), "Keeper", ""),
	} {
		t.Run(name, func(t *testing.T) {
			s := newSavePlayingServer(t, nowPlayingDoc("UPNP", loc, "stale name", ""))
			keeper := presets.Preset{Slot: 1, Name: "Keeper", Type: "radio", StreamURL: savePlayingStation, Codec: "MP3", Art: "https://img.example/k.png"}
			if err := s.presets.SetSlot(keeper); err != nil {
				t.Fatal(err)
			}
			// The station is on key 1 already, so the one-station-one-key rule
			// answers with the move offer's 409.
			rec, out := postSavePlaying(t, s, 4)
			if rec.Code != http.StatusConflict || out["code"] != "already-on-slot" || out["slot"] != float64(1) || out["name"] != "Keeper" {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if _, ok := s.presets.Get(4); ok {
				t.Fatal("nothing may be stored on a refusal")
			}
			// The key that plays held: nothing to do.
			rec, out = postSavePlaying(t, s, 1)
			if rec.Code != http.StatusOK || out["unchanged"] != true {
				t.Fatalf("same key: status %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSavePlayingCopyOfAnEmptyKeyIsRefused(t *testing.T) {
	s := newSavePlayingServer(t, nowPlayingDoc("UPNP", boxurl.StreamSlot(2), "x", ""))
	rec, out := postSavePlaying(t, s, 4)
	if rec.Code != http.StatusUnprocessableEntity || out["code"] != "source-preset-empty" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

// A music-library song the agent pushed saves as the library key the desktop
// app stores: type radio, the file's URL, the server's name.
func TestSavePlayingStoresTheLibrarySong(t *testing.T) {
	const file = "http://127.0.0.1:1/m/NDLNA/Purpose%20For%20Pain.m4a"
	s := newSavePlayingServer(t, nowPlayingDoc("UPNP", file, "Purpose For Pain", ""))
	s.play.last = &lastPlayInfo{boxURL: file, title: "Purpose For Pain", mime: "audio/mp4", ts: time.Now()}
	rec, out := postSavePlaying(t, s, 4)
	if rec.Code != http.StatusOK || out["kind"] != "library" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	p, _ := s.presets.Get(4)
	if p.Type != "radio" || p.StreamURL != file || p.Name != "Purpose For Pain" || p.Source != holdLibraryFallbackSource {
		t.Fatalf("stored %+v", p)
	}
}

// A song on a registered media server the agent no longer remembers pushing
// (the desktop app played something else in between) is still a library key.
func TestSavePlayingStoresASongOnAKnownServer(t *testing.T) {
	const file = "http://127.0.0.1:1/m/NDLNA/22.flac"
	s := newSavePlayingServer(t, nowPlayingDoc("UPNP", file, "Twenty Two", ""))
	reg, err := mediaservers.Load(filepath.Join(t.TempDir(), "mediaservers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(mediaservers.Server{ID: "uuid-nas", Name: "Backupserver", Location: "http://127.0.0.1:50001/desc/device.xml"}); err != nil {
		t.Fatal(err)
	}
	s.mediaServers = reg
	if rec, _ := postSavePlaying(t, s, 6); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if p, _ := s.presets.Get(6); p.StreamURL != file || p.Name != "Twenty Two" || p.Source != "Backupserver" {
		t.Fatalf("stored %+v", p)
	}
}

// A folder playing as the queue saves the folder, not the current song.
func TestSavePlayingStoresThePlayingFolder(t *testing.T) {
	s := newSavePlayingServer(t, nowPlayingDoc("UPNP", queueSaveTracks[0].URL, "Come Together", ""))
	s.queue.load(queueSaveTracks, 0, false, repeatOff)
	s.queueFolder = recentCardCtx{key: "queue:uuid:abc:64$1", name: "Abbey Road", source: "Living Room NAS"}
	rec, out := postSavePlaying(t, s, 2)
	if rec.Code != http.StatusOK || out["kind"] != "queue" || out["name"] != "Abbey Road" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if p, _ := s.presets.Get(2); p.Type != "queue" || len(p.Items) != 2 {
		t.Fatalf("stored %+v", p)
	}
}

// What the page saves itself, or what is no station at all, is refused with
// a code, and nothing is stored.
func TestSavePlayingRefusesWhatItCannotKeep(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  []byte
		code int
		want string
	}{
		"spotify engine": {nowPlayingDoc("UPNP", "http://127.0.0.1:8888/spotify/stream.ogg", "Song", ""), 422, "spotify-playing"},
		"spotify native": {nowPlayingDoc("SPOTIFY", "/playback/container/c3BvdGlmeTpwbGF5bGlzdDp4", "Song", ""), 422, "spotify-playing"},
		"pandora":        {nowPlayingDoc("PANDORA", "4071226281950183516", "Country", ""), 422, "native-playing"},
		"standby":        {[]byte(`<nowPlaying source="STANDBY"><ContentItem source="STANDBY" isPresetable="false"/></nowPlaying>`), 422, "nothing-playing"},
		"bluetooth":      {nowPlayingDoc("BLUETOOTH", "", "Phone", ""), 422, "nothing-playing"},
		"own raw proxy":  {nowPlayingDoc("UPNP", "http://127.0.0.1:8888/stream/raw?u=%%%", "x", ""), 422, "nothing-saveable"},
		"unreadable":     {nil, 502, "nowplaying-unreadable"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSavePlayingServer(t, tc.doc)
			rec, out := postSavePlaying(t, s, 1)
			if rec.Code != tc.code || out["code"] != tc.want {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if _, ok := s.presets.Get(1); ok {
				t.Fatal("nothing may be stored")
			}
		})
	}
}

func TestParseBoxNowItem(t *testing.T) {
	got, ok := parseBoxNowItem(nowPlayingDoc("upnp", "http://a.example/x?y=1&z=2", "Name & Co", "http://a.example/art.png"))
	if !ok || got.Source != "UPNP" || got.Location != "http://a.example/x?y=1&z=2" || got.Name != "Name & Co" || got.Art != "http://a.example/art.png" {
		t.Fatalf("parsed %+v", got)
	}
	if _, ok := parseBoxNowItem([]byte("not xml <")); ok {
		t.Fatal("garbage parsed")
	}
}

// A native hold that is refused leaves a line in the log, with the reason and
// the source, and never the account in clear (#1407: a Pandora hold from the
// phone page failed without a trace).
func TestNativePresetSaveRefusalsAreLogged(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  []byte
		code int
		want []string
	}{
		"unreadable": {nil, http.StatusBadGateway, []string{"native-nowplaying-unreadable", "connection refused"}},
		"radio": {[]byte(`<nowPlaying source="LOCAL_INTERNET_RADIO"><ContentItem source="LOCAL_INTERNET_RADIO" location="/station?data=x" sourceAccount="listener@example.com"/></nowPlaying>`),
			http.StatusUnprocessableEntity, []string{"native-not-playing", "source=LOCAL_INTERNET_RADIO", "li***@example.com"}},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSavePlayingServer(t, tc.doc)
			var logBuf bytes.Buffer
			s.logger = slog.New(slog.NewTextHandler(&logBuf, nil))
			rec := putPreset(t, s, 2, `{"name":"x","type":"native"}`)
			if rec.Code != tc.code {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			log := logBuf.String()
			for _, w := range tc.want {
				if !strings.Contains(log, w) {
					t.Errorf("log misses %q: %s", w, log)
				}
			}
			if strings.Contains(log, "listener@example.com") {
				t.Errorf("account logged in clear: %s", log)
			}
		})
	}
}

// The phone page takes the desktop app's order: Spotify by its rules, the
// native services as type native, its own fresh station while it still plays,
// and everything else through the agent's save-what-plays.
func TestPhoneRemoteHoldSavesWhatPlays(t *testing.T) {
	for _, want := range []string{
		"await saveHeldSlot(slot);",
		"await saveHeldBody(slot, { slot: slot, name: name, type: 'native' });",
		"var st = stationStillPlaying(loc);",
		"if (st) { await saveStationToSlot(slot, st); return; }",
		"await savePlayingToSlot(slot);",
		"fetch('/api/box/save-playing', {",
		"var FIND_PLAY_FRESH_MS = 2 * 60 * 1000;",
		"if (url && streamOfLocation(loc) === url) return st;",
		"if (Date.now() - lastPlayedStationAt < FIND_PLAY_FRESH_MS) return st;",
		"if (c) { showMoveOffer(c, slot); return; }",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("phone page is missing %q", want)
		}
	}
	// The stale fallback that saved an old Find-tab station whatever played.
	if strings.Contains(indexHTML, "if (lastPlayedStation) { await saveStationToSlot(slot, lastPlayedStation); return; }") {
		t.Error("the phone hold still saves the last Find-tab station however long ago it played")
	}
	// Every other play started from the page ends that station's claim.
	if n := strings.Count(indexHTML, "forgetStationPlay();"); n < 6 {
		t.Errorf("forgetStationPlay() is called %d times, want one per other play path (6)", n)
	}
}

// #1467: the app started the station natively (the speaker fetches the raw
// proxy named in a descriptor), and the hold came minutes later. The
// descriptor holds one logo guess and no homepage or codec; the agent's own
// record of the play holds the whole logo chain and the station facts, and the
// hold must store those, the same as the "+" button would.
func TestSavePlayingUsesTheRecordOfANativePlay(t *testing.T) {
	raw := boxurl.RawStream(savePlayingStation)
	loc := OrionStationLocation(raw, "Jazz Lounge", "https://directory.example/favicon.ico")
	s := newSavePlayingServer(t, nowPlayingDoc("LOCAL_INTERNET_RADIO", loc, "Jazz Lounge", ""))
	chain := "https://img.example/jazz.png|https://jazz.example/favicon.ico"
	s.setLastPlayMeta(raw, "Jazz Lounge", chain, "audio/mpeg", "https://jazz.example/", "MP3", 128)
	if rec, _ := postSavePlaying(t, s, 1); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	p, _ := s.presets.Get(1)
	if p.StreamURL != savePlayingStation || p.Art != chain || p.Homepage != "https://jazz.example/" ||
		p.Codec != "MP3" || p.Bitrate != 128 {
		t.Fatalf("stored %+v", p)
	}
}

// The same record for a play the speaker took as a UPnP push: the speaker
// reports the raw proxy itself.
func TestSavePlayingUsesTheRecordOfAUPnPPlay(t *testing.T) {
	raw := boxurl.RawStream(savePlayingStation)
	s := newSavePlayingServer(t, nowPlayingDoc("UPNP", raw, "radio-one.mp3", ""))
	s.setLastPlayMeta(raw, "Radio One", "https://img.example/r1.png", "audio/aac", "https://radio-one.example/", "AAC", 0)
	if rec, _ := postSavePlaying(t, s, 4); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	p, _ := s.presets.Get(4)
	if p.Name != "Radio One" || p.Art != "https://img.example/r1.png" ||
		p.Homepage != "https://radio-one.example/" || p.Codec != "AAC" {
		t.Fatalf("stored %+v", p)
	}
}

// A record of another station, or one the speaker started by itself, is not
// what plays: the descriptor's own facts are saved and nothing of the record.
func TestSavePlayingIgnoresARecordThatDoesNotMatch(t *testing.T) {
	raw := boxurl.RawStream(savePlayingStation)
	loc := OrionStationLocation(raw, "Jazz Lounge", "")
	for name, lp := range map[string]*lastPlayInfo{
		"other station": {boxURL: boxurl.RawStream("http://127.0.0.1:1/other.mp3"), title: "Other",
			art: "https://img.example/o.png", homepage: "https://other.example/", codec: "AAC"},
		"from the box": {boxURL: raw, title: "Other", art: "https://img.example/o.png",
			homepage: "https://other.example/", codec: "AAC", fromBox: true},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSavePlayingServer(t, nowPlayingDoc("LOCAL_INTERNET_RADIO", loc, "", ""))
			lp.ts = time.Now()
			s.play.last = lp
			if rec, _ := postSavePlaying(t, s, 2); rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			p, _ := s.presets.Get(2)
			if p.Name != "Jazz Lounge" || p.Art != "" || p.Homepage != "" || p.Codec != "" {
				t.Fatalf("stored %+v", p)
			}
		})
	}
}

func TestLastPlayMatches(t *testing.T) {
	raw := boxurl.RawStream(savePlayingStation)
	otherHost := strings.Replace(raw, boxurl.Authority, "192.0.2.1:8888", 1)
	for _, c := range []struct {
		name                         string
		recorded, loc, station, orig string
		want                         bool
	}{
		{"upnp raw proxy", raw, raw, "", raw, true},
		{"native descriptor", raw, "/station?data=x", raw, savePlayingStation, true},
		{"proxy on another host", raw, "/station?data=x", otherHost, savePlayingStation, true},
		{"direct library play", savePlayingStation, savePlayingStation, "", savePlayingStation, true},
		{"other station", boxurl.RawStream("http://127.0.0.1:1/other.mp3"), raw, "", raw, false},
		{"no record", "", raw, "", raw, false},
	} {
		if got := lastPlayMatches(c.recorded, c.loc, c.station, c.orig); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// The station facts outlive an agent restart with the rest of the record.
func TestLastPlayMetaSurvivesARestart(t *testing.T) {
	s := &Server{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		lastPlayPath: filepath.Join(t.TempDir(), "last-play.json"),
	}
	s.setLastPlayMeta(boxurl.RawStream(savePlayingStation), "Jazz Lounge", "https://img.example/jazz.png", "audio/mpeg",
		"https://jazz.example/", "MP3", 128)
	s2 := &Server{logger: s.logger, lastPlayPath: s.lastPlayPath}
	s2.loadLastPlay()
	lp := s2.play.current()
	if lp == nil || lp.homepage != "https://jazz.example/" || lp.codec != "MP3" || lp.bitrate != 128 {
		t.Fatalf("restored %+v", lp)
	}
}
