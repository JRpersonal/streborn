package upnp

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// The distinction PositionInfo cannot express: "the box is at zero" and "the
// box has no value for this field" both parse to a zero duration, and only the
// first one is a position.
func TestParseClockOKSeparatesAZeroFromAnUnreadableField(t *testing.T) {
	cases := map[string]bool{
		"0:00:00":         true,
		"0:03:27":         true,
		"00:00":           true,
		"NOT_IMPLEMENTED": false, // the firmware's answer for a field it has no value for
		"":                false,
		"garbage:in:here": false,
		"1:2:3:4":         false,
	}
	for in, wantOK := range cases {
		if _, ok := parseClockOK(in); ok != wantOK {
			t.Errorf("parseClockOK(%q) ok = %v, want %v", in, ok, wantOK)
		}
	}
}

// RelPosition reports readability, not call success: a box that ANSWERS with a
// field it has no value for must come back ok=false, so a caller comparing
// positions skips instead of recording a zero as "played nothing at all".
func TestRelPositionReportsReadability(t *testing.T) {
	cases := []struct {
		name, rel string
		wantDur   time.Duration
		wantOK    bool
	}{
		{"a real position", "0:01:07", time.Minute + 7*time.Second, true},
		{"the start of a track", "0:00:00", 0, true},
		{"a field the firmware has no value for", "NOT_IMPLEMENTED", 0, false},
		{"no field at all", "", 0, false},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			body := `<s:Envelope><s:Body><u:GetPositionInfoResponse><TrackDuration>0:04:11</TrackDuration>`
			if c.rel != "" {
				body += `<RelTime>` + c.rel + `</RelTime>`
			}
			_, _ = w.Write([]byte(body + `</u:GetPositionInfoResponse></s:Body></s:Envelope>`))
		}))
		r := &Renderer{ControlURL: srv.URL, Client: srv.Client()}
		got, ok := r.RelPosition(context.Background())
		srv.Close()
		if got != c.wantDur || ok != c.wantOK {
			t.Errorf("%s: RelPosition = (%v, %v), want (%v, %v)", c.name, got, ok, c.wantDur, c.wantOK)
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
