package presets

import (
	"path/filepath"
	"testing"
)

// SwapSlots exchanges two keys in one persisted write, and an empty key makes
// it a move: the other key ends up empty, nothing is lost or duplicated.
func TestSwapSlots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load(new): %v", err)
	}
	for _, p := range []Preset{
		{Slot: 5, Name: "Station", StreamURL: "http://example.com/five", Type: "radio"},
		{Slot: 6, Name: "Mix", URI: "spotify:playlist:abc", Type: "spotify", Shuffle: true},
		{Slot: 1, Name: "Untouched", StreamURL: "http://example.com/one", Type: "radio"},
	} {
		if err := s.SetSlot(p); err != nil {
			t.Fatalf("SetSlot: %v", err)
		}
	}
	if err := s.SwapSlots(5, 6); err != nil {
		t.Fatalf("SwapSlots: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load(reload): %v", err)
	}
	if p, ok := reloaded.Get(5); !ok || p.Name != "Mix" || !p.Shuffle || p.URI != "spotify:playlist:abc" {
		t.Errorf("slot 5 = %+v (present %v), want the mix with its shuffle", p, ok)
	}
	if p, ok := reloaded.Get(6); !ok || p.Name != "Station" {
		t.Errorf("slot 6 = %+v (present %v), want the station", p, ok)
	}
	if p, ok := reloaded.Get(1); !ok || p.Name != "Untouched" {
		t.Errorf("slot 1 = %+v (present %v), want it untouched", p, ok)
	}
	if err := reloaded.SwapSlots(2, 5); err != nil {
		t.Fatalf("SwapSlots(empty): %v", err)
	}
	if p, ok := reloaded.Get(2); !ok || p.Name != "Mix" {
		t.Errorf("slot 2 = %+v (present %v), want the mix moved over", p, ok)
	}
	if _, ok := reloaded.Get(5); ok {
		t.Error("slot 5 must be empty after a swap with an empty key")
	}
	if n := len(reloaded.All()); n != 3 {
		t.Errorf("store holds %d presets, want 3", n)
	}
}
