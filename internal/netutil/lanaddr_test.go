package netutil

import (
	"net"
	"testing"
)

// The address that started this: every one of these speakers has a USB gadget
// holding 203.0.113.1, and Go reports it as a perfectly ordinary routable
// address. Code that trusted that handed the Spotify engine the USB port.
func TestTheUSBGadgetAddressIsNotALANAddress(t *testing.T) {
	if !IsGadgetIPv4(net.ParseIP("203.0.113.1")) {
		t.Error("203.0.113.1 is the speakers' USB gadget and must be recognised")
	}
	if UsableLANIPv4(net.ParseIP("203.0.113.1")) {
		t.Error("the USB gadget address was accepted as a LAN address")
	}
	// Go itself considers it entirely normal, which is the whole trap.
	if !net.ParseIP("203.0.113.1").IsGlobalUnicast() {
		t.Error("premise changed: 203.0.113.1 is no longer global unicast to Go")
	}
}

func TestRealAddressesAreAccepted(t *testing.T) {
	for _, s := range []string{"192.168.178.44", "10.0.0.247", "172.16.5.9"} {
		if !UsableLANIPv4(net.ParseIP(s)) {
			t.Errorf("%s should be usable", s)
		}
	}
}

func TestTheUnreachableOnesAreRefused(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "169.254.3.4", "0.0.0.0", "192.0.2.7", "198.51.100.9"} {
		if UsableLANIPv4(net.ParseIP(s)) {
			t.Errorf("%s should not be usable", s)
		}
	}
	if UsableLANIPv4(net.ParseIP("2001:db8::1")) {
		t.Error("an IPv6 address is not an IPv4 LAN address")
	}
}

func TestTheGadgetIsAlsoRecognisedByName(t *testing.T) {
	for _, n := range []string{"usb0", "usb1", "rndis0"} {
		if !IsGadgetIface(n) {
			t.Errorf("%s is a gadget interface", n)
		}
	}
	for _, n := range []string{"eth0", "wlan0", "br0"} {
		if IsGadgetIface(n) {
			t.Errorf("%s is a real interface", n)
		}
	}
}
