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

// TestSlotPulledSince covers the handler's slot-scoped success pass-through:
// the decision itself (liveness, sustained-fetch) lives in
// streamproxy.SlotPulledSince and is tested there; here the wiring must be
// nil-safe and forward slot + anchor untouched.
func TestSlotPulledSince(t *testing.T) {
	h := &presetWsHandler{}
	if h.slotPulledSince(3, time.Now().Add(-time.Minute)) {
		t.Fatal("nil slotPulled must read as not pulled")
	}

	var gotSlot int
	var gotSince time.Time
	h.slotPulled = func(slot int, since time.Time) bool {
		gotSlot, gotSince = slot, since
		return slot == 3
	}
	anchor := time.Now()
	if !h.slotPulledSince(3, anchor) {
		t.Fatal("wired signal must decide")
	}
	if gotSlot != 3 || !gotSince.Equal(anchor) {
		t.Fatalf("slot/anchor must pass through untouched, got slot=%d since=%v", gotSlot, gotSince)
	}
	if h.slotPulledSince(2, anchor) {
		t.Fatal("another slot must not certify this recall")
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

// TestUrgentResyncHasOwnBudget: the urgent re-sync (a 1036/re-login wipe is
// imminent) must not be starved by a routine ask that consumed the normal
// budget minutes earlier - the field bundles showed five dead-key presses
// producing no heal because the boot-time ask had eaten the shared budget.
func TestUrgentResyncHasOwnBudget(t *testing.T) {
	presetResyncAsk.Store(false)
	presetResyncLast.Store(time.Now().Unix()) // routine budget just consumed
	presetResyncUrgentLast.Store(0)
	t.Cleanup(func() {
		presetResyncAsk.Store(false)
		presetResyncLast.Store(0)
		presetResyncUrgentLast.Store(0)
	})

	requestPresetKeyResync(nil)
	if presetResyncAsk.Load() {
		t.Fatal("routine ask inside its gap must be rate-limited")
	}

	requestPresetKeyResyncUrgent(nil)
	if !presetResyncAsk.Load() {
		t.Fatal("urgent ask must not be starved by the routine budget")
	}

	// The urgent budget itself still bounds a 1036 storm.
	presetResyncAsk.Store(false)
	requestPresetKeyResyncUrgent(nil)
	if presetResyncAsk.Load() {
		t.Fatal("urgent asks inside the urgent gap must coalesce")
	}
}
