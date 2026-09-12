package webui

import "testing"

// A household network name reached three public diagnostic attachments through
// the Stored list, which the redactor never walked: the same name appeared as
// <REDACTED> in networks and in clear two fields below.
//
//	"networks": [{"ssid": "<REDACTED>", "ssidTag": "604c81:5", ...}]
//	"stored":   [{"ssid": "RicoI",      "ssidTag": "604c81:5", ...}]
//
// Every list of networks on this struct has to be covered, so the test asserts
// on both rather than on the one that happened to be broken.
func TestRedactedCoversEveryNetworkList(t *testing.T) {
	w := wlanConfigured{
		Tool:      "/usr/local/sbin/wpa_cli",
		Interface: "wlan0",
		Networks:  []wlanNetwork{{ID: 0, SSID: "HomeNet", BSSID: "any", Current: true, SSIDTag: "aabbcc:7"}},
		Stored:    []wlanNetwork{{ID: 0, SSID: "HomeNet", BSSID: "11:22:33:44:55:66", Flags: "NetworkProfiles", SSIDTag: "aabbcc:7"}},
	}

	got := w.redacted()

	for _, tc := range []struct {
		list string
		nets []wlanNetwork
	}{{"networks", got.Networks}, {"stored", got.Stored}} {
		for _, n := range tc.nets {
			if n.SSID != wlanRedacted {
				t.Errorf("%s: the network name survived as %q", tc.list, n.SSID)
			}
		}
	}

	// The wildcard is not an address and losing it would hide that no BSSID is
	// pinned; a real address is an address wherever it appears.
	if got.Networks[0].BSSID != "any" {
		t.Errorf("the wpa_supplicant wildcard was redacted: %q", got.Networks[0].BSSID)
	}
	if got.Stored[0].BSSID != wlanRedacted {
		t.Errorf("a hardware address survived in stored: %q", got.Stored[0].BSSID)
	}

	// Everything a diagnosis reads has to survive, or the redaction costs more
	// than it protects.
	if got.Stored[0].SSIDTag != "aabbcc:7" || got.Networks[0].SSIDTag != "aabbcc:7" {
		t.Error("the scrub-proof tag was lost, so two lists can no longer be compared")
	}
	if !got.Networks[0].Current || got.Stored[0].Flags != "NetworkProfiles" || got.Tool == "" || got.Interface == "" {
		t.Errorf("redaction removed diagnostic context: %+v", got)
	}
}

// A box with no networks at all must not turn nil into an empty list, which
// would read as "the question was asked and the answer is none" in a bundle.
func TestRedactedKeepsNilLists(t *testing.T) {
	got := wlanConfigured{Tool: "none"}.redacted()
	if got.Networks != nil || got.Stored != nil {
		t.Errorf("nil lists became %v / %v", got.Networks, got.Stored)
	}
}
