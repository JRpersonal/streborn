package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/upnp"
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

// TestNudgeStuckSource pins the sys-power-nudge decision: exactly one nudge
// per recall, only at the third attempt, only while the box reports
// INVALID_SOURCE (the mojo/ST30 wedge where the box inertly ACKs every push
// and only a real sys-power toggle ever activated the source).
func TestNudgeStuckSource(t *testing.T) {
	cases := []struct {
		attempt int
		nudged  bool
		source  string
		want    bool
	}{
		{1, false, "INVALID_SOURCE", false}, // give the normal wake+re-push a chance first
		{2, false, "INVALID_SOURCE", false},
		{3, false, "INVALID_SOURCE", true},
		{3, true, "INVALID_SOURCE", false}, // one nudge per recall
		{3, false, "STANDBY", false},       // the wake path owns standby
		{3, false, "UPNP", false},
		{3, false, "", false}, // probe failed: do not toggle blind
		{4, false, "INVALID_SOURCE", false},
	}
	for _, c := range cases {
		if got := nudgeStuckSource(c.attempt, c.nudged, c.source); got != c.want {
			t.Errorf("nudgeStuckSource(%d,%v,%q) = %v, want %v", c.attempt, c.nudged, c.source, got, c.want)
		}
	}
}

// TestRePushAfterSourceRejectWakesBeforePush asserts the wake ordering of the
// wrong-state repair (bundle signature: box drops INVALID_SOURCE->STANDBY
// within ~1s of the press; pushing without a wake did nothing): the repair
// must wake the box BEFORE re-issuing SetURI+Play.
func TestRePushAfterSourceRejectWakesBeforePush(t *testing.T) {
	var mu sync.Mutex
	var order []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		order = append(order, "soap")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := &presetWsHandler{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		boxHost:  "192.0.2.9",
		renderer: &upnp.Renderer{ControlURL: srv.URL, Client: srv.Client()},
		wakeBox: func(ctx context.Context, host string) error {
			mu.Lock()
			order = append(order, "wake")
			mu.Unlock()
			return nil
		},
		boxPlayingFn: func(url string) bool { return false },
	}
	seq := h.pressSeq.Add(1)
	pressAt := time.Now().Add(-2 * time.Second)
	h.OnSourceRejected(context.Background()) // 1036 stamped after pressAt

	h.rePushAfterSourceReject(seq, 0, pressAt, 1, "http://127.0.0.1:8888/stream/1", "S", "", "")

	mu.Lock()
	defer mu.Unlock()
	if len(order) < 2 || order[0] != "wake" {
		t.Fatalf("the repair must wake the box before pushing, got order %v", order)
	}
	if order[len(order)-1] != "soap" {
		t.Fatalf("the repair must push after waking, got order %v", order)
	}
}

// TestOnPresetSelectedReturnsBeforeSlowRecall pins the F1 contract directly:
// the gabbo dispatch must not block behind slow box I/O (pre-fix, a press on a
// cold box held the single read loop for up to ~18s and every queued frame -
// including the teardown STOP_STATE the classification windows were tuned for -
// was stamped late).
func TestOnPresetSelectedReturnsBeforeSlowRecall(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()

	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatalf("presets.Load: %v", err)
	}
	if err := store.SetSlot(presets.Preset{Slot: 1, Name: "S", StreamURL: "http://example.com/s.mp3", Type: "radio"}); err != nil {
		t.Fatal(err)
	}
	h := &presetWsHandler{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		store:        store,
		renderer:     &upnp.Renderer{ControlURL: slow.URL, Client: slow.Client()},
		wakeBox:      func(ctx context.Context, host string) error { return nil },
		boxPlayingFn: func(url string) bool { return false },
	}

	start := time.Now()
	h.OnPresetSelected(context.Background(), 1, "http://127.0.0.1:8888/stream/1", "S")
	elapsed := time.Since(start)
	// Supersede immediately so the background recall/verify goroutines stand
	// down at their next check instead of retrying for ~30s.
	h.pressSeq.Add(1)

	if elapsed > time.Second {
		t.Fatalf("OnPresetSelected blocked the gabbo dispatch for %v; the slow recall must run in the background", elapsed)
	}
}
