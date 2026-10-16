package presets

import (
	"path/filepath"
	"testing"
)

// A key's own volume (#1323) survives every whole-preset write that does not
// name one, is switched off only through SetVolume, and is persisted.
func TestKeyVolumeSurvivesSavesAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.SetSlot(Preset{Slot: 2, Name: "News", StreamURL: "http://example.com/news", Type: "radio"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.SetVolume(2, 5); err != nil || !ok {
		t.Fatalf("SetVolume = %v %v", ok, err)
	}
	// A re-save without a volume (an app save, a hold, a metadata heal).
	if err := s.SetSlot(Preset{Slot: 2, Name: "Other station", StreamURL: "http://example.com/other", Type: "radio"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSlots([]Preset{{Slot: 2, Name: "Copied", StreamURL: "http://example.com/copied", Type: "radio"}}); err != nil {
		t.Fatal(err)
	}
	re, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := re.Get(2); p.Volume != 5 || p.Name != "Copied" {
		t.Fatalf("after re-saves and a reload: %+v, want the copied station at volume 5", p)
	}
	if _, _, err := re.SetVolume(2, 0); err != nil {
		t.Fatal(err)
	}
	if p, _ := re.Get(2); p.Volume != 0 {
		t.Fatalf("volume %d after switching it off", p.Volume)
	}
}

func TestSetVolumeClampsAndNeedsAKey(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.SetVolume(3, 20); ok {
		t.Fatal("an empty key accepted a volume")
	}
	if err := s.SetSlot(Preset{Slot: 3, Name: "X", StreamURL: "http://example.com/x", Type: "radio"}); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := s.SetVolume(3, 250); p.Volume != MaxPresetVolume {
		t.Fatalf("volume %d, want it clamped to %d", p.Volume, MaxPresetVolume)
	}
}

// A moved preset takes its volume along: the station is what was set quiet.
func TestMoveSlotCarriesTheVolume(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSlot(Preset{Slot: 1, Name: "X", StreamURL: "http://example.com/x", Type: "radio", Volume: 4}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Get(1)
	p.Slot = 5
	if err := s.MoveSlot(1, p); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(5); got.Volume != 4 {
		t.Fatalf("moved preset volume %d, want 4", got.Volume)
	}
}
