package webui

import (
	"context"
	"errors"
	"fmt"
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

// A Pandora key recalled from the app (or the phone page) gets the same card a
// Find-tab start records, so the two collapse into one and the card replays
// through /api/pandora/play. A slot the speaker refused records nothing, and an
// iHeartRadio slot, which no card can replay, records nothing either.
func TestPlaySlotNativeRecordsRecentCardOnlyOnSuccess(t *testing.T) {
	pandora := presets.Preset{Slot: 4, Type: presets.TypeNative, Name: "Jazz Radio", Native: &presets.NativeItem{
		Source: "PANDORA", SourceAccount: "listener@example.com", Location: "111", ItemType: "stationurl",
	}}
	iheart := presets.Preset{Slot: 5, Type: presets.TypeNative, Name: "Hits", Native: &presets.NativeItem{
		Source: "IHEART", SourceAccount: "listener@example.com", Location: "555", ItemType: "stationurl",
	}}
	cases := []struct {
		name   string
		p      presets.Preset
		refuse bool
		wantN  int
	}{
		{"pandora started", pandora, false, 1},
		{"pandora refused", pandora, true, 0},
		{"iheart started", iheart, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newPlayTestServer(t)
			s.recent = recent.New()
			s.selectFn = func(context.Context, string) error {
				if tc.refuse {
					return errors.New("box /select: 500")
				}
				return nil
			}
			if err := s.presets.SetSlot(tc.p); err != nil {
				t.Fatalf("SetSlot: %v", err)
			}
			w := httptest.NewRecorder()
			s.handlePlaySlot(w, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/play/%d", tc.p.Slot), nil))
			want := http.StatusOK
			if tc.refuse {
				want = http.StatusBadGateway
			}
			if w.Code != want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, want, w.Body.String())
			}
			all := s.recent.All()
			if len(all) != tc.wantN {
				t.Fatalf("recent = %+v, want %d entries", all, tc.wantN)
			}
			if tc.wantN == 1 {
				e := all[0]
				if e.Source != "pandora" || e.CardKey != "pandora:111" || e.CardURL != "111" || e.CardName != "Jazz Radio" || e.ItemType != "stationurl" {
					t.Fatalf("card = %+v", e)
				}
			}
		})
	}
}

// The key and the Find tab record the same station under one card key, so a
// key press right after a Find-tab start folds into the existing card.
func TestPandoraKeyAndFindTabShareOneCard(t *testing.T) {
	s, _, box := nativeTestServer(true)
	s.recent = recent.New()
	pandoraLinked(box)
	s.handlePandoraPlay(httptest.NewRecorder(), nativeReq(http.MethodPost, "/api/pandora/play", `{"location":"111","name":"Jazz Radio","type":"stationurl"}`, "192.168.1.5:5000", "application/json"))
	s.NoteRecentPreset(presets.Preset{Slot: 4, Type: presets.TypeNative, Name: "Jazz Radio", Native: &presets.NativeItem{
		Source: "pandora", Location: "111", ItemType: "stationurl",
	}})
	if all := s.recent.All(); len(all) != 1 || all[0].CardKey != "pandora:111" {
		t.Fatalf("recent = %+v, want one pandora:111 card", all)
	}
}
