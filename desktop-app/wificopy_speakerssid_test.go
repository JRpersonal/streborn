package main

import "testing"

// A computer that will not name its own network (Windows 11 with location
// services off) falls back on the network the speaker keeps itself, but only
// when that network is unambiguous.
func TestSpeakerStoredSSIDOnlyWhenUnambiguous(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, body, want string
	}{
		{"one closed network in the chip", `{"current":"","networks":[{"ssid":"Home","current":false,"open":false,"sources":["chip"]}]}`, "Home"},
		{"the speaker names its current network", `{"current":"Home","networks":[{"ssid":"Home"},{"ssid":"Guest"}]}`, "Home"},
		{"one marked current among several", `{"networks":[{"ssid":"Guest"},{"ssid":"Home","current":true}]}`, "Home"},
		{"two closed networks, none current", `{"networks":[{"ssid":"Home"},{"ssid":"Office"}]}`, ""},
		{"only an open network", `{"networks":[{"ssid":"Cafe","open":true}]}`, ""},
		{"an open and a closed one", `{"networks":[{"ssid":"Cafe","open":true},{"ssid":"Home"}]}`, "Home"},
		{"nothing stored", `{"networks":[]}`, ""},
		{"not JSON (an older agent's page)", `<html></html>`, ""},
	}
	for _, c := range cases {
		if got := speakerStoredSSID([]byte(c.body)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
