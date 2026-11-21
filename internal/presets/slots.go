package presets

// Preset slot layout (#1110, #1657). The speaker has six hardware preset keys
// and its firmware stores exactly six presets. STR keeps up to ten rows of six:
// row N is reached by pressing a hardware key N times. Rows 2 and later live
// in STR's store and never reach the firmware.
const (
	// BoxSlots is the number of hardware preset keys and of presets the
	// speaker firmware stores (row 1).
	BoxSlots = 6
	// Rows is the maximum number of rows of presets STR keeps.
	Rows = 10
	// DefaultRows preserves the existing two-row setup; users can opt in to
	// rows 3-10.
	DefaultRows = 2
	// MaxSlot is the highest preset slot STR accepts (rows * BoxSlots).
	MaxSlot = Rows * BoxSlots
)

// ValidSlot reports whether slot is any STR preset slot, 1..MaxSlot.
func ValidSlot(slot int) bool { return slot >= 1 && slot <= MaxSlot }

// IsBoxSlot reports whether slot is a hardware key slot the firmware stores,
// 1..BoxSlots (row 1).
func IsBoxSlot(slot int) bool { return slot >= 1 && slot <= BoxSlots }

// RowOf reports which row contains a slot, or 0 for an invalid slot.
func RowOf(slot int) int {
	if !ValidSlot(slot) {
		return 0
	}
	return (slot-1)/BoxSlots + 1
}

// IsRow2 reports whether slot belongs to the second row.
func IsRow2(slot int) bool { return RowOf(slot) == 2 }

// IsExtraRow reports whether slot belongs to any row after the hardware row.
func IsExtraRow(slot int) bool { return RowOf(slot) > 1 }

// KeyOf returns the hardware key (1..BoxSlots) a slot is reached from: the
// slot itself on row 1, slot-BoxSlots on row 2, 0 for an invalid slot.
func KeyOf(slot int) int {
	switch {
	case IsBoxSlot(slot):
		return slot
	case IsExtraRow(slot):
		return (slot-1)%BoxSlots + 1
	}
	return 0
}

// RowSlot returns the slot for a hardware key (1..BoxSlots) on row (1..Rows),
// or 0 when either argument is out of range.
func RowSlot(row, key int) int {
	if row < 1 || row > Rows || !IsBoxSlot(key) {
		return 0
	}
	return (row-1)*BoxSlots + key
}

// Row2Slot and Row3Slot keep named helpers for the older hold-to-save and
// compatibility paths. New row-aware code should use RowSlot.
func Row2Slot(key int) int { return RowSlot(2, key) }
func Row3Slot(key int) int { return RowSlot(3, key) }
