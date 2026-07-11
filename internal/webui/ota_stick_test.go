package webui

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestRefreshStickAgentBinary covers the #381 OTA revert: run.sh's boot sync
// copies a stick's agent binary over NAND unconditionally, so an OTA must
// rewrite a still-inserted stick or the next boot reverts the update.
func TestRefreshStickAgentBinary(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	newBin := []byte("NEW-AGENT-BINARY")

	t.Run("stickless box is a no-op", func(t *testing.T) {
		sysRoot, medRoot := t.TempDir(), t.TempDir()
		oldSys, oldMed := sysBlockRoot, mediaRoot
		sysBlockRoot, mediaRoot = sysRoot, medRoot
		t.Cleanup(func() { sysBlockRoot, mediaRoot = oldSys, oldMed })

		refreshStickAgentBinary(newBin, logger) // must not panic or create files
		entries, err := os.ReadDir(medRoot)
		if err != nil || len(entries) != 0 {
			t.Fatalf("stickless refresh must write nothing, got entries=%v err=%v", entries, err)
		}
	})

	t.Run("stale stick binary is replaced", func(t *testing.T) {
		sysRoot, medRoot := t.TempDir(), t.TempDir()
		oldSys, oldMed := sysBlockRoot, mediaRoot
		sysBlockRoot, mediaRoot = sysRoot, medRoot
		t.Cleanup(func() { sysBlockRoot, mediaRoot = oldSys, oldMed })

		mkDisk(t, sysRoot, "sda", "1")
		mnt := filepath.Join(medRoot, "sda1")
		if err := os.MkdirAll(mnt, 0o755); err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(mnt, "streborn-armv7l")
		if err := os.WriteFile(dst, []byte("OLD-AGENT-BINARY"), 0o755); err != nil {
			t.Fatal(err)
		}

		refreshStickAgentBinary(newBin, logger)

		got, err := os.ReadFile(dst)
		if err != nil || string(got) != string(newBin) {
			t.Fatalf("stick binary not replaced: got %q err=%v", got, err)
		}
		if _, err := os.Stat(dst + ".new"); !os.IsNotExist(err) {
			t.Fatalf("temp file left behind on the stick: %v", err)
		}
	})

	t.Run("identical stick binary is left untouched", func(t *testing.T) {
		sysRoot, medRoot := t.TempDir(), t.TempDir()
		oldSys, oldMed := sysBlockRoot, mediaRoot
		sysBlockRoot, mediaRoot = sysRoot, medRoot
		t.Cleanup(func() { sysBlockRoot, mediaRoot = oldSys, oldMed })

		mkDisk(t, sysRoot, "sda", "1")
		mnt := filepath.Join(medRoot, "sda1")
		if err := os.MkdirAll(mnt, 0o755); err != nil {
			t.Fatal(err)
		}
		// The desktop app's SSH stick refresh may have run first (ota.go):
		// same content must cause zero writes (FAT flash wear is real).
		dst := filepath.Join(mnt, "streborn-armv7l")
		if err := os.WriteFile(dst, newBin, 0o755); err != nil {
			t.Fatal(err)
		}
		before, err := os.Stat(dst)
		if err != nil {
			t.Fatal(err)
		}

		refreshStickAgentBinary(newBin, logger)

		after, err := os.Stat(dst)
		if err != nil {
			t.Fatal(err)
		}
		if !after.ModTime().Equal(before.ModTime()) {
			t.Fatal("identical binary was rewritten instead of skipped")
		}
	})
}
