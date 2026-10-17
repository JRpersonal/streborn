package webui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
)

// A Pandora station started from the app lands in Recently played with what a
// replay needs: the station reference, its item type and the listed cover
// (#1101).
func TestPandoraPlayRecordsRecentCard(t *testing.T) {
	s, _, box := nativeTestServer(true)
	s.recent = recent.New()
	pandoraLinked(box)
	box.stations = []boxapi.ServiceStation{pandoraStation("111", "Jazz Radio")}
	s.handlePandoraStations(httptest.NewRecorder(), nativeReq(http.MethodGet, "/api/pandora/stations", "", "192.168.1.5:5000", ""))
	w := httptest.NewRecorder()
	s.handlePandoraPlay(w, nativeReq(http.MethodPost, "/api/pandora/play", `{"location":"111","name":"Jazz Radio","type":"stationurl"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusOK {
		t.Fatalf("play: %d %s", w.Code, w.Body.String())
	}
	all := s.recent.All()
	if len(all) != 1 {
		t.Fatalf("recent = %+v", all)
	}
	e := all[0]
	if e.Source != "pandora" || e.CardKey != "pandora:111" || e.CardURL != "111" || e.CardName != "Jazz Radio" ||
		e.ItemType != "stationurl" || e.CardArt != "http://img.example.com/111.jpg" {
		t.Fatalf("card = %+v", e)
	}
}

// An unlisted station still records the default item type, so the replay can
// rebuild the item after an agent restart.
func TestPandoraPlayUnlistedRecordsDefaultType(t *testing.T) {
	s, _, box := nativeTestServer(true)
	s.recent = recent.New()
	pandoraLinked(box)
	s.handlePandoraPlay(httptest.NewRecorder(), nativeReq(http.MethodPost, "/api/pandora/play", `{"location":"333","name":"Rock"}`, "192.168.1.5:5000", "application/json"))
	all := s.recent.All()
	if len(all) != 1 || all[0].ItemType != "stationurl" || all[0].CardArt != "" {
		t.Fatalf("recent = %+v", all)
	}
}

// A station the speaker refused is not history.
func TestPandoraPlayRefusedRecordsNothing(t *testing.T) {
	s, _, box := nativeTestServer(true)
	s.recent = recent.New()
	pandoraLinked(box)
	box.selectErr = errors.New("box /select: 500")
	s.handlePandoraPlay(httptest.NewRecorder(), nativeReq(http.MethodPost, "/api/pandora/play", `{"location":"111","name":"Jazz Radio"}`, "192.168.1.5:5000", "application/json"))
	if all := s.recent.All(); len(all) != 0 {
		t.Fatalf("refused play recorded: %+v", all)
	}
}

// A Pandora key pressed on the speaker gets a card; iHeartRadio, which no card
// can replay, still does not.
func TestNoteRecentPresetNative(t *testing.T) {
	s := &Server{recent: recent.New()}
	s.NoteRecentPreset(presets.Preset{Slot: 2, Type: presets.TypeNative, Name: "Jazz Radio", Native: &presets.NativeItem{
		Source: "PANDORA", Location: "111", ItemType: "stationurl", ContainerArt: "http://img.example.com/111.jpg",
	}})
	s.NoteRecentPreset(presets.Preset{Slot: 3, Type: presets.TypeNative, Name: "Hits", Native: &presets.NativeItem{
		Source: "IHEART", Location: "555", ItemType: "stationurl",
	}})
	all := s.recent.All()
	if len(all) != 1 {
		t.Fatalf("recent = %+v", all)
	}
	if e := all[0]; e.Source != "pandora" || e.CardKey != "pandora:111" || e.CardName != "Jazz Radio" || e.ItemType != "stationurl" {
		t.Fatalf("card = %+v", e)
	}
}
