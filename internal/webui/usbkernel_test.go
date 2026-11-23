package webui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The dmesg of a mojo ST30 whose USB port never powered the stick (#119,
// 2026-11-23): nothing in sysfs, nothing mounted, and only these kernel
// lines say why.
const mojoVBUSDmesg = `[   13.163833] usb0: MAC aa:bb:cc:dd:ee:ff
[   13.176518] g_ether gadget: Ethernet Gadget, version: Memorial Day 2008
[   13.176569] musb-hdrc musb-hdrc.0.auto: otg: usb_otg_register_gadget: device not registered to otg core
[   20.000000] wlan0: associated
[   25.234689] musb-hdrc musb-hdrc.0.auto: VBUS_ERROR in a_wait_vrise (91, <VBusValid), retry #1, port1 00000100
[   26.100000] usb 1-1: new high-speed USB device number 2 using musb-hdrc
[   26.200000] usb 1-1: SerialNumber: 0123456789ABCDEF
[   26.300000] usb-storage 1-1:1.0: USB Mass Storage device detected
[   27.000000] sd 0:0:0:0: [sda] 7827456 512-byte logical blocks
[   27.010000]  sda: sda1
[   30.000000] FAT-fs (sda1): unable to read boot sector
`

func TestUSBKernelLinesKeepTheStickStory(t *testing.T) {
	old := usbDmesgFn
	defer func() { usbDmesgFn = old }()
	usbDmesgFn = func() ([]byte, error) { return []byte(mojoVBUSDmesg), nil }

	got := usbKernelLines()
	joined := strings.Join(got, "\n")
	for _, want := range []string{"VBUS_ERROR", "usb 1-1: new high-speed", "USB Mass Storage", "[sda]", "sda: sda1", "FAT-fs (sda1)", "usb_otg_register_gadget"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	for _, never := range []string{"usb0:", "g_ether", "wlan0", "aa:bb", "SerialNumber", "0123456789"} {
		if strings.Contains(joined, never) {
			t.Errorf("%q must stay out of the bundle:\n%s", never, joined)
		}
	}
}

func TestUSBKernelLinesKeepOnlyTheNewest(t *testing.T) {
	old := usbDmesgFn
	defer func() { usbDmesgFn = old }()
	var b strings.Builder
	for i := 0; i < usbKernelKeep+10; i++ {
		b.WriteString("musb-hdrc: VBUS_ERROR retry " + strings.Repeat("x", i) + "\n")
	}
	usbDmesgFn = func() ([]byte, error) { return []byte(b.String()), nil }
	got := usbKernelLines()
	if len(got) != usbKernelKeep {
		t.Fatalf("kept %d lines, want %d", len(got), usbKernelKeep)
	}
	if !strings.HasSuffix(got[len(got)-1], strings.Repeat("x", usbKernelKeep+9)) {
		t.Fatalf("the newest line is missing: %q", got[len(got)-1])
	}
}

func TestUSBMusicDebugCarriesKernelLines(t *testing.T) {
	old := usbDmesgFn
	defer func() { usbDmesgFn = old }()
	usbDmesgFn = func() ([]byte, error) { return []byte(mojoVBUSDmesg), nil }

	s, _ := usbTestServer(t)
	raw, err := json.Marshal(s.USBMusicDebug())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["present"]; !ok {
		t.Fatalf("the snapshot fields must stay at the top level: %s", raw)
	}
	lines, _ := m["kernel_usb"].([]any)
	if len(lines) == 0 {
		t.Fatalf("no kernel_usb in %s", raw)
	}

	usbDmesgFn = func() ([]byte, error) { return nil, errors.New("no dmesg") }
	raw, _ = json.Marshal(s.USBMusicDebug())
	if strings.Contains(string(raw), "kernel_usb") {
		t.Fatalf("a failed dmesg must leave the field out: %s", raw)
	}
}
