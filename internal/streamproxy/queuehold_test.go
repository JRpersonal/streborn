package streamproxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// finiteFileServer serves a small, cleanly-terminating file so handle() ends
// with the file-complete path (200 + body) instead of reconnecting forever.
func finiteFileServer(t *testing.T) *httptest.Server {
	t.Helper()
	payload := bytes.Repeat([]byte{0x5A}, 8192)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mp4")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
}

func queuePreset(store *presets.Store, slot int) {
	// SetSlot returns "Store has no path" for a pathless test store, but the
	// in-memory slot is populated before Save runs, so Get sees it. Ignore the err.
	_ = store.SetSlot(presets.Preset{Slot: slot, Name: "Folder", Type: "queue"})
}

// TestQueuePresetHoldsForLiveTrack: a queue preset (empty StreamURL) whose live
// URL is not ready yet must be HELD, not 404'd, until the recall lands the first
// track - the ST20 source-switch race. Once the resolver returns a URL, the box
// gets that track's bytes.
func TestQueuePresetHoldsForLiveTrack(t *testing.T) {
	up := finiteFileServer(t)
	defer up.Close()

	s := New(presets.New(), silentLogger())
	s.client = &http.Client{} // allow the loopback dial past the SSRF guard
	queuePreset(s.store, 6)

	var polls atomic.Int32
	s.SetQueueLiveURLFn(func(slot int) (string, bool) {
		if slot != 6 {
			return "", false
		}
		// Empty (still recalling) for the first few polls, then the live track.
		if polls.Add(1) < 3 {
			return "", true
		}
		return up.URL, true
	})

	req := httptest.NewRequest(http.MethodGet, "/stream/6", nil)
	rw := httptest.NewRecorder()
	s.handle(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("queue preset must be served once the track lands, got %d", rw.Code)
	}
	if rw.Body.Len() == 0 {
		t.Fatal("the held-for track produced no bytes")
	}
	if polls.Load() < 3 {
		t.Fatalf("handler did not hold for the track (only %d polls)", polls.Load())
	}
}

// TestQueuePresetIdleBailsFast: a queue slot whose resolver never reports a
// recall in flight must 404 quickly (after queueRecallGrace), never burn the
// full queueRecallHold. Guards against a spurious native fetch hanging the box.
func TestQueuePresetIdleBailsFast(t *testing.T) {
	s := New(presets.New(), silentLogger())
	queuePreset(s.store, 6)
	s.SetQueueLiveURLFn(func(slot int) (string, bool) { return "", false })

	req := httptest.NewRequest(http.MethodGet, "/stream/6", nil)
	rw := httptest.NewRecorder()
	start := time.Now()
	s.handle(rw, req)
	elapsed := time.Since(start)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("an idle queue slot must 404, got %d", rw.Code)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("idle bail took %s; it must return near queueRecallGrace, not queueRecallHold", elapsed)
	}
}

// TestNonQueuePresetImmediate404: a non-queue preset with an empty StreamURL
// keeps the instant 404 and never consults the queue resolver - the queue hold
// is strictly scoped to Type=="queue".
func TestNonQueuePresetImmediate404(t *testing.T) {
	s := New(presets.New(), silentLogger())
	// A radio-type preset saved with no URL (the genuinely-dead slot, #252).
	_ = s.store.SetSlot(presets.Preset{Slot: 4, Name: "Dead", Type: "radio"})

	var called atomic.Bool
	s.SetQueueLiveURLFn(func(slot int) (string, bool) { called.Store(true); return "", false })

	req := httptest.NewRequest(http.MethodGet, "/stream/4", nil)
	rw := httptest.NewRecorder()
	start := time.Now()
	s.handle(rw, req)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("a non-queue empty preset must 404, got %d", rw.Code)
	}
	if time.Since(start) >= 500*time.Millisecond {
		t.Fatal("a non-queue 404 must be immediate, no hold")
	}
	if called.Load() {
		t.Fatal("the queue resolver must not be consulted for a non-queue preset")
	}
}

// TestQueuePresetNilResolver: with no resolver wired (nil fn), a queue preset
// falls back to the same instant 404 - a partially-wired build never hangs.
func TestQueuePresetNilResolver(t *testing.T) {
	s := New(presets.New(), silentLogger())
	queuePreset(s.store, 6)
	// no SetQueueLiveURLFn

	req := httptest.NewRequest(http.MethodGet, "/stream/6", nil)
	rw := httptest.NewRecorder()
	start := time.Now()
	s.handle(rw, req)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("queue preset with nil resolver must 404, got %d", rw.Code)
	}
	if time.Since(start) >= 500*time.Millisecond {
		t.Fatal("a nil-resolver 404 must be immediate")
	}
}
