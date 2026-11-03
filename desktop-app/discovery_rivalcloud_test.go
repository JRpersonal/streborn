package main

import "testing"

func TestRivalCloudURL(t *testing.T) {
	cases := []struct {
		in, want, why string
	}{
		{"", "", "no address"},
		{"https://streaming.bose.com", "", "Bose's own"},
		{"HTTPS://Streaming.Bose.com/", "", "Bose's own, any case"},
		{"https://streaming.bose.com:443", "", "Bose's own with a port"},
		{"http://192.0.2.13:8880", "http://192.0.2.13:8880", "AfterTouch on a NAS (field bundle 2026-11-03)"},
		{"http://content.api.bose.io.example.net:7777", "http://content.api.bose.io.example.net:7777", "a look-alike host is still a rival"},
		{" http://192.0.2.13:8880 ", "http://192.0.2.13:8880", "trimmed"},
		{"https://str-setup.invalid", "", "STR's own unlock placeholder"},
		{"http://192.0.2.5:9090/;touch /tmp/remote_services;/etc/init.d/sshd start", "", "STR's own unlock injection"},
		{"https://str-setup.invalid;touch /tmp/remote_services;/etc/init.d/sshd start #", "", "STR's own unlock injection on the placeholder host"},
		{"https://aftertouch.invalid;touch /tmp/remote_services;/etc/init.d/sshd start", "https://aftertouch.invalid", "AfterTouch's own SSH unlock left behind (field bundle 2026-11-03)"},
		{";touch /tmp/remote_services # aftertouch", "AfterTouch", "an AfterTouch injection with no address in front"},
	}
	for _, c := range cases {
		if got := rivalCloudURL(c.in); got != c.want {
			t.Errorf("%s: rivalCloudURL(%q) = %q, want %q", c.why, c.in, got, c.want)
		}
	}
}

// The merge pins: the cache never carries the reading (a healed speaker loses
// the note on the next cycle), and within a cycle a thin sighting does not
// erase what the /info probe saw.
func TestRivalCloudURLMerge(t *testing.T) {
	stock := func(u string) BoxInfo { return BoxInfo{Kind: "stock", Host: "192.0.2.45", RivalCloudURL: u} }
	if got := mergeBoxInfo(stock("http://192.0.2.13:8880"), stock("")); got.RivalCloudURL != "" {
		t.Errorf("cache carried the rival address into a cycle without it: %q", got.RivalCloudURL)
	}
	if got := mergeSameKind(stock(""), stock("http://192.0.2.13:8880")); got.RivalCloudURL != "http://192.0.2.13:8880" {
		t.Errorf("a thin sighting erased the probe's reading: %q", got.RivalCloudURL)
	}
}
