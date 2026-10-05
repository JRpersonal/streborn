package streamproxy

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// A live station that plays without a single upstream drop must still report
// that audio arrived. deliveredAny used to be set only when a connection ENDED
// on an upstream drop, so a Wave bundle on 2026-10-05 showed everDelivered=false
// and "nothing has arrived from the station yet" next to a proxy log line that
// had fed the box 12.9 MB at 194 kbps.
func TestALiveStreamWithoutADropReportsDelivery(t *testing.T) {
	ResetDeliveryForTest()
	t.Cleanup(ResetDeliveryForTest)

	chunk := make([]byte, 4096)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if f != nil {
				f.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}))
	defer up.Close()

	s := New(presets.New(), silentLogger())
	s.client = &http.Client{} // bypass the SSRF guard for loopback

	mux := http.NewServeMux()
	s.Register(mux)
	proxy := httptest.NewServer(mux)
	defer proxy.Close()

	resp, err := http.Get(proxy.URL + "/stream/raw?u=" + base64.RawURLEncoding.EncodeToString([]byte(up.URL)))
	if err != nil {
		t.Fatalf("GET via proxy: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadFull(resp.Body, make([]byte, 8192)); err != nil {
		t.Fatalf("reading through the proxy: %v", err)
	}

	// The connection is still open: nothing has dropped, nothing reconnected.
	got := snap(t, s)
	if got["everDelivered"] != true {
		t.Fatalf("everDelivered = %v on a stream that is playing right now, want true (snapshot %v)", got["everDelivered"], got)
	}
	if _, ok := got["playingForSec"]; !ok {
		t.Error("playingForSec missing on a stream that is delivering")
	}
	if _, ok := got["note"]; ok {
		t.Errorf("the bundle still claims nothing arrived: %v", got["note"])
	}
	if since, ok := NothingDeliveredYet(); ok {
		t.Errorf("NothingDeliveredYet = %v, true on a stream that is delivering", since)
	}
}
