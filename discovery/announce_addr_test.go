package discovery

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/grandcat/zeroconf"
)

// fakeNet stands in for the machine's interfaces and for zeroconf, so these
// tests never register anything on the real network.
type fakeNet struct {
	ifaces    []net.Interface
	addrs     []string
	registers int
}

func withFakeNet(t *testing.T) *fakeNet {
	t.Helper()
	f := &fakeNet{}
	oldIfaces, oldAddrs, oldReg := registerIfaces, currentLANAddrs, registerService
	registerIfaces = func(*slog.Logger) []net.Interface { return f.ifaces }
	currentLANAddrs = func() []string { return f.addrs }
	registerService = func(*Announcer, string, []string, []net.Interface) (*zeroconf.Server, error) {
		f.registers++
		return nil, nil
	}
	t.Cleanup(func() { registerIfaces, currentLANAddrs, registerService = oldIfaces, oldAddrs, oldReg })
	return f
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// A speaker whose agent starts before its Wi-Fi lease has nothing to announce
// on. It used to register on every interface anyway, which published only the
// USB gadget's address, and the desktop app showed that as a third speaker at
// 192.0.2.1 (a SoundTouch 30, 2026-10-19). Now it waits.
func TestAnnounceWithoutLANAddressPostpones(t *testing.T) {
	f := withFakeNet(t)

	a, err := Announce(quietLogger(), Config{DeviceID: "AABBCCDDEEFF"})
	if err != nil {
		t.Fatalf("Announce: %v", err)
	}
	if f.registers != 0 {
		t.Errorf("registered %d service(s) with no LAN address, want none", f.registers)
	}
	if a.addrs != nil {
		t.Errorf("addrs = %v, want nil while nothing is registered", a.addrs)
	}
}

// The first lease after such a boot has to reach the network: nothing else
// re-announces on its own, so without this the speaker stayed unannounced (or
// wrongly announced) until the next rename or reboot.
func TestAddressAppearingRegistersOnce(t *testing.T) {
	f := withFakeNet(t)
	a, _ := Announce(quietLogger(), Config{DeviceID: "AABBCCDDEEFF"})

	f.ifaces = []net.Interface{{Name: "wlan0"}}
	f.addrs = []string{"192.168.1.29"}
	cur, changed := a.checkAddresses()
	if !changed || len(cur) != 1 || cur[0] != "192.168.1.29" {
		t.Fatalf("checkAddresses = %v, %v; want a re-announce with the new address", cur, changed)
	}
	if f.registers != 2 { // current and legacy service type
		t.Errorf("registers = %d, want 2 (both service types, once)", f.registers)
	}

	// The same address on the next tick changes nothing.
	if _, changed := a.checkAddresses(); changed || f.registers != 2 {
		t.Errorf("unchanged address re-announced (changed=%v, registers=%d)", changed, f.registers)
	}
}

// Losing the address for a moment (a renew, a roam) must not tear the
// registration down: announcing nothing is worse than a stale record.
func TestAddressGoneKeepsRegistration(t *testing.T) {
	f := withFakeNet(t)
	f.ifaces = []net.Interface{{Name: "wlan0"}}
	f.addrs = []string{"192.168.1.29"}
	a, _ := Announce(quietLogger(), Config{DeviceID: "AABBCCDDEEFF"})
	before := f.registers

	f.addrs = nil
	if _, changed := a.checkAddresses(); changed || f.registers != before {
		t.Errorf("no address re-announced (changed=%v, registers %d -> %d)", changed, before, f.registers)
	}
	if len(a.addrs) != 1 {
		t.Errorf("addrs = %v, want the registered address kept", a.addrs)
	}
}

// A DHCP renumbering is the same case as the first lease: new address, one
// re-announce.
func TestAddressChangeReannounces(t *testing.T) {
	f := withFakeNet(t)
	f.ifaces = []net.Interface{{Name: "wlan0"}}
	f.addrs = []string{"192.168.1.29"}
	a, _ := Announce(quietLogger(), Config{DeviceID: "AABBCCDDEEFF"})
	before := f.registers

	f.addrs = []string{"192.168.1.44"}
	if _, changed := a.checkAddresses(); !changed || f.registers != before+2 {
		t.Errorf("renumbering not re-announced (changed=%v, registers %d -> %d)", changed, before, f.registers)
	}
}
