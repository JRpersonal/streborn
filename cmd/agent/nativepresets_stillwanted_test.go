package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/webui"
)

// specFor builds the sweep spec the reconcile pass builds for a stored preset,
// with the native location it would write.
func specFor(p presets.Preset) boxcli.PresetSpec {
	return boxcli.PresetSpec{
		Slot: p.Slot, Name: p.Name, StreamURL: boxPresetURL(p),
		NativeLocation: webui.OrionStationLocation(boxPresetURL(p), p.Name, p.Art),
	}
}

// The native readback re-writes slots from a snapshot taken when the sweep
// began. A key the owner deleted or replaced in the app meanwhile must not be
// re-written: the stale write brought a deleted key back on the box, and its
// firmware echo was reported as a refused long press (#1217).
func TestNativeRewriteSkipsSlotsTheStoreNoLongerWants(t *testing.T) {
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	wdr := presets.Preset{Slot: 2, Name: "WDR 5", Type: "radio", StreamURL: "http://stream.example/wdr5"}
	live := presets.Preset{Slot: 5, Name: "1LIVE", Type: "radio", StreamURL: "http://stream.example/1live"}
	relax := presets.Preset{Slot: 6, Name: "Absolut Relax", Type: "radio", StreamURL: "http://stream.example/relax"}
	for _, p := range []presets.Preset{wdr, live, relax} {
		if err := store.SetSlot(p); err != nil {
			t.Fatal(err)
		}
	}
	pending := map[int]boxcli.PresetSpec{2: specFor(wdr), 5: specFor(live), 6: specFor(relax)}

	// While the sweep runs: key 6 is deleted, key 5 gets another station.
	if err := store.RemoveSlot(6); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSlot(presets.Preset{Slot: 5, Name: "Deutschlandfunk", Type: "radio", StreamURL: "http://stream.example/dlf"}); err != nil {
		t.Fatal(err)
	}

	stillWanted := func(_ int, spec boxcli.PresetSpec) bool { return specStillWanted(store, spec) }
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	got := keepStillWanted([]int{2, 5, 6}, pending, stillWanted, logger)
	if want := []int{2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("slots to re-write = %v, want %v", got, want)
	}
	if _, ok := pending[6]; ok {
		t.Fatal("deleted slot 6 still pending, the next readback would count it as lost")
	}
	if _, ok := pending[5]; ok {
		t.Fatal("replaced slot 5 still pending")
	}

	// nil keeps the old behaviour: every lost slot is re-written.
	if got := keepStillWanted([]int{2, 5}, map[int]boxcli.PresetSpec{}, nil, logger); !reflect.DeepEqual(got, []int{2, 5}) {
		t.Fatalf("nil stillWanted filtered slots: %v", got)
	}
}
