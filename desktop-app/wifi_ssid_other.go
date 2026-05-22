//go:build !windows && !darwin

package main

import (
	"os/exec"
	"strings"
)

// currentWifiSSID on Linux (and other Unix) tries common helpers in
// order: iwgetid -r (most distros), nmcli (NetworkManager). Returns
// "" if none of them are present or none gives an answer — the
// caller treats that as "unknown, don't warn".
func currentWifiSSID() string {
	if out, err := exec.Command("iwgetid", "-r").CombinedOutput(); err == nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			return s
		}
	}
	if out, err := exec.Command("nmcli", "-t", "-f", "active,ssid", "dev", "wifi").CombinedOutput(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
			if len(parts) == 2 && parts[0] == "yes" {
				return parts[1]
			}
		}
	}
	return ""
}
