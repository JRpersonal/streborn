package presets

// Preset slot layout (#1110). The speaker has six hardware preset keys and its
// firmware stores exactly six presets. STR keeps a second row of six on top:
// slot 6+n is "key n pressed twice". The agent stores row 2 in its own store
// exactly like row 1, but row 2 never reaches the firmware, because the
// firmware's keys are 1..6 and a write outside that range is either refused or,
// worse, lands on a key the user did not choose.
const (
	// BoxSlots is the number of hardware preset keys and of presets the
	// speaker firmware stores (row 1).
	BoxSlots = 6
	// Rows is how many rows of presets STR keeps.
	Rows = 2
	// MaxSlot is the highest preset slot STR accepts (rows * BoxSlots).
	MaxSlot = Rows * BoxSlots
)

// ValidSlot reports whether slot is any STR preset slot, 1..MaxSlot.
func ValidSlot(slot int) bool { return slot >= 1 && slot <= MaxSlot }

// IsBoxSlot reports whether slot is a hardware key slot the firmware stores,
// 1..BoxSlots (row 1).
func IsBoxSlot(slot int) bool { return slot >= 1 && slot <= BoxSlots }

// IsRow2 reports whether slot belongs to the second row, BoxSlots+1..MaxSlot.
func IsRow2(slot int) bool { return slot > BoxSlots && slot <= MaxSlot }

// KeyOf returns the hardware key (1..BoxSlots) a slot is reached from: the
// slot itself on row 1, slot-BoxSlots on row 2, 0 for an invalid slot.
func KeyOf(slot int) int {
	switch {
	case IsBoxSlot(slot):
		return slot
	case IsRow2(slot):
		return slot - BoxSlots
	}
	return 0
}

// Row2Slot returns the row-2 slot reached by pressing hardware key twice, 0
// when key is not a hardware key.
func Row2Slot(key int) int {
	if !IsBoxSlot(key) {
		return 0
	}
	return key + BoxSlots
}
