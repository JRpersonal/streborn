package webui

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStickDiskBase covers the whole-disk name derivation used to address the
// durable device/delete node (#381).
func TestStickDiskBase(t *testing.T) {
	cases := map[string]string{
		"/media/sda1": "sda",
		"/media/sdb1": "sdb",
		"/mnt/x/sdc2": "sdc",
		"":            "",
	}
	for in, want := range cases {
		if got := stickDiskBase(in); got != want {
			t.Errorf("stickDiskBase(%q) = %q, want %q", in, got, want)
		}
	}
}

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

	t.Run("stale stick binary is replaced and the USB cache is committed", func(t *testing.T) {
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
		// A writable delete node stands in for /sys/block/sda/device/delete so the
		// durable-commit write (#381) can be observed without touching real sysfs.
		delDir := filepath.Join(sysRoot, "sda", "device")
		if err := os.MkdirAll(delDir, 0o755); err != nil {
			t.Fatal(err)
		}
		del := filepath.Join(delDir, "delete")
		if err := os.WriteFile(del, nil, 0o644); err != nil {
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
		// The load-bearing #381 fix: a bare sync leaves the write in the USB
		// controller cache and the reboot reverts it. The device/delete write must
		// have fired to force the SCSI cache commit.
		if b, err := os.ReadFile(del); err != nil || strings.TrimSpace(string(b)) != "1" {
			t.Fatalf("device/delete not written to commit the USB cache (#381): got %q err=%v", b, err)
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
