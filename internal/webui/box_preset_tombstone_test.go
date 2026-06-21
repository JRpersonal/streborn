package webui

import (
	"testing"
	"time"
)

// TestForgetBoxPresetHidesAndTombstones verifies that deleting a slot drops it
// from the box-preset snapshot at once and that a trailing presetsUpdated does
// not resurrect it while the tombstone is fresh (the "deleted preset comes back
// as a UPNP entry I cannot delete" report).
func TestForgetBoxPresetHidesAndTombstones(t *testing.T) {
	s := &Server{}
	s.NoteBoxPresets([]BoxPreset{
		{Slot: 1, Source: "UPNP"},
		{Slot: 6, Source: "UPNP"},
	})

	s.forgetBoxPreset(6)

	if got := s.boxPresetSlots(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("after delete, box presets = %v, want only slot 1", got)
	}

	// The box re-reports its full list (still including slot 6). The fresh
	// tombstone must keep slot 6 out of the merged view.
	s.NoteBoxPresets([]BoxPreset{
		{Slot: 1, Source: "UPNP"},
		{Slot: 6, Source: "UPNP"},
	})
	if got := s.boxPresetSlots(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("trailing presetsUpdated resurrected slot 6: %v", got)
	}
}

// TestForgetBoxPresetTombstoneExpires verifies that once the tombstone ages out,
// a slot the box still reports is shown again (it genuinely still exists there).
func TestForgetBoxPresetTombstoneExpires(t *testing.T) {
	s := &Server{}
	s.forgetBoxPreset(6)
	// Backdate the tombstone past its TTL.
	s.boxPresetsMu.Lock()
	s.deletedBoxSlots[6] = time.Now().Add(-boxPresetTombstoneTTL - time.Second)
	s.boxPresetsMu.Unlock()

	s.NoteBoxPresets([]BoxPreset{{Slot: 6, Source: "UPNP"}})
	if got := s.boxPresetSlots(); len(got) != 1 || got[0] != 6 {
		t.Fatalf("expired tombstone should no longer hide slot 6, got %v", got)
	}
}

// boxPresetSlots is a test helper returning the slots currently in the snapshot.
func (s *Server) boxPresetSlots() []int {
	s.boxPresetsMu.Lock()
	defer s.boxPresetsMu.Unlock()
	out := make([]int, 0, len(s.boxPresets))
	for _, p := range s.boxPresets {
		out = append(out, p.Slot)
	}
	return out
}
