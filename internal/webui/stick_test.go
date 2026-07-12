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

// TestStickReallyMounted covers #105 and #179: a stick counts only on POSITIVE
// proof, a readable STR marker on a mounted /media/<disk>1. A non-removable
// internal disk never counts (#105), and, crucially, a removable/USB disk with
// NOTHING mounted does not count either (#179): deqw's speakers exposed an
// internal disk as a removable/USB sda that is never mounted, which kept the
// "remove the USB stick" banner up forever with no stick to remove.
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

	// An internal, non-removable disk -> not a stick (#105: deqw's speakers).
	mkDisk(t, sysRoot, "sda", "0")
	if ok, _ := stickReallyMounted(); ok {
		t.Fatal("a non-removable internal disk must not count as a USB stick")
	}

	// A removable disk with NOTHING mounted -> still NOT a stick (#179): without a
	// readable marker there is no proof a real STR stick is in the speaker.
	if err := os.WriteFile(filepath.Join(sysRoot, "sda", "removable"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, _ := stickReallyMounted(); ok {
		t.Fatal("a removable disk with no mounted STR marker must not count as a stick (#179)")
	}

	// Mounted + readable version.txt -> a real stick, version returned.
	if err := os.MkdirAll(filepath.Join(medRoot, "sda1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(medRoot, "sda1", "version.txt"), []byte("v0.7.43\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, ver := stickReallyMounted(); !ok || ver != "v0.7.43" {
		t.Fatalf("expected version from /media/sda1, got ok=%v ver=%q", ok, ver)
	}

	// A stick predating version.txt: a layout marker (run.sh) on the mount counts,
	// with an empty version. Use a fresh removable sdb to prove sdb is scanned too.
	sysRoot2, medRoot2 := t.TempDir(), t.TempDir()
	sysBlockRoot, mediaRoot = sysRoot2, medRoot2
	mkDisk(t, sysRoot2, "sdb", "1")
	if ok, _ := stickReallyMounted(); ok {
		t.Fatal("a removable sdb with no mounted marker must not count as a stick (#179)")
	}
	if err := os.MkdirAll(filepath.Join(medRoot2, "sdb1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(medRoot2, "sdb1", "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ok, ver := stickReallyMounted(); !ok || ver != "" {
		t.Fatalf("a removable sdb with an STR marker must be detected, got ok=%v ver=%q", ok, ver)
	}
}

// TestSSHPersistentEnabled covers #381/#385: SSH left open via a persistent NAND
// marker (a maintainer-placed /mnt/nv/remote_services, or STR's own
// /mnt/nv/streborn/enable-ssh opt-in) must be reported as persistent, so the app
// stops mislabeling a stickless box as "stick still inserted" and stops advising
// a reboot that would not close SSH.
func TestSSHPersistentEnabled(t *testing.T) {
	nv := t.TempDir()
	old := nvRoot
	nvRoot = nv
	t.Cleanup(func() { nvRoot = old })

	// Clean NAND, no markers -> not persistent (transient stick-driven SSH).
	if sshPersistentEnabled() {
		t.Fatal("no NAND marker must report not-persistent")
	}

	// The maintainer-placed /mnt/nv/remote_services marker -> persistent.
	if err := os.WriteFile(filepath.Join(nv, "remote_services"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !sshPersistentEnabled() {
		t.Fatal("/mnt/nv/remote_services must report persistent SSH")
	}

	// STR's own opt-in marker, in a nested dir, also counts.
	nv2 := t.TempDir()
	nvRoot = nv2
	if err := os.MkdirAll(filepath.Join(nv2, "streborn"), 0o755); err != nil {
		t.Fatal(err)
	}
	if sshPersistentEnabled() {
		t.Fatal("an empty streborn dir without the marker must not count")
	}
	if err := os.WriteFile(filepath.Join(nv2, "streborn", "enable-ssh"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !sshPersistentEnabled() {
		t.Fatal("/mnt/nv/streborn/enable-ssh must report persistent SSH")
	}
}
