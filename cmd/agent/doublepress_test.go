package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/upnp"
)

// doublePressRig is a handler whose box-facing edges are recorded: the row-1
// recall's last-play record (one per ordinary press that reached the push) and
// the row-2 recall through the app path.
type doublePressRig struct {
	h *presetWsHandler

	mu        sync.Mutex
	row1Plays []string
	row2Plays []int
	row2Done  chan int
	row1Done  chan string
}

func newDoublePressRig(t *testing.T, store *presets.Store) *doublePressRig {
	t.Helper()
	soap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(soap.Close)
	rig := &doublePressRig{row2Done: make(chan int, 4), row1Done: make(chan string, 4)}
	rig.h = &presetWsHandler{
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		store:          store,
		renderer:       &upnp.Renderer{ControlURL: soap.URL, Client: soap.Client()},
		wakeBox:        func(context.Context, string) error { return nil },
		boxPlayingFn:   func(string) bool { return true },
		ownSelectionFn: func(int) bool { return false },
		noteLastPlay: func(boxURL, _, _, _ string) uint64 {
			rig.mu.Lock()
			rig.row1Plays = append(rig.row1Plays, boxURL)
			rig.mu.Unlock()
			rig.row1Done <- boxURL
			return 0
		},
		playSlotFn: func(_ context.Context, slot int) (string, error) {
			rig.mu.Lock()
			rig.row2Plays = append(rig.row2Plays, slot)
			rig.mu.Unlock()
			rig.row2Done <- slot
			return "", nil
		},
	}
	// Standalone unless a test says otherwise.
	rig.h.group.zone.Store(groupAlone)
	return rig
}

func (r *doublePressRig) counts() (row1, row2 int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.row1Plays), len(r.row2Plays)
}

func (r *doublePressRig) waitRow2(t *testing.T) int {
	t.Helper()
	select {
	case slot := <-r.row2Done:
		return slot
	case <-time.After(3 * time.Second):
		t.Fatal("row 2 never played")
		return 0
	}
}

func (r *doublePressRig) waitRow1(t *testing.T) {
	t.Helper()
	select {
	case <-r.row1Done:
	case <-time.After(3 * time.Second):
		t.Fatal("row 1 never played")
	}
}

// twoRowStore holds a station on key 1 and one on its second-row slot 7.
func twoRowStore(t *testing.T, row2 bool) *presets.Store {
	t.Helper()
	s := presets.New()
	// A pathless store keeps the slots in memory and only reports that it
	// could not persist them.
	_ = s.SetSlot(presets.Preset{Slot: 1, Name: "One", StreamURL: "http://radio.example/one", Type: "radio"})
	if row2 {
		_ = s.SetSlot(presets.Preset{Slot: 7, Name: "Seven", StreamURL: "http://radio.example/seven", Type: "radio"})
	}
	return s
}

const key1Location = "http://127.0.0.1:8888/stream/1"

func TestDetectorPairsTwoFramesWithinTheWindow(t *testing.T) {
	var d doublePressDetector
	t0 := time.Now()
	if second, _ := d.observe(1, t0, false); second {
		t.Fatal("a first frame is never a double press")
	}
	if second, first := d.observe(1, t0.Add(250*time.Millisecond), false); !second || !first.Equal(t0) {
		t.Fatalf("a re-press 250 ms later must be the second half (second=%v first=%v)", second, first)
	}
	// A third quick press starts a new pair, it does not extend the old one.
	if second, _ := d.observe(1, t0.Add(500*time.Millisecond), false); second {
		t.Fatal("the press after a double press must start fresh")
	}
}

func TestDetectorIgnoresSlowAndCrossKeyPresses(t *testing.T) {
	var d doublePressDetector
	t0 := time.Now()
	d.observe(1, t0, false)
	if second, _ := d.observe(1, t0.Add(doublePressWindow+time.Millisecond), false); second {
		t.Fatal("a press outside the window is a new single press")
	}
	d.observe(2, t0.Add(2*time.Second), false)
	if second, _ := d.observe(3, t0.Add(2*time.Second+200*time.Millisecond), false); second {
		t.Fatal("two different keys are two single presses")
	}
}

