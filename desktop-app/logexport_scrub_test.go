package main

import (
	"strings"
	"testing"
)

// The scrub sees a box log as ONE multi-line blob, and truncation mid-value is
// routine there: the boot script cuts a profile dump at 300 bytes and a seed
// response at 200, and both cuts land inside an ssid="..." attribute. Go's
// negated classes match newlines, so an unterminated value used to run to the
// next quote anywhere later in the file and swallow every line in between.
// These are the shapes of the real data, not synthetic cases.
func TestScrubPII_MultiLineAndTruncatedValues(t *testing.T) {
	cases := []struct {
		name string
		in   string
		gone []string
		kept []string
	}{
		{
			"truncated attribute does not eat the following lines",
			"seed: response='<AddWirelessProfile><profile ssid=\"Home Net\n" +
				"12:00:01 seed: post-state tap='profile 1 ok' networkInfo=wifiProfileCount=\"1\"\n" +
				"12:00:02 preset 3 recalled: 'Radio Swiss Jazz'\n",
			[]string{"Home Net"},
			[]string{"post-state tap=", "Radio Swiss Jazz"},
		},
		{
			"apostrophe inside the network name",
			"wifi failover seed: seeding 'Bob's WiFi' so a later cable pull can fail over",
			[]string{"Bob", "WiFi"},
			[]string{"wifi failover seed", "cable pull"},
		},
		{
			"plain seed line keeps its context",
			"wifi failover seed: seeding 'Livebox-D7WD0_5GHz' so a later cable pull can fail over (non-destructive)",
			[]string{"Livebox"},
			[]string{"non-destructive", "cable pull"},
		},
		{
			"unterminated seed value",
			"wifi failover seed: seeding 'Livebox-D7WD0",
			[]string{"Livebox"},
			[]string{"wifi failover seed"},
		},
		{
			"attribute with spaces",
			`<profile ssid="Home Network 5G" password="hunter two" securityType="wpa_or_wpa2" />`,
			[]string{"Home Network 5G", "hunter two", "Network 5G"},
			[]string{"securityType="},
		},
		{
			"station names survive",
			`preset 3 recalled: 'Radio Swiss Jazz' via UPnP, station "BBC Radio 4" queued`,
			nil,
			[]string{"Radio Swiss Jazz", "BBC Radio 4"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scrubPII(tc.in)
			t.Log(got)
			for _, g := range tc.gone {
				if strings.Contains(got, g) {
					t.Errorf("%q leaked:\n%s", g, got)
				}
			}
			for _, k := range tc.kept {
				if !strings.Contains(got, k) {
					t.Errorf("%q was eaten:\n%s", k, got)
				}
			}
			if again := scrubPII(got); again != got {
				t.Errorf("not idempotent:\n%s\n%s", got, again)
			}
		})
	}
}
