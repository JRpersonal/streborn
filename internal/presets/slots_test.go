package presets

import "testing"

func TestSlotLayout(t *testing.T) {
	if BoxSlots != 6 || Rows != 10 || MaxSlot != 60 {
		t.Fatalf("BoxSlots=%d Rows=%d MaxSlot=%d, want 6, 10 and 60", BoxSlots, Rows, MaxSlot)
	}
	for slot := 1; slot <= MaxSlot; slot++ {
		row, key := RowOf(slot), (slot-1)%BoxSlots+1
		if !ValidSlot(slot) || KeyOf(slot) != key || RowSlot(row, key) != slot {
			t.Errorf("slot %d: row=%d key=%d mapped=%d", slot, row, key, RowSlot(row, key))
		}
		if (row == 1) != IsBoxSlot(slot) || (row > 1) != IsExtraRow(slot) || IsRow2(slot) != (row == 2) {
			t.Errorf("slot %d has inconsistent row classification", slot)
		}
	}
	for _, slot := range []int{-1, 0, MaxSlot + 1} {
		if ValidSlot(slot) || IsBoxSlot(slot) || RowOf(slot) != 0 || KeyOf(slot) != 0 {
			t.Errorf("slot %d must be invalid everywhere", slot)
		}
	}
	if RowSlot(0, 1) != 0 || RowSlot(Rows+1, 1) != 0 || RowSlot(1, 0) != 0 || RowSlot(1, BoxSlots+1) != 0 {
		t.Error("RowSlot must reject invalid row and key values")
	}
}
