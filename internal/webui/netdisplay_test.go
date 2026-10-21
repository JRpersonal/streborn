package webui

import (
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/wlanlive"
)

// The support case behind this file: a scm ST20 whose /networkInfo names no
// network. STR used to fill in the network it had set itself, long after the
// owner had moved the speaker elsewhere. It must now travel as STRSSID only.
func TestAnnotateNetwork_CoprocessorNeverShowsSTRNetworkAsLive(t *testing.T) {
	ifaces := []boxapi.NetworkInterface{{
		Name: "eth0", Type: "ETHERNET_INTERFACE", State: "NETWORK_ETHERNET_CONNECTED", IP: "192.0.2.10",
	}}
	annotateNetworkForDisplay(ifaces, nil, "", "OldNet")
	got := ifaces[0]
	if got.SSID != "" || got.SSIDSource != "" {
		t.Fatalf("STR's own network presented as the live one: ssid=%q source=%q", got.SSID, got.SSIDSource)
	}
	if got.STRSSID != "OldNet" {
		t.Fatalf("STRSSID = %q, want OldNet", got.STRSSID)
	}
	if got.Signal != "" {
		t.Fatalf("signal invented: %q", got.Signal)
	}
}

func TestAnnotateNetwork_FirmwareNamedSSIDIsBoxSourced(t *testing.T) {
	ifaces := []boxapi.NetworkInterface{{
		Name: "eth0", Type: "ETHERNET_INTERFACE", State: "NETWORK_ETHERNET_CONNECTED", SSID: "HomeNet",
	}}
	annotateNetworkForDisplay(ifaces, nil, "GOOD_SIGNAL", "OldNet")
	got := ifaces[0]
	if got.SSID != "HomeNet" || got.SSIDSource != "box" {
		t.Fatalf("ssid=%q source=%q, want HomeNet/box", got.SSID, got.SSIDSource)
	}
	if got.STRSSID != "" {
		t.Fatalf("STRSSID set although the firmware named the network: %q", got.STRSSID)
	}
	if got.Signal != "GOOD_SIGNAL" {
		t.Fatalf("gabbo signal not filled: %q", got.Signal)
	}
}

func TestAnnotateNetwork_WpaLiveWinsOverStaleNetworkInfo(t *testing.T) {
	ifaces := []boxapi.NetworkInterface{
		{Name: "wlan0", Type: "WIFI_INTERFACE", State: "NETWORK_WIFI_CONNECTED", SSID: "OldNet", Signal: "POOR_SIGNAL"},
		{Name: "wlan1", Type: "WIFI_INTERFACE", State: "NETWORK_WIFI_DISCONNECTED"},
	}
	live := &wlanlive.Status{SSID: "HomeNet", FrequencyKHz: 2412000, Signal: "EXCELLENT_SIGNAL", Associated: true}
	annotateNetworkForDisplay(ifaces, live, "", "HomeNet")
	if ifaces[0].SSID != "HomeNet" || ifaces[0].SSIDSource != "live" || ifaces[0].Signal != "EXCELLENT_SIGNAL" {
		t.Fatalf("live association did not win: %+v", ifaces[0])
	}
	if ifaces[0].STRSSID != "" {
		t.Fatalf("STRSSID on a named interface: %+v", ifaces[0])
	}
}

func TestAnnotateNetwork_DisconnectedInterfaceUntouched(t *testing.T) {
	ifaces := []boxapi.NetworkInterface{{Name: "wlan1", Type: "WIFI_INTERFACE", State: "NETWORK_WIFI_DISCONNECTED"}}
	annotateNetworkForDisplay(ifaces, nil, "GOOD_SIGNAL", "OldNet")
	if ifaces[0].Signal != "" || ifaces[0].STRSSID != "" {
		t.Fatalf("a disconnected interface got decorated: %+v", ifaces[0])
	}
}

func TestInterfaceIsUp(t *testing.T) {
	for state, want := range map[string]bool{
		"NETWORK_WIFI_CONNECTED":     true,
		"NETWORK_ETHERNET_CONNECTED": true,
		"NETWORK_WIFI_DISCONNECTED":  false,
		"NETWORK_NOT_CONNECTED":      false,
		"":                           false,
	} {
		if got := interfaceIsUp(state); got != want {
			t.Errorf("interfaceIsUp(%q) = %v, want %v", state, got, want)
		}
	}
}
