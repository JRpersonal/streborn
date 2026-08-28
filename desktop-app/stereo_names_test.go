package main

import "testing"

func TestApplyStereoName(t *testing.T) {
	// set adds a key
	m := applyStereoName(nil, "A+B", "Living room")
	if m["A+B"] != "Living room" {
		t.Fatalf("set: got %q, want %q", m["A+B"], "Living room")
	}

	// overwrite replaces
	m = applyStereoName(m, "A+B", "Office")
	if m["A+B"] != "Office" {
		t.Fatalf("overwrite: got %q, want %q", m["A+B"], "Office")
	}

	// a name is trimmed on the way in
	m = applyStereoName(m, "A+B", "  Kitchen  ")
	if m["A+B"] != "Kitchen" {
		t.Fatalf("trim: got %q, want %q", m["A+B"], "Kitchen")
	}

	// keys are independent
	m = applyStereoName(m, "C+D", "Bedroom")
	if m["A+B"] != "Kitchen" || m["C+D"] != "Bedroom" {
		t.Fatalf("independent keys: got %v", m)
	}

	// a blank (or whitespace-only) name deletes the key, reverting to default
	m = applyStereoName(m, "A+B", "   ")
	if _, ok := m["A+B"]; ok {
		t.Fatalf("blank should delete the key, got %v", m)
	}
	if m["C+D"] != "Bedroom" {
		t.Fatalf("deleting one key must not touch another: got %v", m)
	}

	// empty name deletes too
	m = applyStereoName(m, "C+D", "")
	if len(m) != 0 {
		t.Fatalf("empty name should delete; map should be empty, got %v", m)
	}
}

func TestApplyStereoNameLengthCap(t *testing.T) {
	// A name longer than the cap is truncated by RUNES (not bytes), so the
	// server enforces the same limit the DOM advertises even if the bound
	// method is reached directly.
	long := ""
	for i := 0; i < 100; i++ {
		long += "ä" // multi-byte rune, to prove rune-not-byte truncation
	}
	m := applyStereoName(nil, "A+B", long)
	got := []rune(m["A+B"])
	if len(got) != maxStereoNameLen {
		t.Fatalf("length cap: got %d runes, want %d", len(got), maxStereoNameLen)
	}
	for _, r := range got {
		if r != 'ä' {
			t.Fatalf("truncation split a rune: got %q", string(got))
		}
	}
}
