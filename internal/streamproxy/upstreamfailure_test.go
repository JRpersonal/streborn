package streamproxy

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// A media server that answers but no longer has the file (a USB stick swapped
// on a router's media server): the proxy must report the 404 with the exact
// upstream URL, so the play queue can end that track at once instead of
// waiting out its stall timeout (webui queuebadurl.go).
func TestUpstream404IsReportedWithItsURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	s := New(presets.New(), silentLogger())
	s.client = &http.Client{} // bypass the SSRF guard for loopback
	var (
		mu    sync.Mutex
		gotU  string
		gotC  int
		calls int
	)
	s.SetOnUpstreamFailure(func(url string, code int) {
		mu.Lock()
		gotU, gotC = url, code
		calls++
		mu.Unlock()
	})
	mux := http.NewServeMux()
	s.Register(mux)
	proxy := httptest.NewServer(mux)
	defer proxy.Close()

	track := upstream.URL + "/AUDIO/DLNA-1-0/Gone.mp3"
	resp, err := http.Get(proxy.URL + "/stream/raw?u=" + base64.RawURLEncoding.EncodeToString([]byte(track)))
	if err != nil {
		t.Fatalf("GET via proxy: %v", err)
	}
	resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if calls == 0 {
		t.Fatal("the upstream 404 was not reported")
	}
	if gotU != track || gotC != http.StatusNotFound {
		t.Fatalf("reported (%q, %d), want (%q, 404)", gotU, gotC, track)
	}
}

// Without a callback the proxy records failures exactly as before.
func TestRecordFailureWithoutCallback(t *testing.T) {
	s := New(presets.New(), silentLogger())
	s.recordFailure("http://192.0.2.10/x.mp3", &upstreamStatusError{Code: http.StatusGone})
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.lastErr.code != http.StatusGone || s.lastErr.reason != "gone" {
		t.Fatalf("lastErr = %+v", s.lastErr)
	}
}
