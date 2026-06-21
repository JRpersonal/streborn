// Package sysinfo reads system information from the running Linux kernel,
// in particular the network interfaces. On the ST10 we need the MAC of the
// wlan0 interface as the deviceID we report in the Marge responses.
package sysinfo

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// DefaultInterfaces are the interfaces, in order, that we prioritize for
// the DeviceID. wlan0 is the primary Wi-Fi interface on the ST10, eth0 a
// fallback if someone reached the box over Ethernet (there are ST
// soundbars with eth0).
var DefaultInterfaces = []string{"wlan0", "eth0", "wlan1"}

// DeviceID returns the MAC of the first interface from prefer as a 12-char
// hex string without colons. If none of the interfaces yield a MAC, it
// returns an error.
//
// Example: "a0:f6:fd:02:ff:d8" becomes "DEVICEID_PLACEHOLDER".
func DeviceID(prefer []string) (string, error) {
	if prefer == nil {
		prefer = DefaultInterfaces
	}
	for _, name := range prefer {
		mac, err := MACOf(name)
		if err != nil {
			continue
		}
		if mac == "" {
			continue
		}
		return strings.ToUpper(strings.ReplaceAll(mac, ":", "")), nil
	}
	// Fallback: walk all interfaces and take the first non-zero MAC
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("read interfaces: %w", err)
	}
	for _, iface := range ifaces {
		if len(iface.HardwareAddr) == 0 {
			continue
		}
		// Skip loopback
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		mac := iface.HardwareAddr.String()
		if mac == "" || mac == "00:00:00:00:00:00" {
			continue
		}
		return strings.ToUpper(strings.ReplaceAll(mac, ":", "")), nil
	}
	return "", errors.New("no MAC address found")
}

// MACOf returns the MAC of the named interface as lowercase with colons.
// Empty string if the interface exists but has no MAC (rare).
func MACOf(name string) (string, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return "", err
	}
	if len(iface.HardwareAddr) == 0 {
		return "", nil
	}
	return iface.HardwareAddr.String(), nil
}

// IPOf returns the first global IPv4 of the interface, or an empty string.
func IPOf(name string) (string, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return "", err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil {
			continue
		}
		if ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
			continue
		}
		return ip4.String(), nil
	}
	return "", nil
}
