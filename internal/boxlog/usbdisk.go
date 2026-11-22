package boxlog

import (
	"strings"
	"time"
)

// The kernel saying that a USB disk came or went.
//
// The speaker's kernel logs through klogd into the same busybox ring the
// firmware's daemons log into: a capture from a kitchen ST10 (rhino, FW
// 27.0.6, 2026-09-06) carries "kern.info kernel: usb 1-1: new high-speed USB
// device number 4 using musb-hdrc" (lineKernel in forensics_test.go). The
// agent reads every ring line already, so the music stick code (#1136) can
// look at a stick the moment the kernel attaches it instead of on a timer or
// a netlink listener of its own.
//
// Which lines count, all kernel 3.14 mass-storage shapes:
//
//	sd 0:0:0:0: [sda] Attached SCSI removable disk   the disk is there
//	FAT-fs (sda1): Volume was not properly unmounted the firmware mounts it
//	usb 1-1: USB disconnect, device number 2         the stick was pulled
//
// The earlier lines of an insert ("new high-speed USB device", "USB Mass
// Storage device detected", " sda: sda1") are not used: they come before the
// disk exists, and every insert logs the attach line anyway.

// USBDiskChange is what one kernel line says about a USB disk.
type USBDiskChange int

const (
	// USBDiskAttached is a disk attached or a FAT volume on it mounted: the
	// mount may still be a moment away.
	USBDiskAttached USBDiskChange = iota + 1
	// USBDiskGone is a USB device disconnected.
	USBDiskGone
)

// String names the change for logs.
func (c USBDiskChange) String() string {
	switch c {
	case USBDiskAttached:
		return "attached"
	case USBDiskGone:
		return "gone"
	}
	return "unknown"
}

// USBDiskEvent is one such kernel line.
type USBDiskEvent struct {
	Change  USBDiskChange
	Message string
	At      time.Time
}

// USBDiskHandler receives each one. It runs on the reader's goroutine, so it
// must return at once; anything slow belongs in a goroutine of its own.
type USBDiskHandler func(USBDiskEvent)

// SetUSBDiskHandler installs the hook. Set before Run; nil disables it.
func (r *Reader) SetUSBDiskHandler(h USBDiskHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usbDiskHandler = h
}

// isUSBDiskLine reports whether a kernel message is one of the lines above.
func isUSBDiskLine(m string) bool {
	return usbDiskChangeOf(m) != 0
}

// usbDiskChangeOf reads the change a kernel message reports, 0 for none.
func usbDiskChangeOf(m string) USBDiskChange {
	switch {
	case strings.Contains(m, "Attached SCSI removable disk"),
		strings.Contains(m, "Attached SCSI disk"),
		strings.Contains(m, "FAT-fs (sd"):
		return USBDiskAttached
	case strings.Contains(m, "USB disconnect"):
		return USBDiskGone
	}
	return 0
}

// fireUSBDisk hands a USB disk line to the hook. A line logread replayed from
// the ring at its (re)start is not handed on: the agent looks at the stick
// when it starts anyway, and an old attach must not count as a new one.
func (r *Reader) fireUSBDisk(ev Event) {
	if ev.Class != ClassUSBDisk || ev.Backlog {
		return
	}
	change := usbDiskChangeOf(ev.Message)
	if change == 0 {
		return
	}
	r.mu.Lock()
	h := r.usbDiskHandler
	r.mu.Unlock()
	if h != nil {
		h(USBDiskEvent{Change: change, Message: ev.Message, At: ev.At})
	}
}
