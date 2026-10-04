package webui

import (
	"os"
	"path/filepath"
	"testing"
)

// The real interface directories are named like 1-1:1.0; the colon is not a
// valid directory name on Windows, so the fake tree uses 1-1_1.0. The glob in
// usbStickPresent matches either.
func withFakeSys(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	old := usbSysRoot
	usbSysRoot = root
	t.Cleanup(func() { usbSysRoot = old })
	return root
}

func TestUSBStickAbsentOnABoxWithoutOne(t *testing.T) {
	root := withFakeSys(t)
	// A speaker's own flash shows up as mtdblock/ubi, never as sd*.
	if err := os.MkdirAll(filepath.Join(root, "block", "mtdblock0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bus", "usb", "devices", "1-0_1.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The root hub is class 09, not mass storage.
	if err := os.WriteFile(filepath.Join(root, "bus", "usb", "devices", "1-0_1.0", "bInterfaceClass"), []byte("09\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := usbStickField(); got != "absent" {
		t.Fatalf("usbStickField = %q, want absent", got)
	}
}

func TestUSBStickPresentAsBlockDevice(t *testing.T) {
	root := withFakeSys(t)
	if err := os.MkdirAll(filepath.Join(root, "block", "sda"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := usbStickField(); got != "present" {
		t.Fatalf("usbStickField = %q, want present", got)
	}
}

// A stick that is on the bus but not yet enumerated as a block device (the
// case the app's SCSI rescan exists for) still counts as present.
func TestUSBStickPresentAsMassStorageInterfaceOnly(t *testing.T) {
	root := withFakeSys(t)
	dir := filepath.Join(root, "bus", "usb", "devices", "1-1_1.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bInterfaceClass"), []byte("08\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := usbStickField(); got != "present" {
		t.Fatalf("usbStickField = %q, want present", got)
	}
}