func TestDetectorNeverCountsOwnSelections(t *testing.T) {
	var d doublePressDetector
	t0 := time.Now()
	// STR's own press as the first frame: the person's press after it is a
	// first press, not a second.
	d.observe(1, t0, true)
	if second, _ := d.observe(1, t0.Add(200*time.Millisecond), false); second {
		t.Fatal("STR's own selection must not count as a first press")
	}
	// STR's own press as the second frame (the first-press rescue).
	if second, _ := d.observe(1, t0.Add(400*time.Millisecond), true); second {
		t.Fatal("STR's own selection must not count as a second press")
	}
}

func TestDoublePressPlaysRowTwoAndSupersedesRowOne(t *testing.T) {
	rig := newDoublePressRig(t, twoRowStore(t, true))
	ctx := context.Background()
	rig.h.OnPresetSelected(ctx, 1, key1Location, "One")
	seqAfterFirst := rig.h.pressSeq.Load()
	rig.h.OnPresetSelected(ctx, 1, key1Location, "One")
	if got := rig.waitRow2(t); got != 7 {
		t.Fatalf("row 2 played slot %d, want 7", got)
	}
	if rig.h.pressSeq.Load() == seqAfterFirst {
		t.Fatal("the double press must bump the press sequence so press 1's recall stands down")
	}
	if !rig.h.superseded(seqAfterFirst, 0) {
		t.Fatal("press 1's verify must read as superseded")
	}
	// Press 1 alone reached row 1; the second frame never started a row-1
	// recall of its own (no second "next track" on a Spotify key either).
	time.Sleep(200 * time.Millisecond)
	if row1, row2 := rig.counts(); row1 > 1 || row2 != 1 {
		t.Fatalf("row-1 recalls %d (want at most 1), row-2 recalls %d (want 1)", row1, row2)
	}
}

func TestDoublePressWithEmptyRowTwoStaysOnRowOne(t *testing.T) {
	rig := newDoublePressRig(t, twoRowStore(t, false))
	ctx := context.Background()
	rig.h.OnPresetSelected(ctx, 1, key1Location, "One")
	rig.h.OnPresetSelected(ctx, 1, key1Location, "One")
	// Both frames took the ordinary route (each bumps the sequence, the
	// second supersedes the first exactly as a re-press always did).
	if got := rig.h.pressSeq.Load(); got != 2 {
		t.Fatalf("press sequence %d, want 2 ordinary presses", got)
	}
	rig.waitRow1(t)
	if _, row2 := rig.counts(); row2 != 0 {
		t.Fatal("an empty row 2 must leave the key exactly as before")
	}
}

func TestSlowSecondPressIsASinglePress(t *testing.T) {
	rig := newDoublePressRig(t, twoRowStore(t, true))
	t0 := time.Now()
	if plan, _, _ := rig.h.planPress(1, t0); plan != planSingle {
		t.Fatal("first press must be single")
	}
	if plan, _, _ := rig.h.planPress(1, t0.Add(time.Second)); plan != planSingle {
		t.Fatal("a press a second later must be single")
	}
}

