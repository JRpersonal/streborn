package webui

// What the settings screen shows as the speaker's network.
//
// The rule is that a network NAME only appears as "the network the speaker is
// on" when something live said so: the firmware's own /networkInfo, or
// wpa_supplicant's current association. Until 2026-10-21 the coprocessor
// chassis (scm ST20, Portable), whose /networkInfo carries no name, got one
// filled in from STR's own records instead: wlan-creds, else the first stored
// profile. Those describe what STR last set, not where the speaker is, and
// they went stale the moment an owner moved the speaker through Bose's setup
// page. A support mail showed the result: the screen named the network the
// owner had switched off, with the IP of the new one and a signal grade
// underneath, while the speaker was in fact on a network cable.
//
// STR's own network still travels, as STRSSID, so the app can say "last set up
// through ST Reborn" without presenting it as the live connection.

import (
	"strings"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/wlanlive"
)

// interfaceIsUp reports a connected interface by its state alone, with the
// same care as interfaceIsAssociated: NETWORK_WIFI_DISCONNECTED contains
// "CONNECTED". Unlike interfaceIsAssociated it does not require a name,
// because the coprocessor chassis report a connected interface without one.
func interfaceIsUp(state string) bool {
	st := strings.ToUpper(strings.TrimSpace(state))
	if strings.Contains(st, "DISCONNECT") || strings.Contains(st, "NOT_CONNECTED") {
		return false
	}
	return strings.HasSuffix(st, "_CONNECTED")
}

// annotateNetworkForDisplay fills the gaps in /networkInfo that the agent can
// fill honestly, in place.
//
//   - live (wpa chassis only, nil elsewhere) is wpa_supplicant's current
//     association. It wins over /networkInfo on the Wi-Fi interfaces, because
//     STR switches networks behind the firmware's back and /networkInfo then
//     keeps naming the old one.
//   - gabboSignal is the firmware's own signal class from the gabbo stream,
//     already expired after 15 minutes by the client. It is the only signal
//     a coprocessor chassis reports, and it is a measurement, so it fills an
//     empty signal on a connected interface.
//   - strSSID is the network STR last set for this speaker. It never becomes
//     SSID; it goes to STRSSID on the connected interface that has no name.
func annotateNetworkForDisplay(ifaces []boxapi.NetworkInterface, live *wlanlive.Status, gabboSignal, strSSID string) {
	for i := range ifaces {
		ni := &ifaces[i]
		fromLive := false
		if live != nil && ni.Type == "WIFI_INTERFACE" {
			if live.SSID != "" {
				ni.SSID = live.SSID
				fromLive = true
			}
			if live.FrequencyKHz != 0 {
				ni.Frequency = live.FrequencyKHz
			}
			if live.Signal != "" {
				ni.Signal = live.Signal
			}
		}
		switch {
		case ni.SSID == "":
			ni.SSIDSource = ""
		case fromLive:
			ni.SSIDSource = "live"
		default:
			ni.SSIDSource = "box"
		}
		if !interfaceIsUp(ni.State) {
			continue
		}
		if ni.Signal == "" && gabboSignal != "" {
			ni.Signal = gabboSignal
		}
		if ni.SSID == "" && strSSID != "" {
			ni.STRSSID = strSSID
		}
	}
}
