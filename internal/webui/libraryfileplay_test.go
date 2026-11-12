package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
)

// #1871: a third-party remote replayed a Recently-played album card by sending
// the card's URL and name to POST /api/play, without the MIME that marks a
// library file. The agent played it as a radio station and filed a new card
// under Radio next to the album's Library card.

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
