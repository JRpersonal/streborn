package boxlog

import (
	"testing"
	"time"
)

// The Wi-Fi quality line reaches the minute hook, and nothing else does.
func TestMinuteHandlerHearsTheWiFiQualityLine(t *testing.T) {
	r := New(nil, nil, nil)
	var got []time.Time
	r.SetMinuteHandler(func(at time.Time) { got = append(got, at) })

	now := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	r.InjectLine(lineWiFiQuality, now)
	r.InjectLine(lineMargeUpdate, now.Add(time.Second))
	r.InjectLine("not a syslog line", now.Add(2*time.Second))
	r.InjectLine(lineWiFiQuality, now.Add(time.Minute))

	if len(got) != 2 || !got[0].Equal(now) || !got[1].Equal(now.Add(time.Minute)) {
		t.Fatalf("minute hook calls = %v, want the two Wi-Fi quality lines", got)
	}
}

// Without a hook the line is still classified and nothing panics.
func TestMinuteHandlerUnset(t *testing.T) {
	r := New(nil, nil, nil)
	r.InjectLine(lineWiFiQuality, time.Now())
}
