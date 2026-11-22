package webui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A remount that fails is logged once, the stick is served from the
// read-write mount all the same, and it is tried again on a later look only
// after usbRemountRetryGap.
func TestAFailedRemountIsLoggedOnceAndServingGoesOn(t *testing.T) {
	f := newFakeStick(t, "rhino")
	media := t.TempDir()
	f.put(media, "a.mp3")
	f.plug(fatSector("fat32", 64))
	f.firmwareMount(media, "vfat")
	f.remountErr = errors.New("mount: mounting /dev/sda1 on /media/sda1 failed: Device or resource busy")
	s, logs := usbTestServer(t)

	row, ok := usbRow(listServers(t, s))
	if !ok || row.UDN == "" || row.Refusal != "" {
		t.Fatalf("the stick must be served from the read-write mount: %+v %v", row, ok)
	}
	snap := s.USBMusicDebug().(usbMusicSnapshot)
	if !snap.Served || snap.RemountedRO || !strings.Contains(snap.RemountError, "busy") ||
		!strings.HasPrefix(snap.MountOptions, "rw,") {
		t.Fatalf("snapshot: %+v", snap)
	}
	// More looks inside the retry gap: no new attempt, no new line.
	listServers(t, s)
	s.USBMusicDebug()
	if n := f.remounts(); n != 1 {
		t.Fatalf("want one attempt inside the retry gap, got %d", n)
	}
	if n := strings.Count(logs.String(), "could not switch the firmware's mount"); n != 1 {
		t.Fatalf("the failure must be logged once, got %d:\n%s", n, logs.String())
	}
	if _, _, ok := s.usbServed(true); !ok {
		t.Fatal("serving must go on")
	}

	// After the gap the next look tries again, and this time it takes.
	s.usb.mu.Lock()
	s.usb.remount.at = time.Now().Add(-2 * usbRemountRetryGap)
	s.usb.mu.Unlock()
	f.mu.Lock()
	f.remountErr = nil
	f.mu.Unlock()
	listServers(t, s)
	snap = s.USBMusicDebug().(usbMusicSnapshot)
	if f.remounts() != 2 || !snap.RemountedRO || snap.RemountError != "" {
		t.Fatalf("the retry must take: %d remounts, %+v", f.remounts(), snap)
	}
}

// A remount the shell reports as done while /proc/mounts still says rw is a
// failure: the result is read back, not assumed.
func TestARemountThatDidNotTakeIsAnError(t *testing.T) {
	f := newFakeStick(t, "taigan")
	media := t.TempDir()
	f.put(media, "a.flac")
	f.plug(fatSector("fat32", 64))
	f.firmwareMount(media, "vfat")
	f.remountNoop = true
	s, _ := usbTestServer(t)
	listServers(t, s)
	snap := s.USBMusicDebug().(usbMusicSnapshot)
	if !snap.Served || snap.RemountedRO || !strings.Contains(snap.RemountError, "still read-write") {
		t.Fatalf("snapshot: %+v", snap)
	}
}

// A stick whose classification failed is not known to be a music stick, so
// its mount stays as the firmware made it.
func TestAStickThatCouldNotBeClassifiedIsNotRemounted(t *testing.T) {
	f := newFakeStick(t, "rhino")
	f.plug(fatSector("fat32", 64))
	// Listed as mounted, but the directory cannot be read.
	f.firmwareMount(filepath.Join(t.TempDir(), "gone"), "vfat")
	s, _ := usbTestServer(t)
	row, ok := usbRow(listServers(t, s))
	if !ok || row.Refusal != usbRefusalUnreadable {
		t.Fatalf("row: %+v %v", row, ok)
	}
	if n := f.remounts(); n != 0 {
		t.Fatalf("an unclassified stick must not be remounted: %v", f.ops())
	}
}

// The agent start looks at the stick (no mount of its own) and also switches a
// firmware mount of a music stick to read-only.
func TestTheAgentStartRemountsAMusicStickReadOnly(t *testing.T) {
	f := newFakeStick(t, "rhino")
	media := t.TempDir()
	f.put(media, "a.mp3")
	f.plug(fatSector("fat32", 64))
	f.firmwareMount(media, "vfat")
	s, _ := usbTestServer(t)
	s.USBMusicStartupCheck()
	if ops := f.ops(); len(ops) != 1 || ops[0] != "remount-ro "+media {
		t.Fatalf("ops: %v", ops)
	}
}

