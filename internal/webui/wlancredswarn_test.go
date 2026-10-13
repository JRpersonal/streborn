package webui

import "testing"

func TestWlanCredsWarningFor(t *testing.T) {
	none := func() string { return "" }
	home := func() string { return "HomeNet" }
	cases := []struct {
		mode string
		ssid func() string
		want bool
	}{
		{"bco", home, true},         // scm bridge: the offline rescue needs STR's copy
		{"taigan-bco", home, false}, // Portable with its own profile: no alarm
		{"taigan-bco", none, true},  // Portable that keeps nothing: warn
		{"wlan0", none, false},      // wpa class keeps its own profiles
		{"ethernet-only", none, false},
		{"", none, false},
	}
	for _, c := range cases {
		if got := wlanCredsWarningFor(c.mode, c.ssid); got != c.want {
			t.Errorf("%q: got %v, want %v", c.mode, got, c.want)
		}
	}
}
