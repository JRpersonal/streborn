package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/webhooks"
)

// #536: a preset key configured as "webhook only" carries no STR preset, so the
// speaker treats it as unassigned, blinks orange and never emits a press frame.
// OnPresetSelected is what fires the per-key webhook, so it could never run.
// placeholderKeys is what tells the reconcile which keys need a placeholder.

func hookStore(t *testing.T, replaceSlots []int, additionalSlots []int) *webhooks.Store {
	t.Helper()
	s, err := webhooks.Load(filepath.Join(t.TempDir(), "webhooks.json"), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("load webhooks store: %v", err)
	}
	cfg := webhooks.Config{Buttons: map[string]webhooks.Trigger{}}
	add := func(slots []int, mode string) {
		for _, slot := range slots {
			cfg.Buttons[presetButtonID(slot)] = webhooks.Trigger{
				Action: webhooks.Action{Enabled: true, URL: "http://192.0.2.1/hook"},
				Mode:   mode,
			}
		}
	}
	add(replaceSlots, webhooks.ModeReplace)
	add(additionalSlots, webhooks.ModeAdditional)
	if err := s.Set(cfg); err != nil {
		t.Fatalf("set webhooks config: %v", err)
	}
	return s
}

func presetButtonID(slot int) string {
	return fmt.Sprintf("preset%d", slot)
}

// webhooksReplaceTrigger is what a "webhook only" key carries: an enabled
// action in replace mode, so the firmware's own reaction to the press is
// withheld and only the webhook fires. Same shape hookStore writes; separate
// so a test can add one to a store that is already live.
func webhooksReplaceTrigger() webhooks.Trigger {
	return webhooks.Trigger{
		Action: webhooks.Action{Enabled: true, URL: "http://192.0.2.1/hook"},
		Mode:   webhooks.ModeReplace,
	}
}

func TestPlaceholderKeysForWebhookOnlyKeys(t *testing.T) {
	all := []presets.Preset{{Slot: 2, Name: "A station"}}

	got := placeholderSlots(hookStore(t, []int{1, 2, 5}, nil), all)

	// Slot 2 already holds a real preset, so the key is assigned and the box
	// reports its press without help. Only 1 and 5 need a placeholder.
	want := []int{1, 5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("slots = %v, want %v", got, want)
	}
}

func TestPlaceholderKeysWithEmptyStore(t *testing.T) {
	// The shape the 2026-08-22 reporter was in: no presets at all, and a
	// webhook on key 1 that could never fire. The reconcile short-circuits on
	// an empty store, so this is exactly the case that must still produce work.
	got := placeholderSlots(hookStore(t, []int{1}, nil), nil)
	if !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("slots = %v, want [1]", got)
	}
}

func TestPlaceholderKeysWithoutStore(t *testing.T) {
	// An agent with no webhook config and no second row must not change the
	// reconcile's behaviour in any way.
	if got := placeholderSlots(nil, []presets.Preset{{Slot: 1}}); got != nil {
		t.Fatalf("slots = %v, want nil", got)
	}
}

