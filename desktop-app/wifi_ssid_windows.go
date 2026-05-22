//go:build windows

package main

import (
	"os/exec"
	"strings"
)

// currentWifiSSID returns the SSID the host is currently associated
// with, or "" if not associated or netsh failed. Parses
// `netsh wlan show interfaces`; works on EN and DE Windows because
// the "SSID : <name>" line format is locale-stable.
func currentWifiSSID() string {
	c := exec.Command("netsh", "wlan", "show", "interfaces")
	hideCmdWindow(c)
	out, err := c.CombinedOutput()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "SSID") && !strings.HasPrefix(l, "SSID-BSSID") && !strings.HasPrefix(l, "BSSID") {
			if i := strings.Index(l, " : "); i > 0 {
				return strings.TrimSpace(l[i+3:])
			}
		}
	}
	return ""
}
