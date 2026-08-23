package webui

import "testing"

// The speaker's own /api/debug/state is what the phone remote's "Save
// diagnostic file" button downloads verbatim, and that file gets mailed in and
// attached to public issues. A user's household network name shipped that way
// on 2026-08-22.

func TestRedactedDropsNetworkNamesAndAddresses(t *testing.T) {
	in := wlanConfigured{
		Tool:      "BoseApp-Persistence",
		Interface: "wlan0",
		Networks: []wlanNetwork{
			{ID: 0, SSID: "FRITZ!Box 6690 XH", BSSID: "any", Flags: "[CURRENT]", Current: true},
			{ID: 1, SSID: "Guest", BSSID: "AA:BB:CC:DD:EE:FF"},
		},
		FileBlocks: 2,
	}

	got := in.redacted()

	for i, n := range got.Networks {
		if n.SSID != wlanRedacted {
			t.Errorf("network %d ssid = %q, want %q", i, n.SSID, wlanRedacted)
		}
	}
	// "any" is wpa_supplicant's wildcard, not an address: losing it would hide
	// that no BSSID is pinned.
	if got.Networks[0].BSSID != "any" {
		t.Errorf("wildcard bssid = %q, want it kept", got.Networks[0].BSSID)
	}
	if got.Networks[1].BSSID != wlanRedacted {
		t.Errorf("real bssid = %q, want %q", got.Networks[1].BSSID, wlanRedacted)
	}

	// Everything a diagnosis actually reads must survive.
	if got.Tool != in.Tool || got.Interface != in.Interface || got.FileBlocks != in.FileBlocks {
		t.Errorf("redacted() lost diagnostic context: %+v", got)
	}
	if len(got.Networks) != len(in.Networks) {
		t.Fatalf("network count = %d, want %d", len(got.Networks), len(in.Networks))
	}
	if !got.Networks[0].Current || got.Networks[0].Flags != "[CURRENT]" || got.Networks[1].ID != 1 {
		t.Errorf("redacted() lost per-network context: %+v", got.Networks)
	}
}

func TestRedactedLeavesTheCallerUntouched(t *testing.T) {
	// Value receiver plus a fresh slice: the source struct must keep its real
	// data, because only the diagnostic path is supposed to lose it.
	in := wlanConfigured{Networks: []wlanNetwork{{SSID: "Home", BSSID: "AA:BB:CC:DD:EE:FF"}}}

	_ = in.redacted()

	if in.Networks[0].SSID != "Home" || in.Networks[0].BSSID != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("redacted() mutated its receiver: %+v", in.Networks[0])
	}
}

func TestRedactedHandlesAnEmptyPicture(t *testing.T) {
	// A chassis with no wpa_cli and no stored profiles reports no networks at
	// all, and that is itself the answer for that box.
	got := wlanConfigured{Tool: "", Err: "no wpa_cli and no stored profile files on this speaker"}.redacted()
	if len(got.Networks) != 0 || got.Err == "" {
		t.Errorf("empty picture mangled: %+v", got)
	}
}
