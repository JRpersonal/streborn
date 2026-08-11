package webui

import (
	"net"
	"testing"
)

// The gate on the update endpoints answers one question: is this request coming
// from the speaker's own network. It used to answer a different one, "is the
// address in an RFC1918 range", and those two are not the same. A Wave owner
// whose home network is numbered 192.210.1.0/24 (public space used as a LAN)
// could not update at all: the app was on the same wire and every push came
// back "update only allowed from LAN" (2026-08-11).

func TestLocalLANAcceptsTheUsualPrivateNetworks(t *testing.T) {
	for _, addr := range []string{
		"192.168.1.20:51000",
		"10.0.0.5:1",
		"172.16.9.9:1",
		"127.0.0.1:1",
		"[::1]:1",
		"localhost",
		"169.254.3.4:1", // direct cable, no DHCP
	} {
		if !isLocalLAN(addr) {
			t.Errorf("isLocalLAN(%q) = false, want true", addr)
		}
	}
}

func TestLocalLANRejectsTheOpenInternet(t *testing.T) {
	for _, addr := range []string{
		"8.8.8.8:1",
		"203.0.113.7:40000",
		"not-an-ip:1",
	} {
		if isLocalLAN(addr) {
			t.Errorf("isLocalLAN(%q) = true, want false", addr)
		}
	}
}

// The repair itself: an address outside RFC1918 counts as local when it shares a
// subnet with one of this machine's own interfaces. Asserted against whatever
// address this machine actually has, so it exercises the real lookup rather
// than a stubbed one.
func TestLocalLANAcceptsTheSpeakersOwnSubnet(t *testing.T) {
	var probe net.IP
	var network *net.IPNet
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skip("no interfaces to test against")
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil || n.IP.IsLoopback() {
				continue
			}
			if ones, bits := n.Mask.Size(); ones == bits || bits-ones < 2 {
				continue
			}
			network = n
			break
		}
		if network != nil {
			break
		}
	}
	if network == nil {
		t.Skip("no routable IPv4 interface with a real subnet on this host")
	}
	// A neighbour address inside the same network, different from the host's own.
	probe = append(net.IP(nil), network.IP.To4()...)
	probe[3] ^= 1
	if !network.Contains(probe) {
		t.Skip("could not derive a neighbour address in " + network.String())
	}
	if !sameSubnetAsAnInterface(probe) {
		t.Errorf("sameSubnetAsAnInterface(%s) = false for a neighbour in %s", probe, network)
	}
	if !isLocalLAN(probe.String() + ":51000") {
		t.Errorf("isLocalLAN rejected %s, a neighbour on this machine's own subnet", probe)
	}
}

// A host route carries no neighbours, so it must not be read as "everything
// here is local".
func TestHostRouteIsNotASubnet(t *testing.T) {
	if sameSubnetAsAnInterface(net.ParseIP("198.51.100.9")) {
		t.Error("an address on no interface network was accepted")
	}
}
