// Wi-Fi pre-flight check for the install wizard.
//
// Background (issue #60, deqw 2026-05-22): users coming from the
// Bose SoundTouch smartphone app expect the install flow to begin by
// joining the speaker's "Bose SoundTouch ..." setup network. That
// expectation is wrong for STR's stick-based install — the stick
// already carries the home Wi-Fi credentials, so the speaker joins
// the home network on its own after the first stick boot and we just
// need the host (laptop) to stay on the home Wi-Fi to find it.
//
// deqw reported the failure mode: macOS auto-rejoins the home Wi-Fi
// during the speaker reboot, the user manually switches back to the
// Bose-AP network thinking they have to, and then the wizard cannot
// reach the speaker on the LAN any more and times out.
//
// CheckCurrentWifi returns a verdict the frontend uses to show an
// explanatory banner before starting the install. It is cheap to
// call (one OS-native command, no scan) and safe to call repeatedly.

package main

import "strings"

// WifiCheck is the JSON the frontend renders. The fields are
// deliberately small and copy-ready.
type WifiCheck struct {
	// SSID currently associated to, or "" if we could not determine
	// it (Wi-Fi off, Ethernet-only host, lack of OS tools, ...).
	SSID string `json:"ssid"`
	// OnBoseSetupAP is true when SSID matches the Bose factory-AP
	// pattern. Frontend should show the explanatory banner and
	// the "switch back to home Wi-Fi" button in that case.
	OnBoseSetupAP bool `json:"onBoseSetupAP"`
	// Hint is a short user-facing string the frontend can render
	// verbatim. EN; the frontend localizes the long copy.
	Hint string `json:"hint"`
}

// CheckCurrentWifi is exposed to the Wails frontend.
func (a *App) CheckCurrentWifi() WifiCheck {
	ssid := currentWifiSSID()
	out := WifiCheck{SSID: ssid}
	if ssid != "" && looksLikeBoseSetupSSID(ssid) {
		out.OnBoseSetupAP = true
		out.Hint = "You are currently connected to the Bose speaker's setup network (\"" + ssid + "\"). " +
			"You do NOT need to be on that network — the USB stick carries your home Wi-Fi credentials " +
			"and the speaker will join your home Wi-Fi on its own after the first boot. " +
			"Please switch back to your home Wi-Fi now."
	}
	return out
}

// looksLikeBoseSetupSSID returns true for the factory-AP SSIDs
// the SoundTouch firmware advertises while unprovisioned. Pattern
// derived from live captures across ST10/20/30: prefix is
// case-insensitively "Bose ST", "Bose SoundTouch", or "SoundTouch ".
func looksLikeBoseSetupSSID(ssid string) bool {
	low := strings.ToLower(strings.TrimSpace(ssid))
	return strings.HasPrefix(low, "bose st") ||
		strings.HasPrefix(low, "bose soundtouch") ||
		strings.HasPrefix(low, "soundtouch ")
}
