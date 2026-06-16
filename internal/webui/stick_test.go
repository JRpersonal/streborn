package webui

import (
	"os"
	"path/filepath"
	"testing"
)

// mkDisk creates a fake /sys/block/<disk> with the given removable flag.
func mkDisk(t *testing.T, root, disk, removable string) {
	t.Helper()
	dir := filepath.Join(root, disk)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "removable"), []byte(removable+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStickReallyMounted is the #105 regression: a non-removable internal disk
// (the box's own storage) must NOT count as a USB stick, while a removable one
// does. Built-in disks reporting removable=0 raised the "remove the USB stick"
// banner forever on speakers with no stick inserted.
func TestStickReallyMounted(t *testing.T) {
	sysRoot := t.TempDir()
	medRoot := t.TempDir()
	oldSys, oldMed := sysBlockRoot, mediaRoot
	sysBlockRoot, mediaRoot = sysRoot, medRoot
	t.Cleanup(func() { sysBlockRoot, mediaRoot = oldSys, oldMed })

	// No disks at all -> not mounted (the Portable case: no sda without a stick).
	if ok, _ := stickReallyMounted(); ok {
		t.Fatal("no disks must report not-mounted")
	}

	// An internal, non-removable disk -> still not a stick (#105: deqw's speakers).
	mkDisk(t, sysRoot, "sda", "0")
	if ok, _ := stickReallyMounted(); ok {
		t.Fatal("a non-removable internal disk must not count as a USB stick")
	}

	// A removable disk -> a stick, no version when nothing is mounted.
	if err := os.WriteFile(filepath.Join(sysRoot, "sda", "removable"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, ver := stickReallyMounted(); !ok || ver != "" {
		t.Fatalf("removable sda must be a stick with empty version, got ok=%v ver=%q", ok, ver)
	}

	// Mounted + readable version.txt -> returned.
	if err := os.MkdirAll(filepath.Join(medRoot, "sda1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(medRoot, "sda1", "version.txt"), []byte("v0.7.43\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, ver := stickReallyMounted(); !ok || ver != "v0.7.43" {
		t.Fatalf("expected version from /media/sda1, got ok=%v ver=%q", ok, ver)
	}

	// Only a removable sdb (no sda) is also detected.
	sysRoot2, medRoot2 := t.TempDir(), t.TempDir()
	sysBlockRoot, mediaRoot = sysRoot2, medRoot2
	mkDisk(t, sysRoot2, "sdb", "1")
	if ok, _ := stickReallyMounted(); !ok {
		t.Fatal("a removable sdb must be detected when sda is absent")
	}
}
