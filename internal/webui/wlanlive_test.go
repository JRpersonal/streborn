package webui

import (
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// The four interface shapes measured on Jens' fleet on 2026-09-30. Names are
// placeholders; the types, states and the presence or absence of each field are
// exactly what /networkInfo returned.
var (
	st10Wlan0 = boxapi.NetworkInterface{
		Name: "wlan0", Type: "WIFI_INTERFACE", State: "NETWORK_WIFI_CONNECTED",
		Mode: "STATION", SSID: "HomeNet", Frequency: 5220000, Signal: "GOOD_SIGNAL",
	}
	// The second radio is DISCONNECTED and still reports an SSID, a frequency
	// and a signal grade. This row is why liveness must be read off the state.
	st10Wlan1 = boxapi.NetworkInterface{
		Name: "wlan1", Type: "WIFI_INTERFACE", State: "NETWORK_WIFI_DISCONNECTED",
		SSID: "HomeNet", Frequency: 5220000, Signal: "GOOD_SIGNAL",
	}
	// The coprocessor chassis presents its Wi-Fi chip as Ethernet, with no
	// frequency, signal or mode, but it does fill in the network name.
	bcoEth0 = boxapi.NetworkInterface{
		Name: "eth0", Type: "ETHERNET_INTERFACE", State: "NETWORK_ETHERNET_CONNECTED",
		SSID: "HomeNet",
	}
	bcoEth0Down = boxapi.NetworkInterface{
		Name: "eth0", Type: "ETHERNET_INTERFACE", State: "NETWORK_ETHERNET_DISCONNECTED",
		SSID: "HomeNet",
	}
)

func TestInterfaceIsAssociatedRejectsDisconnected(t *testing.T) {
	// "NETWORK_WIFI_DISCONNECTED" contains "CONNECTED". A substring check here
	// reads a dead radio as the live one, and on the measured ST10 that radio
	// carried a believable SSID, frequency and signal too.
	for _, tc := range []struct {
		name string
		in   boxapi.NetworkInterface
		want bool
	}{
		{"wpa chassis, live radio", st10Wlan0, true},
		{"wpa chassis, second radio down", st10Wlan1, false},
		{"coprocessor chassis, live", bcoEth0, true},
		{"coprocessor chassis, down", bcoEth0Down, false},
		{"connected but no network name", boxapi.NetworkInterface{
			Name: "wlan0", Type: "WIFI_INTERFACE", State: "NETWORK_WIFI_CONNECTED",
		}, false},
		{"state empty", boxapi.NetworkInterface{Name: "wlan0", SSID: "HomeNet"}, false},
		{"lowercase state still understood", boxapi.NetworkInterface{
			Name: "wlan0", State: "network_wifi_connected", SSID: "HomeNet",
		}, true},
		{"NOT_CONNECTED is not connected", boxapi.NetworkInterface{
			Name: "wlan0", State: "NETWORK_WIFI_NOT_CONNECTED", SSID: "HomeNet",
		}, false},
	} {
		if got := interfaceIsAssociated(tc.in); got != tc.want {
			t.Errorf("%s: got %v, want %v (state %q)", tc.name, got, tc.want, tc.in.State)
		}
	}
}

func TestLiveAssociationPrefersTheRealRadio(t *testing.T) {
	// Both orderings, because picking the first match would pass one and fail
	// the other.
	for _, ifaces := range [][]boxapi.NetworkInterface{
		{st10Wlan0, st10Wlan1},
		{st10Wlan1, st10Wlan0},
	} {
		name, live := liveAssociation(ifaces)
		if live == nil {
			t.Fatal("no association found on a connected ST10")
		}
		if live.Interface != "wlan0" {
			t.Errorf("picked %q, want wlan0", live.Interface)
		}
		if name != "HomeNet" {
			t.Errorf("name %q, want HomeNet", name)
		}
	}
}

func TestLiveAssociationOnTheCoprocessorChassis(t *testing.T) {
	// The whole point: an ST30 or Portable reports only an Ethernet-typed
	// interface, and that is the answer, not a fallback to be distrusted.
	name, live := liveAssociation([]boxapi.NetworkInterface{bcoEth0})
	if live == nil {
		t.Fatal("no association found on a connected Portable/ST30")
	}
	if live.Interface != "eth0" || live.Type != "ETHERNET_INTERFACE" {
		t.Errorf("got %q/%q, want eth0/ETHERNET_INTERFACE", live.Interface, live.Type)
	}
	if name != "HomeNet" {
		t.Errorf("name %q, want HomeNet", name)
	}
	if live.FreqKHz != 0 || live.Signal != "" {
		t.Errorf("this chassis reports no frequency or signal; got %d/%q", live.FreqKHz, live.Signal)
	}
}

func TestLiveAssociationNeverCarriesTheNetworkName(t *testing.T) {
	// This record lands in the diagnostic that the phone remote downloads and
	// users mail in, so the name must not be in it anywhere.
	_, live := liveAssociation([]boxapi.NetworkInterface{st10Wlan0})
	if live == nil {
		t.Fatal("no association")
	}
	for _, f := range []string{live.Interface, live.Type, live.State, live.SSIDTag, live.Signal} {
		if strings.Contains(f, "HomeNet") {
			t.Errorf("the network name leaked into a published field: %q", f)
		}
	}
	if live.SSIDTag == "" || live.SSIDTag == "none" {
		t.Errorf("want a usable tag for the live network, got %q", live.SSIDTag)
	}
}

func TestLiveAssociationNothingConnected(t *testing.T) {
	for _, ifaces := range [][]boxapi.NetworkInterface{
		nil,
		{},
		{st10Wlan1},
		{bcoEth0Down},
	} {
		if name, live := liveAssociation(ifaces); live != nil || name != "" {
			t.Errorf("reported an association where there is none: %v / %q", live, name)
		}
	}
}

func TestWithLiveAssociationMarksTheStoredProfile(t *testing.T) {
	// The measured Portable/ST30 state: one stored profile, current=false, and
	// no liveness anywhere.
	in := wlanConfigured{
		Tool: "BoseApp-Persistence",
		Networks: []wlanNetwork{
			{ID: 0, SSID: "HomeNet", Flags: "AirplayConfiguration"},
		},
		Stored: []wlanNetwork{
			{ID: 0, SSID: "HomeNet", Flags: "AirplayConfiguration"},
		},
	}

	got := withLiveAssociation(in, []boxapi.NetworkInterface{bcoEth0})

	if got.Live == nil {
		t.Fatal("no live record")
	}
	if !got.Networks[0].Current {
		t.Error("the live network is still reported as not current")
	}
	if !got.Stored[0].Current {
		t.Error("the stored profile the speaker is actually on is still not current")
	}
	if !strings.Contains(got.Networks[0].Flags, "AirplayConfiguration") {
		t.Error("the origin flag was overwritten instead of extended")
	}
	if !strings.Contains(got.Networks[0].Flags, "[LIVE]") {
		t.Errorf("no live marker in the flags: %q", got.Networks[0].Flags)
	}
}

func TestWithLiveAssociationLeavesWpaCurrentAlone(t *testing.T) {
	// wpa_cli's [CURRENT] comes from the supplicant's own selection. If a name
	// match disagreed with it, overwriting would hide the disagreement, which is
	// exactly the interesting case.
	in := wlanConfigured{
		Tool:      "/usr/local/sbin/wpa_cli",
		Interface: "wlan0",
		Networks: []wlanNetwork{
			{ID: 0, SSID: "OtherNet", Flags: "[CURRENT]", Current: true},
			{ID: 1, SSID: "HomeNet"},
		},
		Stored: []wlanNetwork{{ID: 0, SSID: "HomeNet", Flags: "NetworkProfiles"}},
	}

	got := withLiveAssociation(in, []boxapi.NetworkInterface{st10Wlan0})

	if got.Networks[0].Flags != "[CURRENT]" || !got.Networks[0].Current {
		t.Errorf("the supplicant's own selection was altered: %+v", got.Networks[0])
	}
	if !got.Networks[1].Current {
		t.Error("the network the firmware says is live was not marked")
	}
	// Both marked current at once is the disagreement worth seeing, not a bug.
	if !got.Stored[0].Current {
		t.Error("the stored profile matching the live network was not marked")
	}
}

func TestWithLiveAssociationMarksEveryDuplicate(t *testing.T) {
	// The measured ST30 reported wifiProfileCount=2 against one distinct name.
	// Marking only the first would present the duplicate as a second network
	// the user does not have.
	in := wlanConfigured{
		Networks: []wlanNetwork{
			{ID: 0, SSID: "HomeNet", Flags: "NetworkProfiles"},
			{ID: 1, SSID: "HomeNet", Flags: "AirplayConfiguration"},
			{ID: 2, SSID: "Guest", Flags: "NetworkProfiles"},
		},
	}

	got := withLiveAssociation(in, []boxapi.NetworkInterface{bcoEth0})

	if !got.Networks[0].Current || !got.Networks[1].Current {
		t.Error("a duplicate of the live network was left unmarked")
	}
	if got.Networks[2].Current {
		t.Error("a network the speaker is NOT on was marked current")
	}
}

func TestWithLiveAssociationUnchangedWhenNothingIsReported(t *testing.T) {
	// A box that will not answer /networkInfo must degrade to the old picture,
	// not to a wrong one.
	in := wlanConfigured{
		Tool:     "BoseApp-Persistence",
		Networks: []wlanNetwork{{ID: 0, SSID: "HomeNet", Flags: "AirplayConfiguration"}},
		Stored:   []wlanNetwork{{ID: 0, SSID: "HomeNet", Flags: "AirplayConfiguration"}},
	}

	got := withLiveAssociation(in, nil)

	if got.Live != nil {
		t.Error("invented a live record from nothing")
	}
	if got.Networks[0].Current || got.Stored[0].Current {
		t.Error("marked something current with no evidence at all")
	}
}

func TestWithLiveAssociationDoesNotMutateItsInput(t *testing.T) {
	nets := []wlanNetwork{{ID: 0, SSID: "HomeNet", Flags: "AirplayConfiguration"}}
	in := wlanConfigured{Networks: nets, Stored: nets}

	_ = withLiveAssociation(in, []boxapi.NetworkInterface{bcoEth0})

	// Networks and Stored share a backing array here, which is exactly how the
	// coprocessor path builds them, so an in-place write would corrupt both and
	// double-append the marker.
	if nets[0].Current || nets[0].Flags != "AirplayConfiguration" {
		t.Errorf("the caller's slice was modified: %+v", nets[0])
	}
}
