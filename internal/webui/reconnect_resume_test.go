package webui

import "testing"

// The reconnect recovery must only resume when the box is stuck on OUR last
// stream. A stuck selection pointing at a different STR stream (e.g. after a
// failed Spotify preset recall) must not resurrect the old one (#ST30 preset 1
// self-started, 2026-07-10). sameStream compares by path so the master's own
// loopback URL and the box-visible :17008 form of the same stream match.
func TestSameStream(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		// Same stream, different host/port forms -> match.
		{"http://127.0.0.1:8888/stream/2", "http://192.0.2.9:17008/stream/2", true},
		{"http://127.0.0.1:8888/stream/1", "http://127.0.0.1:8888/stream/1", true},
		// Different slot -> no match (the ST30 case: stuck on a spotify/other
		// selection while lastPlay was /stream/1).
		{"http://127.0.0.1:8888/stream/1", "http://127.0.0.1:8888/stream/4", false},
		{"http://127.0.0.1:8888/spotify/stream.ogg", "http://127.0.0.1:8888/stream/1", false},
		// Empty / unparseable -> no match (never claim sameness on no data).
		{"", "http://127.0.0.1:8888/stream/1", false},
		{"http://127.0.0.1:8888/stream/1", "", false},
	}
	for _, c := range cases {
		if got := sameStream(c.a, c.b); got != c.want {
			t.Errorf("sameStream(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// A bare INVALID_SOURCE (no location) is the deep-standby wake signature: the
// box rebooted, the wake frame beat the agent's first gabbo connect, and the
// reconnect recovery is the only path that brings the last station back. That
// case must keep the power-on resume's generous window, or the overnight
// resume breaks again (#119). A selection stuck on our own stream location is
// a mid-playback drop and must stay inside the short blip window.
func TestReconnectResumeWindow(t *testing.T) {
	if got := reconnectResumeWindow(""); got != resumeMaxAge {
		t.Errorf("empty location: window = %v, want resumeMaxAge %v", got, resumeMaxAge)
	}
	if got := reconnectResumeWindow("http://192.0.2.9:17008/stream/2"); got != reconnectResumeMaxAge {
		t.Errorf("own-stream location: window = %v, want reconnectResumeMaxAge %v", got, reconnectResumeMaxAge)
	}
}
