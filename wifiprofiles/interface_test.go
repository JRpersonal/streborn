package wifiprofiles

import "testing"

// Placeholder names and MACs only.
const netshEN = `
There is 1 interface on the system:

    Name                   : Wi-Fi
    Description            : Example Wireless Adapter
    GUID                   : 00000000-0000-0000-0000-000000000000
    Physical address       : aa:bb:cc:dd:ee:ff
    Interface type         : Primary
    State                  : connected
    SSID                   : PlaceholderWLAN
    AP BSSID               : 11:22:33:44:55:66
    Band                   : 2.4 GHz
    Network type           : Infrastructure
    Authentication         : WPA2-Personal

    Hosted network status  : Not available
`

// A German Windows: every key is translated except SSID and BSSID, and the
// BSSID line comes before the physical address here on purpose.
const netshDE = `
Es ist 2 Schnittstelle auf dem System vorhanden:

    Name                   : WLAN 2
    Beschreibung           : Example USB Adapter
    GUID                   : 00000000-0000-0000-0000-000000000001
    Physische Adresse      : 02:00:00:00:00:01
    Status                 : Getrennt

    Name                   : WLAN
    Beschreibung           : Example Wireless Adapter
    GUID                   : 00000000-0000-0000-0000-000000000002
    BSSID                  : 11:22:33:44:55:66
    Physische Adresse      : AA:BB:CC:DD:EE:01
    Status                 : Verbunden
    SSID                   : TestNet-A
`

func TestParseNetshConnectedInterface(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want Interface
		ok   bool
	}{
		{"english", netshEN, Interface{SSID: "PlaceholderWLAN", Name: "Wi-Fi", MAC: "aa:bb:cc:dd:ee:ff"}, true},
		{"german, second adapter connected", netshDE, Interface{SSID: "TestNet-A", Name: "WLAN", MAC: "aa:bb:cc:dd:ee:01"}, true},
		{"not connected", "There is 1 interface on the system:\n\n    Name : Wi-Fi\n    State : disconnected\n", Interface{}, false},
		{"no adapter", "There is no wireless interface on the system.\n", Interface{}, false},
		{"empty", "", Interface{}, false},
	}
	for _, c := range cases {
		got, ok := parseNetshConnectedInterface(c.out)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got %+v %v, want %+v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestParseNmcliWifiDevice(t *testing.T) {
	out := "enp3s0:ethernet:connected:Wired connection 1\n" +
		"wlp2s0:wifi:connected:TestNet-A\n" +
		"p2p-dev-wlp2s0:wifi-p2p:disconnected:\n" +
		"lo:loopback:connected (externally):lo\n"
	got, ok := parseNmcliWifiDevice(out)
	if !ok || got.Name != "wlp2s0" || got.SSID != "TestNet-A" {
		t.Errorf("got %+v %v", got, ok)
	}
	if _, ok := parseNmcliWifiDevice("wlp2s0:wifi:disconnected:\nenp3s0:ethernet:connected:Wired\n"); ok {
		t.Error("a disconnected Wi-Fi device was reported as connected")
	}
}
