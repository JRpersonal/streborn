package boxcli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxwrites"
)

// The second preset row (slots 7..12, #1110) lives only in STR's store. The
// firmware's keys are 1..6, so every command that writes, removes or presses a
// speaker preset refuses a row-2 slot before anything is sent. The context is
// already cancelled: a command that got past the guard would fail on the dial
// instead, with a different error.
func TestBoxPresetCommandsRefuseRowTwo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	const host = "192.0.2.1"
	for _, slot := range []int{0, 7, 9, 12, 13} {
		calls := map[string]error{
			"AddPreset":            AddPreset(ctx, host, slot, "Row two", "http://127.0.0.1:8888/stream/9"),
			"AddPresetRaw":         AddPresetRaw(ctx, host, slot, "DEEZER", "playlist", "123", "Mix", "acct"),
			"AddPresetContentItem": AddPresetContentItem(ctx, host, slot, "PANDORA", "stationurl", "loc", "Station", "acct"),
			"AddPresetNative":      AddPresetNative(ctx, host, slot, "Row two", "/station?data=x"),
			"RemovePreset":         RemovePreset(ctx, host, slot),
			"PresetKey":            PresetKey(ctx, host, slot, "p"),
		}
		for name, err := range calls {
			if !errors.Is(err, ErrNotBoxSlot) {
				t.Errorf("%s(slot %d) = %v, want ErrNotBoxSlot", name, slot, err)
			}
		}
		if boxwrites.SlotWrittenWithin(slot, time.Minute) {
			t.Errorf("slot %d was noted as written although nothing was sent", slot)
		}
	}
}

// Row-1 slots get past the guard: the same cancelled context then fails on the
// dial, which is a different error.
func TestBoxPresetCommandsAcceptRowOne(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for slot := 1; slot <= 6; slot++ {
		if err := RemovePreset(ctx, "192.0.2.1", slot); errors.Is(err, ErrNotBoxSlot) {
			t.Errorf("RemovePreset(slot %d) refused a speaker key", slot)
		}
	}
}

// SyncAllPresets, the sweep every reconcile and the boot sync use, skips a
// row-2 spec without a word: no command, no error for it. A row-1 spec in the
// same sweep is attempted (and fails here on the cancelled dial).
func TestSyncAllPresetsNeverSendsRowTwo(t *testing.T) {
	old := WriteGap
	WriteGap = 0
	t.Cleanup(func() { WriteGap = old })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	errs := SyncAllPresets(ctx, "192.0.2.1", []PresetSpec{
		{Slot: 9, Name: "Row two", StreamURL: "http://127.0.0.1:8888/stream/9"},
		{Slot: 3, Name: "Row one", StreamURL: "http://127.0.0.1:8888/stream/3"},
	})
	if _, sent := errs[9]; sent {
		t.Errorf("slot 9 reached the speaker: %v", errs[9])
	}
	if _, tried := errs[3]; !tried {
		t.Error("slot 3 should have been attempted (and failed on the cancelled dial)")
	}
}
