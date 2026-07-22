package main

import (
	"testing"
	"time"
)

// TestSuperseded covers the hardware verify's stand-down on a newer play: a
// newer hardware press (press sequence) or a newer play of any kind (the
// webui recall generation) must stop an older verify loop from re-pushing its
// stale URL over the user's newest choice ("pressed 2, got 1").
func TestSuperseded(t *testing.T) {
	h := &presetWsHandler{}
	seq := h.pressSeq.Add(1)
	if h.superseded(seq, 0) {
		t.Fatal("the newest press must not read as superseded")
	}
	h.pressSeq.Add(1)
	if !h.superseded(seq, 0) {
		t.Fatal("a newer press must supersede the older verify")
	}

	cur := uint64(7)
	h2 := &presetWsHandler{recallGenFn: func() uint64 { return cur }}
	s2 := h2.pressSeq.Add(1)
	if h2.superseded(s2, 7) {
		t.Fatal("matching generation must not read as superseded")
	}
	cur = 8 // an app recall (or any newer play) bumped the shared generation
	if !h2.superseded(s2, 7) {
		t.Fatal("a newer recall generation must supersede the older verify")
	}
	if h2.superseded(s2, 0) {
		t.Fatal("gen 0 means the generation was never captured; only the press sequence decides")
	}
}

// TestSlotPulledSince covers the slot-scoped success signal: only THIS slot's
// proxied fetch after the press counts, so cross-traffic (another slot's
// reconnect, a zone follower) can no longer certify a failed recall as healthy.
func TestSlotPulledSince(t *testing.T) {
	h := &presetWsHandler{}
	if h.slotPulledSince(3, time.Now().Add(-time.Minute)) {
		t.Fatal("nil slotStreamActivity must read as not pulled")
	}

	stamp := time.Now()
	h.slotStreamActivity = func(slot int) time.Time {
		if slot == 3 {
			return stamp
		}
		return time.Time{}
	}
	if !h.slotPulledSince(3, stamp.Add(-time.Second)) {
		t.Fatal("this slot's fetch after the press must count as success")
	}
	if h.slotPulledSince(3, stamp.Add(time.Second)) {
		t.Fatal("a fetch BEFORE the press must not count")
	}
	if h.slotPulledSince(2, stamp.Add(-time.Second)) {
		t.Fatal("another slot's fetch must not certify this recall")
	}
}

// TestIsOwnBoxPresetLocation pins the prune's deletion guard to exactly the
// locations STR itself writes into box preset slots (boxurl shapes). The old
// loose "/stream/" substring match could misread a foreign station URL as
// STR-owned and delete a working box preset.
func TestIsOwnBoxPresetLocation(t *testing.T) {
	own := []string{
		"http://127.0.0.1:8888/stream/1",
		"http://127.0.0.1:8888/stream/6",
		"http://127.0.0.1:8888/spotify/stream-4.ogg",
		"http://127.0.0.1:8888/spotify/stream.ogg",
	}
	for _, u := range own {
		if !isOwnBoxPresetLocation(u) {
			t.Errorf("STR-written location not recognised: %s", u)
		}
	}
	foreign := []string{
		"http://icecast.example.com/stream/1",
		"http://example.com/radio/stream/128.mp3",
		"http://127.0.0.1:8888/stream/7",
		"http://127.0.0.1:8888/stream/raw?u=abc",
		"/v1/playback/station/s12345",
		"123456789",
		"",
	}
	for _, u := range foreign {
		if isOwnBoxPresetLocation(u) {
			t.Errorf("foreign location must never be prunable: %s", u)
		}
	}
}
