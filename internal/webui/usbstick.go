package webui

import (
	"os"
	"path/filepath"
	"strings"
)

// Whether a USB stick is plugged into the speaker, for the desktop app.
//
// The app refreshes the program files on a setup stick before every agent
// update, so the next boot's stick-to-NAND sync does not revert the update.
// That refresh runs over SSH, and it used to open SSH on every update whether a
// stick was there or not. A speaker without a stick then had its port open
// until the update's reboot, and the app's "SSH access to this speaker is open"
// warning flashed for a speaker the user never asked to open (Jens' ST30 after
// the v1.0.2 update, 2026-10-04, no stick inserted).
//
// So the agent answers the one question the app needs first. Two signals, either
// one is enough:
//
//   - a SCSI disk block device (/sys/block/sd*), which is what a mounted or
//     unmounted stick looks like once the kernel has enumerated it. The mount
//     point is deliberately not used: the Portable does not auto-mount a stick.
//   - a USB interface of the mass-storage class (bInterfaceClass 08) on the
//     bus. A stick that has sat in the box since before a standby is sometimes
//     not re-enumerated as a block device until something rescans the SCSI
//     host, which the app's own probe does; the USB interface is still there,
//     so it still counts as present.
//
// Read on request, nothing cached, nothing written.

// usbSysRoot is the sysfs root, a variable so tests can point it at a fake tree.
var usbSysRoot = "/sys"

// usbStickPresent reports whether the speaker currently has a USB mass-storage
// device attached.
func usbStickPresent() bool {
	if blocks, _ := filepath.Glob(filepath.Join(usbSysRoot, "block", "sd*")); len(blocks) > 0 {
		return true
	}
	classes, _ := filepath.Glob(filepath.Join(usbSysRoot, "bus", "usb", "devices", "*", "bInterfaceClass"))
	for _, c := range classes {
		if b, err := os.ReadFile(c); err == nil && strings.TrimSpace(string(b)) == "08" {
			return true
		}
	}
	return false
}

// usbStickField is the /api/agent/version value: "present" or "absent".
func usbStickField() string {
	if usbStickPresent() {
		return "present"
	}
	return "absent"
}
