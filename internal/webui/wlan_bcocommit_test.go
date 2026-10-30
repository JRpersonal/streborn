package webui

import "testing"

// The Portable's NetManager never processes addWirelessProfile, so the BCO arm
// must not spend its 20 s on it there; the scm bridge boxes keep the call.
func TestCommitBeforeBCOReboot(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want bool
	}{
		{"taigan-bco", false},
		{"bco", true},
		{"", true},
		{"wlan0", true},
	} {
		if got := commitBeforeBCOReboot(tc.mode); got != tc.want {
			t.Errorf("commitBeforeBCOReboot(%q) = %v, want %v", tc.mode, got, tc.want)
		}
	}
}
