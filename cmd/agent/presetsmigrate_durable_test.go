package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The one-time stick-to-NAND preset migration copies the stick store verbatim
// at 0644 into a directory it creates and leaves no temp file. It never
// overwrites a store NAND already has.
func TestMigratePresetsToNANDContentModeAndNoOverwrite(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stick := filepath.Join(t.TempDir(), "presets.json")
	const stickStore = `{"presets":[{"slot":1,"name":"Stick Radio"}]}`
	if err := os.WriteFile(stick, []byte(stickStore), 0o644); err != nil {
		t.Fatal(err)
	}
	nand := filepath.Join(t.TempDir(), "streborn", "presets.json")

	migratePresetsToNAND(stick, nand, logger)
	b, err := os.ReadFile(nand)
	if err != nil || string(b) != stickStore {
		t.Fatalf("NAND store = %q, %v", b, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(nand)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o644 {
			t.Fatalf("mode = %v, want 0644", fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(nand + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}

	// NAND has a store now: a later stick copy must not replace it.
	if err := os.WriteFile(stick, []byte(`{"presets":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	migratePresetsToNAND(stick, nand, logger)
	b, err = os.ReadFile(nand)
	if err != nil || string(b) != stickStore {
		t.Fatalf("second migration changed the NAND store: %q, %v", b, err)
	}
}

// A migration write that fails must not leave a NAND store behind at all: an
// empty or partial file there would be read as "no presets" from then on and,
// because the file exists, would also stop the migration from ever running
// again. With nothing written, the next start simply tries again.
func TestMigratePresetsToNANDFailedWriteLeavesNoStore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stick := filepath.Join(t.TempDir(), "presets.json")
	const stickStore = `{"presets":[{"slot":1,"name":"Stick Radio"}]}`
	if err := os.WriteFile(stick, []byte(stickStore), 0o644); err != nil {
		t.Fatal(err)
	}
	nand := filepath.Join(t.TempDir(), "presets.json")

	// Occupy the temp sibling with a directory so the write cannot start.
	if err := os.Mkdir(nand+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	migratePresetsToNAND(stick, nand, logger)
	if _, err := os.Stat(nand); !os.IsNotExist(err) {
		t.Fatalf("a failed migration left a NAND store behind: %v", err)
	}

	// Once the write can go through, the retry migrates the stick store.
	if err := os.Remove(nand + ".tmp"); err != nil {
		t.Fatal(err)
	}
	migratePresetsToNAND(stick, nand, logger)
	b, err := os.ReadFile(nand)
	if err != nil || string(b) != stickStore {
		t.Fatalf("retry did not migrate: %q, %v", b, err)
	}
}
