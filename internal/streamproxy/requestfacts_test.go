package streamproxy

import (
	"net/http/httptest"
	"testing"
)

// The CineMate case these exist for: a preset recall and a search play reach
// the same streaming code, so the only place the difference can show is the
// box's own request. Each of these fields is one candidate explanation, and a
// missing one is a diagnostic that cannot answer the question.

func TestRequestFactsCarriesWhatSeparatesTheTwoPaths(t *testing.T) {
	r := httptest.NewRequest("GET", "http://127.0.0.1:8888/stream/2", nil)
	r.RemoteAddr = "127.0.0.1:41234"
	r.Header.Set("User-Agent", "BoseSoundTouch/27.0.6")
	r.Header.Set("Icy-MetaData", "1")
	r.Header.Set("Range", "bytes=0-")

	got := map[string]any{}
	facts := requestFacts(r)
	if len(facts)%2 != 0 {
		t.Fatalf("facts must be key/value pairs, got %d entries", len(facts))
	}
	for i := 0; i < len(facts); i += 2 {
		k, ok := facts[i].(string)
		if !ok {
			t.Fatalf("key at %d is not a string: %#v", i, facts[i])
		}
		got[k] = facts[i+1]
	}

	for k, want := range map[string]any{
		"from":   "127.0.0.1:41234",
		"host":   "127.0.0.1:8888",
		"proto":  "HTTP/1.1",
		"ua":     "BoseSoundTouch/27.0.6",
		"range":  "bytes=0-",
		"icyReq": "1",
	} {
		if got[k] != want {
			t.Errorf("%s = %#v, want %#v", k, got[k], want)
		}
	}
}

func TestRequestFactsSeparatesLoopbackFromTheLANAddress(t *testing.T) {
	// The strongest single candidate: the preset descriptor stored on the box
	// carries a loopback stream URL while a search play is resolved against the
	// speaker's LAN address. If those two arrive from different places, the
	// next diagnostic says so in one line.
	loop := httptest.NewRequest("GET", "http://127.0.0.1:8888/stream/2", nil)
	loop.RemoteAddr = "127.0.0.1:41234"
	lan := httptest.NewRequest("GET", "http://192.0.2.7:8888/stream/raw?u=abc", nil)
	lan.RemoteAddr = "192.0.2.7:52001"

	l, n := requestFacts(loop), requestFacts(lan)
	if l[1] == n[1] {
		t.Errorf("both requests reported from=%v, so the log cannot tell them apart", l[1])
	}
	if l[3] == n[3] {
		t.Errorf("both requests reported host=%v, so the log cannot tell the URL forms apart", l[3])
	}
}

func TestRequestFactsToleratesAMissingRequest(t *testing.T) {
	// Never let a diagnostic aid be the thing that panics a stream handler.
	if got := requestFacts(nil); got != nil {
		t.Errorf("requestFacts(nil) = %#v, want nil", got)
	}
}
