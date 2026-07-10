package webui

import "testing"

// A preset must never store the agent's own /stream/<n> proxy location as its
// stream URL (#252): that is the box-visible address, and saving it clobbers
// the station's origin URL for good.
func TestSelfProxySlot(t *testing.T) {
	cases := []struct {
		url  string
		slot int
		self bool
	}{
		{"http://127.0.0.1:8888/stream/4", 4, true},
		{"http://192.0.2.7:17008/stream/1", 1, true},
		{"http://192.0.2.7:8888/stream/6", 6, true},
		{"http://localhost/stream/2", 2, true},
		// Real origins must never match.
		{"http://strm112.1.fm/x_mobile_mp3", 0, false},
		{"https://example.com/stream/3", 0, false}, // path matches but neither port nor loopback
		{"http://192.0.2.7:8888/stream/raw?u=abc", 0, false},
		{"http://192.0.2.7:8888/stream/7", 0, false},
		{"not a url at all://", 0, false},
	}
	for _, c := range cases {
		slot, self := selfProxySlot(c.url)
		if self != c.self || slot != c.slot {
			t.Errorf("selfProxySlot(%q) = (%d,%v), want (%d,%v)", c.url, slot, self, c.slot, c.self)
		}
	}
}
