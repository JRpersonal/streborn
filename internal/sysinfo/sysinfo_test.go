package sysinfo

import (
	"net"
	"strings"
	"testing"
)

func TestDeviceIDFallback(t *testing.T) {
	// Suche nach einer realen Interface die wir im Test verifizieren koennen.
	// Wir koennen kein wlan0 erwarten (z.B. CI Linux), aber irgendeine Interface
	// mit echter MAC ist normalerweise da (eth0 auf Linux, vEthernet auf Win).
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("kann Interfaces nicht lesen: %v", err)
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
		t.Skip("keine Interface mit MAC vorhanden")
	}

	// Wir geben absichtlich nicht existierende Interface Praeferenzen, damit
	// der Fallback Pfad genommen wird.
	id, err := DeviceID([]string{"intf-existiert-nicht"})
	if err != nil {
		t.Fatalf("DeviceID Fallback fehlgeschlagen: %v", err)
	}
	if len(id) != 12 {
		t.Errorf("DeviceID Laenge falsch, got %d (%s)", len(id), id)
	}
	// Muss Uppercase Hex sein
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'F')) {
			t.Errorf("DeviceID enthaelt non hex: %s", id)
			break
		}
	}
	if strings.Contains(id, ":") {
		t.Errorf("DeviceID enthaelt Doppelpunkte: %s", id)
	}
}

func TestDeviceIDOhneTreffer(t *testing.T) {
	// Wenn wir zwingen koennten dass nichts gefunden wird... das geht nicht
	// portabel ohne Mock. Wir testen daher nur den happy path oben.
	t.Skip("Negativ Test braucht Mocking, ausgelassen")
}

func TestMACOfFehler(t *testing.T) {
	_, err := MACOf("nichtexistierende-interface-xyz")
	if err == nil {
		t.Error("erwartete Fehler bei nicht existierender Interface")
	}
}

func TestIPOfFehler(t *testing.T) {
	_, err := IPOf("nichtexistierende-interface-xyz")
	if err == nil {
		t.Error("erwartete Fehler bei nicht existierender Interface")
	}
}
