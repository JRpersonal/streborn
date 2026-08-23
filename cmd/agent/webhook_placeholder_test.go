package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/webhooks"
)

// #536: a preset key configured as "webhook only" carries no STR preset, so the
// speaker treats it as unassigned, blinks orange and never emits a press frame.
// OnPresetSelected is what fires the per-key webhook, so it could never run.
// webhookOnlySlots is what tells the reconcile which keys need a placeholder.

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

func TestWebhookOnlySlotsNeedingPlaceholder(t *testing.T) {
	stick := []presets.Preset{{Slot: 2, Name: "A station"}}

	got := webhookOnlySlots(hookStore(t, []int{1, 2, 5}, nil), stick)

	// Slot 2 already holds a real preset, so the key is assigned and the box
	// reports its press without help. Only 1 and 5 need a placeholder.
	want := []int{1, 5}
	if len(got) != len(want) {
		t.Fatalf("slots = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("slots = %v, want %v", got, want)
		}
	}
}

func TestWebhookOnlySlotsWithEmptyStore(t *testing.T) {
	// The shape the 2026-08-22 reporter was in: no presets at all, and a
	// webhook on key 1 that could never fire. The reconcile short-circuits on
	// an empty store, so this is exactly the case that must still produce work.
	got := webhookOnlySlots(hookStore(t, []int{1}, nil), nil)
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("slots = %v, want [1]", got)
	}
}

func TestWebhookOnlySlotsIgnoresAdditionalMode(t *testing.T) {
	// "Keep original + webhook" plays the preset AND fires, so it has a preset
	// behind it by definition and must never get a placeholder written for it.
	if got := webhookOnlySlots(hookStore(t, nil, []int{3}), nil); len(got) != 0 {
		t.Fatalf("slots = %v, want none for additional mode", got)
	}
}

func TestWebhookOnlySlotsWithoutStore(t *testing.T) {
	// An agent with no webhook config at all must not change the reconcile's
	// behaviour in any way.
	if got := webhookOnlySlots(nil, []presets.Preset{{Slot: 1}}); got != nil {
		t.Fatalf("slots = %v, want nil", got)
	}
}
