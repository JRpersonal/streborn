package main

import (
	"errors"
	"testing"
)

func TestClockDisplayXML(t *testing.T) {
	for _, tc := range []struct {
		enable   bool
		tz       string
		off      int
		format24 bool
		bright   int
		want     string
	}{
		{true, "Europe/Berlin", 120, true, 30,
			`<clockDisplay><clockConfig userEnable="true" timezoneInfo="Europe/Berlin" userOffsetMinute="0" timeFormat="TIME_FORMAT_24HOUR_ID" brightnessLevel="30" /></clockDisplay>`},
		{false, "", -300, false, -1,
			`<clockDisplay><clockConfig userEnable="false" timezoneInfo="NOT_SET" userOffsetMinute="-300" timeFormat="TIME_FORMAT_12HOUR_ID" /></clockDisplay>`},
		{true, "", 0, true, 400,
			`<clockDisplay><clockConfig userEnable="true" timezoneInfo="NOT_SET" userOffsetMinute="0" timeFormat="TIME_FORMAT_24HOUR_ID" brightnessLevel="100" /></clockDisplay>`},
	} {
		if got := clockDisplayXML(tc.enable, tc.tz, tc.off, tc.format24, tc.bright); got != tc.want {
			t.Errorf("got  %s\nwant %s", got, tc.want)
		}
	}
}

func TestClientRefused(t *testing.T) {
	if !clientRefused(errors.New("status 400: <errors>CLIENT_XML_ERROR</errors>")) {
		t.Error("a 400 is the speaker refusing")
	}
	if clientRefused(errors.New("status 500: boom")) || clientRefused(errors.New("dial tcp: i/o timeout")) {
		t.Error("only a 4xx counts")
	}
}
