// Regression tests for the two #419 recall-verify fixes (on-site ST30 report
// 2026-07-25): Finding 1, the silent false success when a same-slot re-press
// resurrects the previous play's stale ContentItem; and Finding 2, the
// sys-power nudge never firing while the box inertly ACKs every push under
// source=UPNP without fetching audio.

package main

import (
	"context"
	"testing"
	"time"
)

const proxiedURL = "http://127.0.0.1:8888/stream/5"

// Finding 1: box refused the recall (1036), now_playing still matches the
// stale same-slot path in a play-ish state, but the proxy saw no fetch since
// the press and holds no open pull - that is silence, not success.
// pressAt sits firmly in the past: on Windows the system clock ticks coarsely
// enough that a press and a rejection stamped back to back can land on the
// SAME instant, and After() would read the rejection as not-after-the-press
// (in the field they are ~800 ms apart).
func TestRecallVerify_StaleSameSlotFalseSuccessRejected(t *testing.T) {
	pressAt := time.Now().Add(-time.Second)
	h := &presetWsHandler{
		boxPlayingFn:  func(string) bool { return true }, // stale play-ish report
		slotPulled:    func(int, time.Time) bool { return false },
		slotFetchLive: func(int) bool { return false },
	}
	h.OnSourceRejected(context.TODO()) // 1036 lands ~0.8s after the press
	ok, _ := h.recallReachedAudioSignal(5, proxiedURL, pressAt)
	if ok {
		t.Fatal("stale same-slot now_playing passed the verify despite a rejection and zero proxy evidence (#419 Finding 1)")
	}
}

// A same-slot re-press over a STILL-PLAYING stream keeps its open proxy pull:
// that success must survive the Finding-1 carve-out untouched.
func TestRecallVerify_LivePullKeepsSameSlotSuccess(t *testing.T) {
	pressAt := time.Now().Add(-time.Second)
	h := &presetWsHandler{
		boxPlayingFn:  func(string) bool { return true },
		slotPulled:    func(int, time.Time) bool { return false }, // pull predates the press
		slotFetchLive: func(int) bool { return true },             // but it is still open
	}
	h.OnSourceRejected(context.TODO())
	ok, signal := h.recallReachedAudioSignal(5, proxiedURL, pressAt)
	if !ok || signal != "now_playing" {
		t.Fatalf("still-playing same-slot re-press must stay a success, got ok=%v signal=%q", ok, signal)
	}
}

// Without a rejection for this recall, the now_playing report keeps its old
// trust - no behavior change for the healthy path.
func TestRecallVerify_UnrefusedRecallKeepsNowPlayingTrust(t *testing.T) {
	h := &presetWsHandler{
		boxPlayingFn: func(string) bool { return true },
	}
	ok, signal := h.recallReachedAudioSignal(5, proxiedURL, time.Now())
	if !ok || signal != "now_playing" {
		t.Fatalf("unrefused recall lost its now_playing trust, got ok=%v signal=%q", ok, signal)
	}
}

// Direct (non-proxied) URLs have no proxy evidence to demand; the carve-out
// must never apply to them.
func TestRecallVerify_DirectURLUnaffectedByCarveOut(t *testing.T) {
	pressAt := time.Now().Add(-time.Second)
	h := &presetWsHandler{
		boxPlayingFn: func(string) bool { return true },
	}
	h.OnSourceRejected(context.TODO())
	ok, _ := h.recallReachedAudioSignal(5, "http://radio.example/live.mp3", pressAt)
	if !ok {
		t.Fatal("direct-URL recall wrongly subjected to the proxy-evidence carve-out")
	}
}

// Finding 2: source=UPNP + proxied stream + zero fetch evidence at the nudge
// attempt = the inert-ACK freeze; the nudge must fire. Any other source (a
// user-started AUX session) and any direct URL must never nudge.
func TestInertAckNudge(t *testing.T) {
	pressAt := time.Now()
	h := &presetWsHandler{
		slotPulled:    func(int, time.Time) bool { return false },
		slotFetchLive: func(int) bool { return false },
	}
	if !h.inertAckNudge("UPNP", 5, proxiedURL, pressAt) {
		t.Fatal("inert-ACK freeze (UPNP, proxied, zero fetches) must nudge (#419 Finding 2)")
	}
	if h.inertAckNudge("AUX", 5, proxiedURL, pressAt) {
		t.Fatal("a user-started AUX session must never be nudged")
	}
	if h.inertAckNudge("UPNP", 5, "http://radio.example/live.mp3", pressAt) {
		t.Fatal("direct-URL plays leave no fetch evidence and must never nudge")
	}
	live := &presetWsHandler{
		slotPulled:    func(int, time.Time) bool { return true },
		slotFetchLive: func(int) bool { return false },
	}
	if live.inertAckNudge("UPNP", 5, proxiedURL, pressAt) {
		t.Fatal("a slot fetched since the press is playing; nudging it would interrupt audio")
	}
}
