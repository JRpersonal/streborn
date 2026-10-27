package streamproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// The second preset row (#1110) lives in slots 7..12. A double press or an app
// recall of one of them points the speaker at /stream/<slot> exactly like a
// row-1 key, so the proxy must serve the slot and stamp its fetch signals, or
// the recall verify would read a playing row-2 station as a failed recall.
func TestProxyServesTheSecondRow(t *testing.T) {
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

	store := presets.New()
	// A pathless test store keeps the slot in memory and only reports that it
	// could not persist it.
	_ = store.SetSlot(presets.Preset{Slot: 12, Name: "Row two", StreamURL: up.URL, Type: "radio"})
	s := New(store, silentLogger())
	s.client = &http.Client{} // bypass the SSRF guard for loopback

	mux := http.NewServeMux()
	s.Register(mux)
	proxy := httptest.NewServer(mux)
	defer proxy.Close()

	// One millisecond back: on Windows two time.Now() calls can return the same
	// instant, and SlotPulledSince wants a fetch strictly after it.
	before := time.Now().Add(-time.Millisecond)
	resp, err := http.Get(proxy.URL + "/stream/12")
	if err != nil {
		t.Fatalf("GET /stream/12: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/stream/12 answered %d, want 200", resp.StatusCode)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 8192)); err != nil {
		t.Fatalf("reading /stream/12: %v", err)
	}
	if !s.SlotFetchLive(12) {
		t.Error("slot 12 is being pulled right now, SlotFetchLive must say so")
	}
	if !s.SlotPulledSince(12, before) {
		t.Error("an open slot-12 connection must count as pulled for the recall verify")
	}
	if s.SlotFetchLive(6) {
		t.Error("a row-2 fetch must not stamp row 1")
	}
}

// TestSelfProxyRecognisesTheSecondRow: a stored stream URL that points back at
// this agent's own /stream/<n> is poisoned whichever row n is on (#252).
func TestSelfProxyRecognisesTheSecondRow(t *testing.T) {
	for _, tc := range []struct {
		url  string
		slot int
		ok   bool
	}{
		{"http://127.0.0.1:8888/stream/7", 7, true},
		{"http://127.0.0.1:8888/stream/12", 12, true},
		{"http://127.0.0.1:8888/stream/13", 0, false},
	} {
		slot, ok := selfProxySlotRef(tc.url)
		if ok != tc.ok || slot != tc.slot {
			t.Errorf("selfProxySlotRef(%q) = %d,%v, want %d,%v", tc.url, slot, ok, tc.slot, tc.ok)
		}
	}
}