func TestGroupedDoublePressNeedsTheKeyTrace(t *testing.T) {
	old := doublePressTraceWait
	doublePressTraceWait = 150 * time.Millisecond
	t.Cleanup(func() { doublePressTraceWait = old })
	for _, tc := range []struct {
		name    string
		presses int
		row2    bool
	}{
		{"trace shows two presses", 2, true},
		{"group echo, trace shows one press", 1, false},
		{"no trace on this speaker", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDoublePressRig(t, twoRowStore(t, true))
			rig.h.group.zone.Store(groupMember)
			rig.h.tracePressesFn = func(key int, _ time.Time) int {
				if key != 1 {
					return 0
				}
				return tc.presses
			}
			ctx := context.Background()
			rig.h.OnPresetSelected(ctx, 1, key1Location, "One")
			rig.waitRow1(t) // press 1 plays row 1 at once, grouped or not
			rig.h.OnPresetSelected(ctx, 1, key1Location, "One")
			if tc.row2 {
				if got := rig.waitRow2(t); got != 7 {
					t.Fatalf("row 2 played slot %d, want 7", got)
				}
				return
			}
			// The echo is handled like the ordinary press it always was.
			rig.waitRow1(t)
			if _, row2 := rig.counts(); row2 != 0 {
				t.Fatal("a group echo must never play row 2")
			}
		})
	}
}

func TestUnknownGroupStateAsksTheTrace(t *testing.T) {
	rig := newDoublePressRig(t, twoRowStore(t, true))
	rig.h.group.zone.Store(groupUnknown)
	t0 := time.Now()
	rig.h.planPress(1, t0)
	if plan, _, _ := rig.h.planPress(1, t0.Add(200*time.Millisecond)); plan != planConfirm {
		t.Fatalf("plan = %v, want planConfirm while the zone state is unknown", plan)
	}
	rig.h.group.zone.Store(groupAlone)
	rig.h.group.pair.Store(groupMember)
	rig.h.planPress(1, t0.Add(2*time.Second))
	if plan, _, _ := rig.h.planPress(1, t0.Add(2200*time.Millisecond)); plan != planConfirm {
		t.Fatalf("plan = %v, want planConfirm in a stereo pair", plan)
	}
}

func TestOwnSelectionIsNeverADoublePress(t *testing.T) {
	rig := newDoublePressRig(t, twoRowStore(t, true))
	own := true
	rig.h.ownSelectionFn = func(int) bool { return own }
	t0 := time.Now()
	rig.h.planPress(1, t0)
	own = false
	if plan, _, _ := rig.h.planPress(1, t0.Add(200*time.Millisecond)); plan != planSingle {
		t.Fatal("a press right after STR's own selection must not pair with it")
	}
}

func TestDoublePressCancelsTheFirstPressRescue(t *testing.T) {
	var f firstPressRescue
	now := time.Now()
	f.noteNativeActivation(1, now)
	epoch := f.currentEpoch()
	f.cancel()
	if f.currentEpoch() == epoch {
		t.Fatal("cancel must move the epoch so a waiting rescue stands down")
	}
	if slot := f.claim(now.Add(time.Second)); slot != 0 {
		t.Fatalf("a cancelled activation was still claimed for a rescue on slot %d", slot)
	}
}

func TestZoneFramesFeedTheGroupState(t *testing.T) {
	h := &presetWsHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if g, known := h.group.grouped(); !g || known {
		t.Fatal("before any frame the state is unknown and treated as grouped")
	}
	h.group.zone.Store(membership(false))
	if g, known := h.group.grouped(); g || !known {
		t.Fatal("a dissolved zone frame means standalone")
	}
	h.group.zone.Store(membership(true))
	if g, _ := h.group.grouped(); !g {
		t.Fatal("a zone frame naming a master means grouped")
	}
}

func TestBoxRowPresetsDropsTheSecondRow(t *testing.T) {
	in := []presets.Preset{{Slot: 1}, {Slot: 6}, {Slot: 7}, {Slot: 12}}
	out := boxRowPresets(in)
	if len(out) != 2 || out[0].Slot != 1 || out[1].Slot != 6 {
		t.Fatalf("boxRowPresets = %+v, want slots 1 and 6 only", out)
	}
	// The missing-slot bookkeeping never names a row-2 slot, so the reconcile
	// cannot loop on a key the speaker does not have.
	if missing := slotsMissingOnBox(in, nil, nil); len(missing) != 2 {
		t.Fatalf("slotsMissingOnBox = %v, want only 1 and 6", missing)
	}
}
