package webui

import "testing"

// A cabled wpa speaker keeps its radio down while the cable carries it, so a
// live switch there can only fail, and its M3 restart broke NetManager's
// control socket on the way (#1307). The mode decision is what keeps that
// path from running at all.
func TestWLANSwitchModeFor(t *testing.T) {
	cases := []struct {
		name, mech, wired, want string
	}{
		{"wifi speaker switches live", "wpa", "", wlanSwitchLive},
		{"cabled wpa speaker only stores", "wpa", "eth0", wlanSwitchStoreWired},
		{"BCO speaker reboots", "bco", "", wlanSwitchReboot},
		// The BCO chip IS eth0; whatever the uplink probe said, that chassis
		// keeps its reboot path.
		{"BCO is never treated as cabled", "bco", "eth0", wlanSwitchReboot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wlanSwitchModeFor(tc.mech, tc.wired); got != tc.want {
				t.Errorf("wlanSwitchModeFor(%q, %q) = %q, want %q", tc.mech, tc.wired, got, tc.want)
			}
		})
	}
}

// The app keys its "unplug the cable" message on wiredUplink, and it must
// never be told a forget will run on a switch that cannot be confirmed.
func TestWLANSwitchResponse(t *testing.T) {
	r := wlanSwitchResponse(wlanSwitchStoreWired, "wpa", "HomeNet", true)
	if r["status"] != "stored" || r["wiredUplink"] != true {
		t.Errorf("cabled response = %v, want status=stored wiredUplink=true", r)
	}
	if r["forgetOthers"] != false {
		t.Errorf("cabled response promised a forget: %v", r)
	}

	r = wlanSwitchResponse(wlanSwitchLive, "wpa", "HomeNet", true)
	if r["status"] != "switching" || r["wiredUplink"] != false || r["forgetOthers"] != true {
		t.Errorf("live response = %v, want switching, not wired, forget on", r)
	}

	r = wlanSwitchResponse(wlanSwitchReboot, "bco", "HomeNet", true)
	if r["status"] != "rebooting" || r["wiredUplink"] != false || r["forgetOthers"] != false {
		t.Errorf("BCO response = %v, want rebooting, not wired, no forget", r)
	}
}
