package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
)

// #1871, 2026-11-13: key 6 held a library file saved as a radio station (no
// Source, no MIME, only the file's URL). With the music server off, a press in
// STR Remote woke the speaker, which answered the file with BAD_URL, and the
// press still answered "playing" and filed a Radio card.

func radioKeyOnServer(slot int) presets.Preset {
	return presets.Preset{Slot: slot, Type: "radio", Name: "Counterparts", StreamURL: offTrackURL}
}

func TestPlaySlotRefusesARadioKeyOnAMusicServerThatIsOff(t *testing.T) {
	o := &offProbes{}
	s, rec := offPlayServer(t, o)
	s.recent = recent.New()
	if err := s.presets.SetSlot(radioKeyOnServer(6)); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handlePlaySlot(w, httptest.NewRequest(http.MethodPost, "/api/play/6", nil))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v %s", err, w.Body.String())
	}
	if body["code"] != libraryServerOfflineCode || body["server"] != "MinimServer" || body["offline"] != true {
		t.Fatalf("body = %v, want the offline code, the server and offline:true", body)
	}
	if body["slot"] != float64(6) || body["name"] != "Counterparts" {
		t.Fatalf("body = %v, want the key's slot and name", body)
	}
	if rec.count() != 0 {
		t.Fatalf("the speaker was sent %v for a file nobody serves", rec.list())
	}
	if n := len(s.recent.All()); n != 0 {
		t.Fatalf("a press that never started filed %d Recently-played entries", n)
	}
	if o.hostProbes != 1 {
		t.Fatalf("the file's address was probed %d times, want once", o.hostProbes)
	}
}

// The same key with its server switched on plays as before.
func TestPlaySlotStillPlaysARadioKeyOnAMusicServerThatAnswers(t *testing.T) {
	o := &offProbes{serves: true, hostAnswers: true}
	s, rec := offPlayServer(t, o)
	if err := s.presets.SetSlot(radioKeyOnServer(6)); err != nil {
		t.Fatal(err)
	}
	if _, ref := s.playSlot(context.Background(), 6); ref != nil {
		t.Fatalf("a key whose server answers was refused: %+v", ref)
	}
	if rec.count() == 0 {
		t.Fatal("the key never reached the speaker")
	}
}

// A station that does not sit on a registered music server is never probed,
// whatever its address.
func TestPlaySlotNeverProbesAStationOffTheMusicServer(t *testing.T) {
	for name, url := range map[string]string{
		"internet station":      "http://stream.example/relax",
		"station on another IP": "http://192.0.2.50:8000/live",
	} {
		t.Run(name, func(t *testing.T) {
			o := &offProbes{}
			s, _ := offPlayServer(t, o)
			if err := s.presets.SetSlot(presets.Preset{Slot: 2, Type: "radio", Name: "Relax FM", StreamURL: url}); err != nil {
				t.Fatal(err)
			}
			if _, ref := s.playSlot(context.Background(), 2); ref != nil {
				t.Fatalf("a station was refused: %+v", ref)
			}
			if o.hostProbes != 0 {
				t.Fatalf("a station was probed %d times", o.hostProbes)
			}
		})
	}
}
