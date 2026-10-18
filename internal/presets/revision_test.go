package presets

import (
	"path/filepath"
	"testing"
)

// Every write moves the revision a client polls to re-read the presets at once
// (#1217); a read does not.
func TestRevisionMovesOnEveryWrite(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatalf("Load(new): %v", err)
	}
	last := s.Revision()
	step := func(what string, write func() error) {
		t.Helper()
		if err := write(); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if got := s.Revision(); got <= last {
			t.Fatalf("%s: revision %d, want above %d", what, got, last)
		}
		last = s.Revision()
	}
	step("SetSlot", func() error {
		return s.SetSlot(Preset{Slot: 1, Name: "One", StreamURL: "http://example.com/one", Type: "radio"})
	})
	step("SetSlots", func() error {
		return s.SetSlots([]Preset{{Slot: 2, Name: "Two", StreamURL: "http://example.com/two", Type: "radio"}})
	})
	step("SwapSlots", func() error { return s.SwapSlots(1, 2) })
	step("MoveSlot", func() error {
		return s.MoveSlot(2, Preset{Slot: 3, Name: "One", StreamURL: "http://example.com/one", Type: "radio"})
	})
	step("RemoveSlot", func() error { return s.RemoveSlot(3) })

	_ = s.All()
	_, _ = s.Get(1)
	if got := s.Revision(); got != last {
		t.Fatalf("a read moved the revision: %d, want %d", got, last)
	}
}
