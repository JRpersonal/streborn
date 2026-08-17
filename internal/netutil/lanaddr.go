package netutil

import (
	"net"
	"strings"
)

// The knowledge in this file used to live in five separate places and was
// missing from two of them, which is how a SoundTouch 30 nearly ended up
// announcing itself at an address no client can reach.
//
// Every one of these speakers carries a USB gadget interface. It is up, it is
// multicast capable, and it holds 192.0.2.x style documentation space:
// 203.0.113.1, from the range reserved for examples. Go reports that as an
// ordinary global unicast address, so the obvious test, "the first interface
// that is up and has a routable IPv4", can hand back the USB port. Whatever is
// pinned to it then talks where nobody is listening.
//
// The desktop app carries its own copy of this rule because it is a separate
// module and cannot import this one. Keep the two in step: see
// desktop-app/app_boxops.go.

// gadgetPrefixes are the documentation ranges the speakers' USB gadget uses.
// TEST-NET-3 is what the current firmware assigns; the other two are here
// because they are reserved for the same purpose and cost nothing to exclude.
var gadgetPrefixes = []string{"203.0.113.", "192.0.2.", "198.51.100."}

// IsGadgetIPv4 reports whether ip belongs to the speaker's USB gadget rather
// than to a real network.
func IsGadgetIPv4(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	s := v4.String()
	for _, p := range gadgetPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// IsGadgetIface reports whether the interface is the speaker's USB gadget, by
// name. Checked alongside the address because a gadget can be brought up
// before it has an address, and because the name is the cheaper test.
func IsGadgetIface(name string) bool {
	return strings.HasPrefix(name, "usb") || strings.HasPrefix(name, "rndis")
}

// UsableLANIPv4 reports whether this address is one the speaker can actually be
// reached at from the home network: a real IPv4, not loopback, not link local,
// and not the USB gadget.
func UsableLANIPv4(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	if v4.IsLoopback() || v4.IsLinkLocalUnicast() || v4.IsLinkLocalMulticast() || v4.IsUnspecified() {
		return false
	}
	return !IsGadgetIPv4(v4)
}

// LANIfaceAddrs returns every (interface, address) pair the speaker can be
// reached at from the home network, in the order the system reports them.
// Callers that need exactly one should take the first; callers that announce on
// several should use them all.
func LANIfaceAddrs(requireMulticast bool) []IfaceAddr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []IfaceAddr
	for i := range ifaces {
		in := &ifaces[i]
		if in.Flags&net.FlagUp == 0 || in.Flags&net.FlagLoopback != 0 {
			continue
		}
		if requireMulticast && in.Flags&net.FlagMulticast == 0 {
			continue
		}
		if IsGadgetIface(in.Name) {
			continue
		}
		addrs, err := in.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if !UsableLANIPv4(n.IP) {
				continue
			}
			out = append(out, IfaceAddr{Iface: in, IP: n.IP.To4()})
		}
	}
	return out
}

// IfaceAddr pairs an interface with one of its usable IPv4 addresses.
type IfaceAddr struct {
	Iface *net.Interface
	IP    net.IP
}

// FirstLANIPv4 returns the address the speaker is reachable at, or nil.
func FirstLANIPv4() net.IP {
	for _, c := range LANIfaceAddrs(false) {
		return c.IP
	}
	return nil
}
