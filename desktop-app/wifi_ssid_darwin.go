//go:build darwin

package main

import (
	"os/exec"
	"strings"
)

// currentWifiSSID returns the SSID the host is currently associated
// with, or "" on error. macOS path: prefer `networksetup
// -getairportnetwork`, which is stable across versions and does not
// require Location permission like the older `airport` binary on
// recent macOS. We probe the first few common interfaces and return
// the first answer.
//
// `networksetup -getairportnetwork enX` prints either
//   "Current Wi-Fi Network: <ssid>"
// or "You are not associated with an AirPort network."
// The first line is what we parse.
func currentWifiSSID() string {
	for _, iface := range []string{"en0", "en1", "en2"} {
		c := exec.Command("networksetup", "-getairportnetwork", iface)
		out, err := c.CombinedOutput()
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(out))
		const prefix = "Current Wi-Fi Network: "
		if idx := strings.Index(s, prefix); idx >= 0 {
			rest := s[idx+len(prefix):]
			if nl := strings.IndexAny(rest, "\r\n"); nl >= 0 {
				rest = rest[:nl]
			}
			rest = strings.TrimSpace(rest)
			if rest != "" {
				return rest
			}
		}
	}
	return ""
}