// usbWaitFor polls cond for up to two seconds; the settle round runs in a
// goroutine of its own.
func usbWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func usbSettling(s *Server) bool {
	s.usb.mu.Lock()
	defer s.usb.mu.Unlock()
	return s.usb.settling
}

// The kernel's attach line starts one round of looks. The firmware's mount
// shows up a moment later; the round finds it, serves the stick, switches the
// mount to read-only and stops. More lines of the same insert start nothing,
// and the disconnect line stops serving at once.
func TestTheKernelAttachLineRemountsTheStickOnceTheFirmwareMountedIt(t *testing.T) {
	f := newFakeStick(t, "rhino")
	media := t.TempDir()
	f.put(media, "a.mp3")
	f.plug(fatSector("fat32", 64))
	s, logs := usbTestServer(t)
	// The first look comes at once; the firmware mounts the stick before the
	// second one.
	usbSettleDelays = []time.Duration{time.Millisecond, 300 * time.Millisecond, 300 * time.Millisecond, 300 * time.Millisecond}

	s.OnUSBDiskLine(true)
	usbWaitFor(t, "the first look", func() bool {
		s.usb.mu.Lock()
		defer s.usb.mu.Unlock()
		return s.usb.snap.Checks >= 1
	})
	if snap := s.USBMusicDebug().(usbMusicSnapshot); snap.Served {
		t.Fatalf("nothing is mounted yet, nothing may be served: %+v", snap)
	}
	f.firmwareMount(media, "vfat")
	// Lines of the same insert while the round runs start nothing new.
	s.OnUSBDiskLine(true)
	s.OnUSBDiskLine(true)
	usbWaitFor(t, "the round to end", func() bool { return !usbSettling(s) })

	snap := s.USBMusicDebug().(usbMusicSnapshot)
	if !snap.Served || !snap.RemountedRO || snap.MountedBy != "firmware" || snap.LastKernelEvent != "attached" {
		t.Fatalf("snapshot: %+v", snap)
	}
	if f.remounts() != 1 {
		t.Fatalf("want one remount, got %v", f.ops())
	}
	for _, op := range f.ops() {
		if strings.HasPrefix(op, "mount ") || strings.HasPrefix(op, "umount ") {
			t.Fatalf("a kernel line must neither mount nor unmount anything: %v", f.ops())
		}
	}
	if n := strings.Count(logs.String(), "looked at the stick after the kernel attached a USB disk"); n < 1 {
		t.Fatalf("the round must be logged:\n%s", logs.String())
	}

	// Pulled: the disconnect line drops the stick at once.
	f.unplug()
	s.OnUSBDiskLine(false)
	usbWaitFor(t, "the stick to be dropped", func() bool {
		_, _, ok := func() (any, any, bool) {
			s.usb.mu.Lock()
			defer s.usb.mu.Unlock()
			return nil, nil, s.usb.lib != nil
		}()
		return !ok
	})
	if snap := s.USBMusicDebug().(usbMusicSnapshot); snap.Present || snap.LastKernelEvent != "gone" {
		t.Fatalf("snapshot after the pull: %+v", snap)
	}
}

// An install stick the kernel attaches is looked at and left alone: no
// remount, no mount.
func TestTheKernelAttachLineLeavesAnInstallStickAlone(t *testing.T) {
	f := newFakeStick(t, "rhino")
	media := t.TempDir()
	f.put(media, "run.sh", "wlan.pass", "Music/a.mp3")
	f.plug(fatSector("fat32", 64))
	f.firmwareMount(media, "vfat")
	s, _ := usbTestServer(t)
	usbSettleDelays = []time.Duration{time.Millisecond, time.Millisecond}
	s.OnUSBDiskLine(true)
	usbWaitFor(t, "the round to end", func() bool {
		s.usb.mu.Lock()
		defer s.usb.mu.Unlock()
		return !s.usb.settling && s.usb.snap.Checks > 0
	})
	snap := s.USBMusicDebug().(usbMusicSnapshot)
	if snap.Refusal != usbRefusalInstallStick || snap.RemountedRO || len(f.ops()) != 0 {
		t.Fatalf("snapshot %+v, ops %v", snap, f.ops())
	}
}
