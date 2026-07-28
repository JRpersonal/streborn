package upnp

import (
	"testing"
	"time"
)

func TestParseClock(t *testing.T) {
	cases := map[string]time.Duration{
		"0:03:27":         3*time.Minute + 27*time.Second,
		"1:02:03":         time.Hour + 2*time.Minute + 3*time.Second,
		"03:27":           3*time.Minute + 27*time.Second,
		"0:00:00":         0,
		"00:00:12.500":    12 * time.Second,
		"NOT_IMPLEMENTED": 0, // the firmware's answer for a field it has no value for
		"":                0,
		"garbage:in:here": 0,
		"0:03":            3 * time.Second,
		"1:2:3:4":         0,
	}
	for in, want := range cases {
		if got := parseClock(in); got != want {
			t.Errorf("parseClock(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestInnerText(t *testing.T) {
	doc := `<s:Envelope><s:Body><u:GetPositionInfoResponse><Track>1</Track>` +
		`<TrackDuration>0:04:11</TrackDuration><RelTime>0:01:07</RelTime>` +
		`</u:GetPositionInfoResponse></s:Body></s:Envelope>`
	if got := innerText(doc, "RelTime"); got != "0:01:07" {
		t.Errorf("RelTime = %q", got)
	}
	if got := innerText(doc, "TrackDuration"); got != "0:04:11" {
		t.Errorf("TrackDuration = %q", got)
	}
	if got := innerText(doc, "Missing"); got != "" {
		t.Errorf("a missing tag must be empty, got %q", got)
	}
}