// #1910: a key with nothing on its single press is refused by the speaker,
// so a station on its double press, or a webhook of either mode, is never
// reached unless the key holds a placeholder. Every combination, per key.
func TestPlaceholderKeysDecision(t *testing.T) {
	for _, tc := range []struct {
		name       string
		all        []presets.Preset
		replace    []int
		additional []int
		want       []keyPlaceholder
	}{
		{
			name: "row 2 only",
			all:  []presets.Preset{{Slot: 9, Name: "Nine"}},
			want: []keyPlaceholder{{slot: 3, playsRow2: true}},
		},
		{
			name:       "additional-mode webhook only",
			additional: []int{4},
			want:       []keyPlaceholder{{slot: 4}},
		},
		{
			name:       "row 2 and an additional-mode webhook",
			all:        []presets.Preset{{Slot: 10, Name: "Ten"}},
			additional: []int{4},
			want:       []keyPlaceholder{{slot: 4, playsRow2: true}},
		},
		{
			// The single press stays webhook-only as the mode promised, so the
			// key is named and played as a webhook key; its double press still
			// plays row 2 (planPress runs before the replace check).
			name:    "row 2 and a replace-mode webhook",
			all:     []presets.Preset{{Slot: 11, Name: "Eleven"}},
			replace: []int{5},
			want:    []keyPlaceholder{{slot: 5}},
		},
		{
			name: "neither",
			all:  nil,
			want: nil,
		},
		{
			// A real row-1 preset is never replaced, whatever else the key has.
			name:       "real row-1 preset with row 2 and webhooks",
			all:        []presets.Preset{{Slot: 1, Name: "One"}, {Slot: 7, Name: "Seven"}},
			replace:    []int{1},
			additional: nil,
			want:       nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := placeholderKeys(hookStore(t, tc.replace, tc.additional), tc.all)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("placeholderKeys = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPlaceholderNames(t *testing.T) {
	if got := (keyPlaceholder{slot: 1, playsRow2: true}).name(); got != row2PlaceholderName {
		t.Fatalf("row-2 key name = %q, want %q", got, row2PlaceholderName)
	}
	if got := (keyPlaceholder{slot: 1}).name(); got != webhookPlaceholderName {
		t.Fatalf("webhook key name = %q, want %q", got, webhookPlaceholderName)
	}
}

// What the reconcile writes for the placeholder keys: an empty key gets one,
// a key the box already holds in STR's own form is left alone outside a
// forced pass, a foreign key is never touched, and a native leftover is
// rewritten so STR decides what the press plays.
func TestPlaceholderWrites(t *testing.T) {
	keys := []keyPlaceholder{{slot: 1, playsRow2: true}, {slot: 2}, {slot: 3, playsRow2: true}, {slot: 4}}
	boxLocs := map[int]string{
		2: "http://127.0.0.1:8888/stream/2",
		3: "https://api.deezer.com/user/me/flow",
		4: ownNativeEntry(4, "Old station").Location,
	}
	got := placeholderWrites(keys, boxLocs, false)
	want := []boxcli.PresetSpec{
		{Slot: 1, Name: row2PlaceholderName, StreamURL: boxurl.StreamSlot(1)},
		{Slot: 4, Name: webhookPlaceholderName, StreamURL: boxurl.StreamSlot(4)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routine pass writes %+v, want %+v", got, want)
	}
	for _, w := range got {
		if w.NativeLocation != "" {
			t.Fatalf("slot %d: a placeholder must never be written natively, the box would play it by itself", w.Slot)
		}
	}
	forced := placeholderWrites(keys, boxLocs, true)
	slots := []int{}
	for _, w := range forced {
		slots = append(slots, w.Slot)
	}
	if !reflect.DeepEqual(slots, []int{1, 2, 4}) {
		t.Fatalf("forced pass writes slots %v, want [1 2 4] (never the foreign key 3)", slots)
	}
}

// A station saved on a key's double press while its single press is empty
// writes nothing to the speaker by itself; the reconcile's maintenance wait
// must notice the new placeholder key and look at the speaker's keys now,
// not after up to five minutes.
func TestMaintenanceWaitWakesForANewPlaceholderKey(t *testing.T) {
	r := newTestReconciler(t)
	r.store = presets.New()
	r.fullDone = true
	sleeps := 0
	r.sleep = func(time.Duration) {
		sleeps++
		if sleeps == 2 {
			_ = r.store.SetSlot(presets.Preset{Slot: 8, Name: "Eight", StreamURL: "http://radio.example/eight", Type: "radio"})
		}
	}
	r.waitForNextPass()
	if sleeps != 2 {
		t.Fatalf("the wait slept %d ticks, want it to end on the tick the placeholder key appeared (2)", sleeps)
	}
	// Nothing changed: the full maintenance cadence.
	sleeps = 0
	r.sleep = func(time.Duration) { sleeps++ }
	r.waitForNextPass()
	if sleeps != 30 {
		t.Fatalf("an unchanged store slept %d ticks, want the full 30", sleeps)
	}
}
