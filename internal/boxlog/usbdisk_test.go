package boxlog

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// syslogStamp formats t the way busybox stamps a ring line.
func syslogStamp(t time.Time) string { return t.Format("Jan _2 15:04:05") }

func TestUSBDiskLinesAreClassifiedFromTheKernelOnly(t *testing.T) {
	cases := []struct {
		line string
		want USBDiskChange
	}{
		{`Nov 21 18:53:01 rhino kern.notice kernel: sd 0:0:0:0: [sda] Attached SCSI removable disk`, USBDiskAttached},
		{`Nov 21 18:53:01 rhino kern.notice kernel: [  812.123456] sd 0:0:0:0: [sda] Attached SCSI disk`, USBDiskAttached},
		{`Nov 21 18:53:02 taigan kern.warn kernel: FAT-fs (sda1): Volume was not properly unmounted. Some data may be corrupt. Please run fsck.`, USBDiskAttached},
		{`Nov 21 18:59:40 rhino kern.info kernel: usb 1-1: USB disconnect, device number 4`, USBDiskGone},
		// The earlier lines of an insert come before the disk exists.
		{lineKernel, 0},
		{`Nov 21 18:53:01 rhino kern.info kernel:  sda: sda1`, 0},
	}
	for _, c := range cases {
		l, ok := ParseLine(c.line)
		if !ok {
			t.Fatalf("not parsed: %s", c.line)
		}
		class, classified := Classify(l)
		if got := usbDiskChangeOf(l.Message); got != c.want {
			t.Errorf("%q: change %v, want %v", c.line, got, c.want)
		}
		if (c.want != 0) != (classified && class == ClassUSBDisk) {
			t.Errorf("%q: class %q %v", c.line, class, classified)
		}
	}
	// The same words from a Bose daemon are not the kernel's.
	l, _ := ParseLine(`Nov 21 18:53:01 rhino local0.info BoseApp[1943]: [(002001):USB:INFO]USB disconnect seen`)
	if c, ok := Classify(l); ok && c == ClassUSBDisk {
		t.Fatal("a daemon line must not count as the kernel's")
	}
}

func TestUSBDiskHandlerHearsLiveLinesNotTheReplayedRing(t *testing.T) {
	r := New(slog.New(slog.DiscardHandler), nil, nil)
	var got []USBDiskEvent
	r.SetUSBDiskHandler(func(ev USBDiskEvent) { got = append(got, ev) })
	now := time.Now()

	// logread -f replays the ring at its start: an attach of an hour ago.
	r.InjectLine(syslogStamp(now.Add(-time.Hour))+` rhino kern.notice kernel: sd 0:0:0:0: [sda] Attached SCSI removable disk`, now)
	if len(got) != 0 {
		t.Fatalf("a replayed line must not be handed on: %+v", got)
	}
	r.InjectLine(syslogStamp(now)+` rhino kern.notice kernel: sd 0:0:0:0: [sda] Attached SCSI removable disk`, now)
	r.InjectLine(syslogStamp(now)+` rhino kern.info kernel: usb 1-1: new high-speed USB device number 5 using musb-hdrc`, now)
	r.InjectLine(syslogStamp(now)+` rhino kern.info kernel: usb 1-1: USB disconnect, device number 5`, now)
	if len(got) != 2 || got[0].Change != USBDiskAttached || got[1].Change != USBDiskGone ||
		!strings.Contains(got[0].Message, "Attached SCSI") {
		t.Fatalf("events: %+v", got)
	}
	// The lines are in the bundle's event list too.
	n := 0
	for _, e := range r.forensics.events.items() {
		if e.Class == ClassUSBDisk {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("want the three usb_disk lines (the replayed one included) in the events, got %d", n)
	}
}
