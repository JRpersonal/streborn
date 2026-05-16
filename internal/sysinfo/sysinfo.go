// Package sysinfo liest System Informationen aus dem laufenden Linux Kernel,
// insbesondere die Netzwerk Interfaces. Auf der ST10 brauchen wir die MAC
// der wlan0 Interface als deviceID die wir in den Marge Antworten ausgeben.
package sysinfo

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// DefaultInterfaces sind die Interfaces in Reihenfolge die wir fuer die
// DeviceID priorisieren. wlan0 ist auf der ST10 die primary WLAN
// Schnittstelle, eth0 ein Fallback wenn jemand die Box per Ethernet
// angesprochen hat (es gibt ST Soundbars mit eth0).
var DefaultInterfaces = []string{"wlan0", "eth0", "wlan1"}

// DeviceID liefert die MAC der ersten Interface aus prefer als 12 Hex
// String ohne Doppelpunkte. Wenn keine der Interfaces eine MAC liefert,
// gibt es einen Fehler.
//
// Beispiel: aus "a0:f6:fd:02:ff:d8" wird "DEVICEID_PLACEHOLDER".
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
	// Fallback: alle Interfaces durchgehen und die erste non zero MAC nehmen
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("Interfaces lesen: %w", err)
	}
	for _, iface := range ifaces {
		if iface.HardwareAddr == nil || len(iface.HardwareAddr) == 0 {
			continue
		}
		// Loopback ueberspringen
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		mac := iface.HardwareAddr.String()
		if mac == "" || mac == "00:00:00:00:00:00" {
			continue
		}
		return strings.ToUpper(strings.ReplaceAll(mac, ":", "")), nil
	}
	return "", errors.New("keine MAC Adresse gefunden")
}

// MACOf liefert die MAC der genannten Interface als Lowercase mit Doppelpunkten.
// Leerer String wenn die Interface zwar existiert aber keine MAC hat (selten).
func MACOf(name string) (string, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return "", err
	}
	if iface.HardwareAddr == nil || len(iface.HardwareAddr) == 0 {
		return "", nil
	}
	return iface.HardwareAddr.String(), nil
}

// IPOf liefert die erste globale IPv4 der Interface oder leeren String.
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
