package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
)

// #1871: a third-party remote replayed a Recently-played album card by sending
// the card's URL and name to POST /api/play, without the MIME that marks a
// library file. The agent played it as a radio station and filed a new card
// under Radio next to the album's Library card; with the music server off
// nothing played and the Radio card was all that came of it.

const (
	albumFirstTrack = "http://192.0.2.82:9790/minimserver/*/Music/Album/01*20First.m4a"
	albumCardKey    = "queue:slot:11:0a1b2c3d"
)

// albumReplayServer is a play server whose Recently-played ring holds an
// album card the way a folder key leaves it: the card and one song row.
func albumReplayServer(t *testing.T) (*Server, *soapRecorder) {
	t.Helper()
	s, rec := newPlayTestServer(t)
	s.recent = recent.New()
	s.recent.Add(recent.Entry{Source: "upnp", CardKey: albumCardKey, CardName: "Album",
		CardURL: albumFirstTrack, Mime: "audio/mp4", DurationSec: 337})
	s.recent.Add(recent.Entry{Source: "upnp", CardKey: albumCardKey, CardName: "Album",
		CardURL: albumFirstTrack, Mime: "audio/mp4", DurationSec: 337, Track: "Artist - First"})
	return s, rec
}

// postPlayBody sends one POST /api/play and returns the answer.
func postPlayBody(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	t.Cleanup(s.bumpRecallGen)
	w := httptest.NewRecorder()
	s.handlePlay(w, httptest.NewRequest(http.MethodPost, "/api/play", strings.NewReader(body)))
	return w
}

func TestAKnownLibraryFilePlayedWithoutAMimeStaysALibraryPlay(t *testing.T) {
	s, _ := albumReplayServer(t)
	before := len(s.recent.All())

	w := postPlayBody(t, s, `{"url":"`+albumFirstTrack+`","title":"Album"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("play: status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	ring := s.recent.All()
	if len(ring) != before+1 {
		t.Fatalf("the play filed %d entries, want 1", len(ring)-before)
	}
	got := ring[len(ring)-1]
	if got.Source != "upnp" {
		t.Errorf("the play was filed under %q, want upnp (Library)", got.Source)
	}
	if got.Mime != "audio/mp4" || got.DurationSec != 337 {
		t.Errorf("the card lost what the agent knew of the file: mime %q, duration %d", got.Mime, got.DurationSec)
	}
	for _, e := range ring {
		if e.Source == "radio" {
			t.Errorf("a radio card was filed for a library file: %+v", e)
		}
	}
	// Played the library way: handed to the speaker directly, with its length.
	tr := activeFileTrack(t, s)
	if tr.boxURL != albumFirstTrack || tr.mime != "audio/mp4" {
		t.Errorf("the speaker was handed %q as %q, want the file itself as audio/mp4", tr.boxURL, tr.mime)
	}
}

func TestAFileOnAFolderKeyPlayedWithoutAMimeStaysALibraryPlay(t *testing.T) {
	s, _ := newPlayTestServer(t)
	s.recent = recent.New()
	if err := s.presets.SetSlot(presets.Preset{Slot: 11, Type: "queue", Name: "Album",
		Items: []presets.PresetItem{{URL: albumFirstTrack, Title: "First", Mime: "audio/mp4", DurationSec: 337}}}); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}

	w := postPlayBody(t, s, `{"url":"`+albumFirstTrack+`","title":"Album"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("play: status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	ring := s.recent.All()
	if len(ring) != 1 || ring[0].Source != "upnp" || ring[0].Mime != "audio/mp4" {
		t.Fatalf("ring = %+v, want one upnp card with the key's MIME", ring)
	}
}

func TestAStationPlayedWithoutAMimeStaysRadio(t *testing.T) {
	s, _ := albumReplayServer(t)
	const station = "http://stream.example/relax"

	w := postPlayBody(t, s, `{"url":"`+station+`","title":"Relax FM"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("play: status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	ring := s.recent.All()
	if got := ring[len(ring)-1]; got.Source != "radio" || got.CardKey != station {
		t.Fatalf("a station was filed as %+v, want a radio card", got)
	}
}

func TestALibraryFileIsRefusedAndNotFiledWhenItsServerIsOff(t *testing.T) {
	for name, body := range map[string]string{
		"a client that sends no MIME":  `{"url":"` + albumFirstTrack + `","title":"Album"}`,
		"a client that sends the MIME": `{"url":"` + albumFirstTrack + `","title":"Album","mime":"audio/mp4"}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, rec := albumReplayServer(t)
			store, err := mediaservers.Load(filepath.Join(t.TempDir(), "mediaservers.json"))
			if err != nil {
				t.Fatalf("store: %v", err)
			}
			if err := store.Add(mediaservers.Server{ID: "uuid:AAAA-BBBB", Name: "MinimServer[example-mac.local]",
				Location: "http://192.0.2.82:9791/AAAA-BBBB/Upnp/device.xml"}); err != nil {
				t.Fatalf("seed store: %v", err)
			}
			s.mediaServers = store
			var probed []string
			s.queueHostAnswers = func(_ context.Context, raw string) bool {
				probed = append(probed, raw)
				return false
			}
			before := len(s.recent.All())

			w := postPlayBody(t, s, body)
			if w.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502 (body %s)", w.Code, w.Body.String())
			}
			var got map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("body is not JSON: %v (%s)", err, w.Body.String())
			}
			if got["code"] != libraryServerOfflineCode || got["offline"] != true {
				t.Errorf("body = %v, want the library-server-offline refusal", got)
			}
			if got["server"] != "MinimServer[example-mac.local]" {
				t.Errorf("server = %v, want the registered server at the file's address", got["server"])
			}
			if e, _ := got["error"].(string); !strings.Contains(e, "MinimServer[example-mac.local] is not answering") {
				t.Errorf("error = %q, want the sentence that names the server", e)
			}
			if len(probed) != 1 || probed[0] != albumFirstTrack {
				t.Errorf("probed %v, want the file's address once", probed)
			}
			if n := len(s.recent.All()); n != before {
				t.Errorf("a play that never started filed %d Recently-played entries", n-before)
			}
			if rec.count() != 0 {
				t.Errorf("the speaker was sent %v for a file nobody serves", rec.list())
			}
		})
	}
}

