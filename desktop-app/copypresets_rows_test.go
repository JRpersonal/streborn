package main

import "testing"

// The second row of six keys (#1110): slots 7 to 12 are the hardware keys
// pressed twice. A copy carries both rows to a speaker whose agent keeps them
// and row 1 only to one whose agent predates them, which would refuse a
// row-2 slot.

func twoRows() []Preset {
	return []Preset{
		{Slot: 1, Name: "1LIVE", Type: "radio", StreamURL: "http://example.com/1"},
		{Slot: 6, Name: "WDR 5", Type: "radio", StreamURL: "http://example.com/6"},
		{Slot: 7, Name: "Radio Bob", Type: "radio", StreamURL: "http://example.com/7"},
		{Slot: 12, Name: "Sunshine Live", Type: "radio", StreamURL: "http://example.com/12"},
		{Slot: 13, Name: "never a key", Type: "radio", StreamURL: "http://example.com/13"},
	}
}

func TestPresetRowsFromVersion(t *testing.T) {
	cases := []struct {
		ver  map[string]string
		want int
	}{
		{nil, 1},
		{map[string]string{"version": "v1.0.20"}, 1},
		{map[string]string{"presetRows": "1"}, 1},
		{map[string]string{"presetRows": "2"}, 2},
		{map[string]string{"presetRows": "3"}, 1},
	}
	for _, c := range cases {
		if got := presetRowsFromVersion(c.ver); got != c.want {
			t.Errorf("presetRowsFromVersion(%v) = %d, want %d", c.ver, got, c.want)
		}
	}
}

func TestPresetsUpToKeepsNamedSlotsInRange(t *testing.T) {
	in := append(twoRows(), Preset{Slot: 3, Name: ""}, Preset{Slot: 0, Name: "zero"})
	slots := func(ps []Preset) []int {
		var out []int
		for _, p := range ps {
			out = append(out, p.Slot)
		}
		return out
	}
	if got := slots(presetsUpTo(in, 12)); len(got) != 4 || got[2] != 7 || got[3] != 12 {
		t.Errorf("both rows: got slots %v, want [1 6 7 12]", got)
	}
	if got := slots(presetsUpTo(in, 6)); len(got) != 2 || got[0] != 1 || got[1] != 6 {
		t.Errorf("one row: got slots %v, want [1 6]", got)
	}
}

func TestCopyCarriesBothRowsToATargetThatKeepsThem(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", twoRows())
	dst := newFakeBox(t, "127.0.0.3", nil)
	dst.bulk = true
	dst.presetRows = "2"
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	n, err := a.CopyPresetsAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t))
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if n != 4 || dst.count() != 4 {
		t.Errorf("copied %d, target holds %d, want 4 and 4 (%v)", n, dst.count(), dst.written)
	}
	if dst.written[7] != "Radio Bob" || dst.written[12] != "Sunshine Live" {
		t.Errorf("the keys pressed twice did not arrive: %v", dst.written)
	}
	if _, ok := dst.written[13]; ok {
		t.Errorf("slot 13 is no key and must never be written: %v", dst.written)
	}
}

func TestCopyKeepsRowOneOnlyForAnOlderTarget(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", twoRows())
	dst := newFakeBox(t, "127.0.0.3", nil) // no presetRows: an agent before #1110
	dst.bulk = true
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	n, err := a.CopyPresetsAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t))
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if n != 2 || dst.count() != 2 {
		t.Errorf("copied %d, target holds %d, want 2 and 2 (%v)", n, dst.count(), dst.written)
	}
	for slot := 7; slot <= 12; slot++ {
		if _, ok := dst.written[slot]; ok {
			t.Errorf("slot %d reached a target without the second row: %v", slot, dst.written)
		}
	}
}

func TestCopyPerSlotFallbackCarriesBothRows(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", twoRows())
	dst := newFakeBox(t, "127.0.0.3", nil) // no bulk endpoint, but the second row
	dst.presetRows = "2"
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	n, err := a.CopyPresetsAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t))
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if n != 4 || dst.written[7] != "Radio Bob" {
		t.Errorf("copied %d, target holds %v, want 4 with key 1 pressed twice", n, dst.written)
	}
}
