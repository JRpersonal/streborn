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
