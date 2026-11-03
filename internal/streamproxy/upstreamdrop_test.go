package streamproxy

import (
	"testing"
	"time"
)

// A station that was playing and broke off mid-stream is handed on, so the
// agent can look at the speaker's Wi-Fi signal at that moment (webui
// weakwifidropout.go). A clean EOF (a CDN token running out) and a connection
// that never delivered anything are not dropouts the owner heard.
func TestOnlyAPlayingStationBreakingOffIsAnUpstreamDrop(t *testing.T) {
	s := healthTestServer(t)
	drops := 0
	s.SetOnUpstreamDrop(func() { drops++ })
	const station = "http://radio.example.org/live"
	s.noteHealthStart(station)

	s.noteReconnect(station, "read-fail", 0, time.Second, 0)
	if drops != 0 {
		t.Fatalf("a station that never delivered counted as a dropout (%d)", drops)
	}
	s.noteReconnect(station, "eof", 4096, time.Minute, 0)
	if drops != 0 {
		t.Fatalf("a clean EOF counted as a dropout (%d)", drops)
	}
	s.noteReconnect(station, "read-fail", 4096, time.Minute, 2*time.Second)
	if drops != 1 {
		t.Fatalf("a mid-stream break was not handed on (%d)", drops)
	}
}

// Without a callback wired (tests, an agent built without the webui) the
// reconnect bookkeeping still runs.
func TestAnUnwiredUpstreamDropIsHarmless(t *testing.T) {
	s := healthTestServer(t)
	s.noteHealthStart("http://radio.example.org/live")
	s.noteReconnect("http://radio.example.org/live", "read-fail", 4096, time.Minute, 0)
}
