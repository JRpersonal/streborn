package streamproxy

import (
	"testing"
	"time"
)

// The watchdog in cmd/agent asks this package "did we refuse a fetch just now",
// so a station the box dropped because of our own 404 is not counted against
// the speaker. Nothing else records that, so the record has to be right.
func TestASlotMissIsRememberedWithItsAge(t *testing.T) {
	prevSlot, prevAt := lastMiss.slot, lastMiss.at
	t.Cleanup(func() { lastMiss.slot, lastMiss.at = prevSlot, prevAt })

	lastMiss.slot, lastMiss.at = 0, time.Time{}
	if _, _, ok := LastSlotMiss(); ok {
		t.Fatal("an agent that never refused a slot must report none")
	}

	noteSlotMiss(6)
	slot, ago, ok := LastSlotMiss()
	if !ok || slot != 6 {
		t.Fatalf("LastSlotMiss() = (%d, %v, %v), want slot 6", slot, ago, ok)
	}
	if ago < 0 || ago > time.Minute {
		t.Errorf("age = %v, want a fresh refusal", ago)
	}

	// The newest refusal is the one a drop has to be judged against.
	noteSlotMiss(2)
	if slot, _, _ := LastSlotMiss(); slot != 2 {
		t.Errorf("slot = %d, want the most recent refusal (2)", slot)
	}
}
