package boxapi

import (
	"encoding/xml"
	"testing"
)

func TestClockTimeOffsetMinutes(t *testing.T) {
	// Answers read from a SoundTouch Portable on FW 27.0.6, 2026-11-06, with
	// the zone set as named (utcTime is 05:25:09 / 05:26:28 UTC).
	cases := []struct {
		name, body string
		want       int
	}{
		{"Europe/Berlin", `<clockTime utcTime="1793942709"><localTime year="2026" month="10" dayOfMonth="6" hour="6" minute="25" second="9" /></clockTime>`, 60},
		{"America/Vancouver, old rules", `<clockTime utcTime="1793942709"><localTime year="2026" month="10" dayOfMonth="5" hour="21" minute="25" second="9" /></clockTime>`, -480},
		{"NOT_SET -420", `<clockTime utcTime="1793942788"><localTime hour="22" minute="26" second="28" /></clockTime>`, -420},
		{"NOT_SET +330", `<clockTime utcTime="1793942788"><localTime hour="10" minute="56" second="28" /></clockTime>`, 330},
	}
	for _, c := range cases {
		var ct ClockTime
		if err := xml.Unmarshal([]byte(c.body), &ct); err != nil {
			t.Fatal(err)
		}
		got, ok := ct.OffsetMinutes()
		if !ok || got != c.want {
			t.Errorf("%s: got %d ok=%v, want %d", c.name, got, ok, c.want)
		}
	}
	if _, ok := (ClockTime{UTCTime: 1793942709}).OffsetMinutes(); ok {
		t.Error("an answer without localTime must not give an offset")
	}
}
