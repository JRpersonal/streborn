package presets

import "testing"

func TestSlotLayout(t *testing.T) {
	if BoxSlots != 6 || MaxSlot != 12 {
		t.Fatalf("BoxSlots=%d MaxSlot=%d, want 6 and 12", BoxSlots, MaxSlot)
	}
	for slot := -1; slot <= 14; slot++ {
		valid, box, row2 := ValidSlot(slot), IsBoxSlot(slot), IsRow2(slot)
		switch {
		case slot >= 1 && slot <= 6:
			if !valid || !box || row2 || KeyOf(slot) != slot || Row2Slot(slot) != slot+6 {
				t.Errorf("slot %d: valid=%v box=%v row2=%v key=%d row2slot=%d", slot, valid, box, row2, KeyOf(slot), Row2Slot(slot))
			}
		case slot >= 7 && slot <= 12:
			if !valid || box || !row2 || KeyOf(slot) != slot-6 || Row2Slot(slot) != 0 {
				t.Errorf("slot %d: valid=%v box=%v row2=%v key=%d row2slot=%d", slot, valid, box, row2, KeyOf(slot), Row2Slot(slot))
			}
		default:
			if valid || box || row2 || KeyOf(slot) != 0 {
				t.Errorf("slot %d must be invalid everywhere", slot)
			}
		}
	}
}
