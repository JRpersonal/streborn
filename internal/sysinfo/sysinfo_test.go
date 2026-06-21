package sysinfo

import (
	"net"
	"strings"
	"testing"
)

func TestDeviceIDFallback(t *testing.T) {
	// Look for a real interface we can verify in the test. We cannot expect
	// wlan0 (e.g. CI Linux), but some interface with a real MAC is normally
	// present (eth0 on Linux, vEthernet on Windows).
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("cannot read interfaces: %v", err)
	}

	haveAnyMAC := false
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(iface.HardwareAddr) > 0 && iface.HardwareAddr.String() != "00:00:00:00:00:00" {
			haveAnyMAC = true
			break
		}
	}
	if !haveAnyMAC {
		t.Skip("no interface with a MAC present")
	}

	// We deliberately pass non-existent interface preferences so the
	// fallback path is taken.
	id, err := DeviceID([]string{"intf-does-not-exist"})
	if err != nil {
		t.Fatalf("DeviceID fallback failed: %v", err)
	}
	if len(id) != 12 {
		t.Errorf("DeviceID length wrong, got %d (%s)", len(id), id)
	}
	// Must be uppercase hex
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'A' || r > 'F') {
			t.Errorf("DeviceID contains non-hex: %s", id)
			break
		}
	}
	if strings.Contains(id, ":") {
		t.Errorf("DeviceID contains colons: %s", id)
	}
}

func TestDeviceIDNoMatch(t *testing.T) {
	// If we could force a no-match... that is not portable without a mock,
	// so we only test the happy path above.
	t.Skip("negative test needs mocking, skipped")
}

func TestMACOfError(t *testing.T) {
	_, err := MACOf("nichtexistierende-interface-xyz")
	if err == nil {
		t.Error("expected an error for a non-existent interface")
	}
}

func TestIPOfError(t *testing.T) {
	_, err := IPOf("nichtexistierende-interface-xyz")
	if err == nil {
		t.Error("expected an error for a non-existent interface")
	}
}
