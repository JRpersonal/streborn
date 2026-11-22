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
	"github.com/JRpersonal/streborn/internal/recallintent"
	"github.com/JRpersonal/streborn/internal/upnp"
	"github.com/JRpersonal/streborn/internal/webhooks"
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

// The row-2 recall of a double press carries when its first half arrived, so
// the Spotify engine can tell the pause row 1's switch-away put on it from a
// pause of the listener's (Jens' ST30, 2026-11-22: double press for the
// playlist that played, the song just went on). Standalone and grouped alike.
func TestDoublePressRowTwoRecallCarriesTheFirstPress(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		rig := newDoublePressRig(t, twoRowStore(t, true))
		if grouped {
			rig.h.group.zone.Store(groupMember)
			rig.h.tracePressesFn = func(int, time.Time) int { return 2 }
		}
		since := make(chan time.Time, 1)
		rig.h.playSlotFn = func(ctx context.Context, slot int) (string, error) {
			t0, ok := recallintent.PressSince(ctx)
			if !ok {
				t.Errorf("grouped=%v: the row-2 recall of slot %d carries no first press", grouped, slot)
			}
			since <- t0
			return "", nil
		}
		before := time.Now()
		ctx := context.Background()
		rig.h.OnPresetSelected(ctx, 1, key1Location, "One")
		rig.h.OnPresetSelected(ctx, 1, key1Location, "One")
		select {
		case t0 := <-since:
			if t0.Before(before) || t0.After(time.Now()) {
				t.Errorf("grouped=%v: first press at %v, want the first frame's arrival", grouped, t0)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("grouped=%v: row 2 never played", grouped)
		}
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

// Live Portable 2026-11-22: the first press's recall noted its native
// activation a moment AFTER the double press had cancelled the rescue, and
// the rescue was armed for row 1 again. A superseded first press notes
// nothing: neither when the newer press is already visible, nor when its
// cancel lands between the epoch read and the note.
func TestSupersededFirstPressArmsNoRescue(t *testing.T) {
	const nativeLoc = "/station?data=eyJ9"
	rig := newDoublePressRig(t, twoRowStore(t, true))
	seq := rig.h.pressSeq.Add(1)
	rig.h.pressSeq.Add(1) // the second half of the double press
	rig.h.recallPreset(context.Background(), seq, time.Now(), 1, nativeLoc, "One")
	if slot := rig.h.firstPress.claim(time.Now()); slot != 0 {
		t.Fatalf("a superseded first press armed a rescue for slot %d", slot)
	}

	var f firstPressRescue
	epoch := f.currentEpoch()
	f.cancel()
	if f.noteNativeActivationIn(epoch, 1, time.Now()) {
		t.Fatal("an activation noted after a cancel must be dropped")
	}
	if slot := f.claim(time.Now()); slot != 0 {
		t.Fatalf("the dropped activation was claimed for slot %d", slot)
	}
	if !f.noteNativeActivationIn(f.currentEpoch(), 2, time.Now()) || f.claim(time.Now()) != 2 {
		t.Fatal("an activation without a cancel must still arm its one rescue")
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

// #1910: keys with nothing on row 1 hold a placeholder (placeholderKeys) so
// the speaker reports their press at all. The placeholder itself is never
// played; what the press plays is decided here.

// row2OnlyStore holds a station on key 3's second row (slot 9) and nothing on
// key 3's first row.
func row2OnlyStore(t *testing.T) *presets.Store {
	t.Helper()
	s := presets.New()
	_ = s.SetSlot(presets.Preset{Slot: 9, Name: "Nine", StreamURL: "http://radio.example/nine", Type: "radio"})
	return s
}

const key3Location = "http://127.0.0.1:8888/stream/3"

// webhookHits builds a webhook store whose preset keys fire at a local
// server, and returns a channel that receives one value per fire.
func webhookHits(t *testing.T, replace, additional []int) (*webhooks.Store, chan struct{}) {
	t.Helper()
	hits := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	s := hookStore(t, nil, nil)
	cfg := webhooks.Config{Buttons: map[string]webhooks.Trigger{}}
	for _, group := range []struct {
		keys []int
		mode string
	}{{replace, webhooks.ModeReplace}, {additional, webhooks.ModeAdditional}} {
		for _, k := range group.keys {
			cfg.Buttons[presetButtonID(k)] = webhooks.Trigger{
				Action: webhooks.Action{Enabled: true, URL: srv.URL},
				Mode:   group.mode,
			}
		}
	}
	if err := s.Set(cfg); err != nil {
		t.Fatal(err)
	}
	return s, hits
}

func waitHit(t *testing.T, hits chan struct{}) {
	t.Helper()
	select {
	case <-hits:
	case <-time.After(3 * time.Second):
		t.Fatal("the key's webhook never fired")
	}
}

func TestSinglePressOnRowTwoOnlyKeyPlaysRowTwo(t *testing.T) {
	rig := newDoublePressRig(t, row2OnlyStore(t))
	rig.h.OnPresetSelected(context.Background(), 3, key3Location, "2x")
	if got := rig.waitRow2(t); got != 9 {
		t.Fatalf("row 2 played slot %d, want 9", got)
	}
	time.Sleep(100 * time.Millisecond)
	if row1, row2 := rig.counts(); row1 != 0 || row2 != 1 {
		t.Fatalf("row-1 recalls %d (want 0: the placeholder's /stream/3 must never be pushed), row-2 recalls %d (want 1)", row1, row2)
	}
}

func TestDoublePressOnRowTwoOnlyKeyPlaysRowTwoOnce(t *testing.T) {
	rig := newDoublePressRig(t, row2OnlyStore(t))
	ctx := context.Background()
	rig.h.OnPresetSelected(ctx, 3, key3Location, "2x")
	rig.h.OnPresetSelected(ctx, 3, key3Location, "2x")
	if got := rig.waitRow2(t); got != 9 {
		t.Fatalf("row 2 played slot %d, want 9", got)
	}
	time.Sleep(200 * time.Millisecond)
	if row1, row2 := rig.counts(); row1 != 0 || row2 != 1 {
		t.Fatalf("row-1 recalls %d (want 0), row-2 recalls %d (want 1: the second press must not restart the station)", row1, row2)
	}
	// The pair is used up: a third press starts fresh and plays row 2 again,
	// like any single press of the key.
	rig.h.OnPresetSelected(ctx, 3, key3Location, "2x")
	if got := rig.waitRow2(t); got != 9 {
		t.Fatalf("third press played slot %d, want 9", got)
	}
}

func TestGroupedDoublePressOnRowTwoOnlyKeyDoesNotRestart(t *testing.T) {
	rig := newDoublePressRig(t, row2OnlyStore(t))
	rig.h.group.zone.Store(groupMember)
	rig.h.tracePressesFn = func(int, time.Time) int { return 2 }
	ctx := context.Background()
	rig.h.OnPresetSelected(ctx, 3, key3Location, "2x")
	rig.waitRow2(t)
	rig.h.OnPresetSelected(ctx, 3, key3Location, "2x")
	time.Sleep(200 * time.Millisecond)
	if row1, row2 := rig.counts(); row1 != 0 || row2 != 1 {
		t.Fatalf("row-1 recalls %d, row-2 recalls %d, want 0 and 1", row1, row2)
	}
}

func TestAdditionalWebhookOnlyKeyFiresAndPlaysNothing(t *testing.T) {
	rig := newDoublePressRig(t, presets.New())
	wh, hits := webhookHits(t, nil, []int{4})
	rig.h.webhooks = wh
	rig.h.OnPresetSelected(context.Background(), 4, "http://127.0.0.1:8888/stream/4", webhookPlaceholderName)
	waitHit(t, hits)
	time.Sleep(200 * time.Millisecond)
	if row1, row2 := rig.counts(); row1 != 0 || row2 != 0 {
		t.Fatalf("row-1 recalls %d, row-2 recalls %d, want nothing played for a webhook-only key", row1, row2)
	}
}

func TestAdditionalWebhookOnRowTwoOnlyKeyFiresAndPlaysRowTwo(t *testing.T) {
	rig := newDoublePressRig(t, row2OnlyStore(t))
	wh, hits := webhookHits(t, nil, []int{3})
	rig.h.webhooks = wh
	rig.h.OnPresetSelected(context.Background(), 3, key3Location, "2x")
	waitHit(t, hits)
	if got := rig.waitRow2(t); got != 9 {
		t.Fatalf("row 2 played slot %d, want 9", got)
	}
}

func TestReplaceWebhookOnRowTwoOnlyKeyWithholdsTheSinglePress(t *testing.T) {
	rig := newDoublePressRig(t, row2OnlyStore(t))
	wh, hits := webhookHits(t, []int{3}, nil)
	rig.h.webhooks = wh
	ctx := context.Background()
	// The replace-mode return happens on the read loop, before any recall
	// could be started, so nothing may have been asked to play by now.
	rig.h.OnPresetSelected(ctx, 3, key3Location, webhookPlaceholderName)
	if row1, row2 := rig.counts(); row1 != 0 || row2 != 0 {
		t.Fatalf("replace mode must withhold the single press, got row-1 %d row-2 %d", row1, row2)
	}
	if seq := rig.h.pressSeq.Load(); seq != 0 {
		t.Fatalf("a withheld press started a recall (press sequence %d)", seq)
	}
	waitHit(t, hits)
	// Its double press still plays row 2.
	rig.h.OnPresetSelected(ctx, 3, key3Location, webhookPlaceholderName)
	if got := rig.waitRow2(t); got != 9 {
		t.Fatalf("double press played slot %d, want 9", got)
	}
}

func TestForeignKeyWithRowTwoIsNoPlaceholder(t *testing.T) {
	// A speaker-cached entry STR did not write occupies row 1 from the
	// speaker's point of view: the key plays what it holds, as before.
	rig := newDoublePressRig(t, row2OnlyStore(t))
	rig.h.OnPresetSelected(context.Background(), 3, "http://radio.example/foreign", "Foreign")
	rig.waitRow1(t)
	if _, row2 := rig.counts(); row2 != 0 {
		t.Fatal("a foreign key must not be taken for a placeholder")
	}
}

func TestRowOnePresetIsNeverAPlaceholder(t *testing.T) {
	rig := newDoublePressRig(t, twoRowStore(t, true))
	rig.h.OnPresetSelected(context.Background(), 1, key1Location, "One")
	rig.waitRow1(t)
	if row2, holder := rig.h.placeholderPress(1, key1Location); holder || row2 != nil {
		t.Fatal("a key with a row-1 preset must never be handled as a placeholder")
	}
	time.Sleep(100 * time.Millisecond)
	if _, row2 := rig.counts(); row2 != 0 {
		t.Fatal("a single press of a key with a row-1 preset plays row 1, never row 2")
	}
}
