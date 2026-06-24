package webui

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsForeignNANDDir guards the freshness check that answers Jens' question
// for an over-full box: is a top-level /mnt/nv dir STR's, Bose's, or a leftover
// from a third-party post-cloud tool / another custom firmware the owner ran
// before STR (a candidate for what is eating the tiny ~31 MB NAND).
func TestIsForeignNANDDir(t *testing.T) {
	cases := []struct {
		name    string
		foreign bool
	}{
		{"streborn", false},
		{"nv", false},
		{"lost+found", false},
		{"BoseApp-Persistence", false},
		{"product-persistence", false}, // matches "persistence"
		{"Bose", false},
		{"bose-stuff", false},
		{"soundtouch-mod", true}, // a community tool's dir
		{"librespot", true},
		{"shairport", true},
	}
	for _, c := range cases {
		if got := isForeignNANDDir(c.name); got != c.foreign {
			t.Errorf("isForeignNANDDir(%q) = %v, want %v", c.name, got, c.foreign)
		}
	}
}

// TestWriteBinaryAtomic checks the OTA write writes the body, leaves no temp
// behind, and clears a stale .new from an earlier interrupted OTA first (the
// repeat-failure trap on the tight NAND).
func TestWriteBinaryAtomic(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "streborn-armv7l")

	// A stale temp from a previous failed attempt must not survive.
	stale := dst + ".new"
	if err := os.WriteFile(stale, []byte("partial-garbage"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := make([]byte, 4096)
	for i := range body {
		body[i] = byte(i)
	}
	if err := writeBinaryAtomic(dst, body); err != nil {
		t.Fatalf("writeBinaryAtomic: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if len(got) != len(body) || got[0] != body[0] || got[len(got)-1] != body[len(body)-1] {
		t.Fatalf("written content mismatch: got %d bytes", len(got))
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale .new temp should be gone, stat err = %v", err)
	}
}

// TestDirBytes sums file sizes recursively and ignores directories.
func TestDirBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b"), make([]byte, 200), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := dirBytes(dir); got != 300 {
		t.Errorf("dirBytes = %d, want 300", got)
	}
}
