package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileCreatesAndReadsBack(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "presets.json")
	want := []byte(`{"presets":[{"slot":1}]}`)
	if err := WriteFile(p, want, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("content = %q, want %q", got, want)
	}
	// The temp sibling must not survive a successful write.
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}

func TestWriteFileOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "last-play.json")
	if err := WriteFile(p, []byte("old-and-longer-content"), 0o644); err != nil {
		t.Fatalf("first WriteFile: %v", err)
	}
	if err := WriteFile(p, []byte("new"), 0o644); err != nil {
		t.Fatalf("second WriteFile: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("content = %q, want %q (no truncation leftover)", got, "new")
	}
}

// A directory in place of the target must fail cleanly, leaving nothing torn.
func TestWriteFileTargetIsDir(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "adir")
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("x"), 0o644); err == nil {
		t.Fatal("expected error writing over a directory, got nil")
	}
}