// A media server addressed by name is not judged: a name this agent cannot
// resolve says nothing about what the speaker reaches.
func TestALibraryFileOnANamedHostIsNotProbed(t *testing.T) {
	s, _ := newPlayTestServer(t)
	s.queueHostAnswers = func(context.Context, string) bool {
		t.Error("a host name was probed")
		return false
	}
	w := postPlayBody(t, s, `{"url":"http://nas.example/music/1.flac","title":"Song","mime":"audio/flac"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("play: status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
}

// A radio station is never probed either, whatever its address.
func TestAStationOnAnIPAddressIsNotProbed(t *testing.T) {
	s, _ := newPlayTestServer(t)
	s.queueHostAnswers = func(context.Context, string) bool {
		t.Error("a radio station was probed")
		return false
	}
	w := postPlayBody(t, s, `{"url":"http://192.0.2.50:8000/live","title":"Relax FM"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("play: status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
}

// The phone page says which server is off when a single library file is
// refused, on every path that starts one: before the refusal existed these
// paths either said nothing or a bare "Could not start".
func TestPhoneRemoteSaysWhyASingleLibraryFileWasRefused(t *testing.T) {
	if !strings.Contains(indexHTML, "function libPlayRefusal(res)") {
		t.Fatal("the phone page lost libPlayRefusal")
	}
	for want, n := range map[string]int{
		"searchMsg(libPlayRefusal(res) || findFail(res), true)": 1, // a search result
		"searchMsg(libPlayRefusal(lr) || findFail(lr), true)":   1, // a library favourite
		"browseMsgSet(libPlayRefusal(res) || T.cantStart)":      2, // the server browser
		"libPlayRefusal(r)) showPlayNotice(libPlayRefusal(r))":  1, // Recently played
	} {
		if got := strings.Count(indexHTML, want); got != n {
			t.Errorf("%q: found %d times, want %d", want, got, n)
		}
	}
}
