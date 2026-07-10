package streamproxy

import (
	"io"
	"log/slog"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

func TestResolvePresetURLHealsSlotForm(t *testing.T) {
	store := presets.New()
	_ = store.SetSlot(presets.Preset{Slot: 3, Name: "Good", StreamURL: "http://radio.example/live"})
	// Slot 4 was poisoned with slot 3's box-visible proxy URL.
	_ = store.SetSlot(presets.Preset{Slot: 4, Name: "Poisoned", StreamURL: "http://127.0.0.1:8888/stream/3"})
	// Slot 5 stores ITSELF: the origin is unrecoverable.
	_ = store.SetSlot(presets.Preset{Slot: 5, Name: "Lost", StreamURL: "http://127.0.0.1:8888/stream/5"})

	s := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if got := s.resolvePresetURL(4, "http://127.0.0.1:8888/stream/3"); got != "http://radio.example/live" {
		t.Fatalf("slot 4 must heal to slot 3's origin, got %q", got)
	}
	if got := s.resolvePresetURL(5, "http://127.0.0.1:8888/stream/5"); got != "http://127.0.0.1:8888/stream/5" {
		t.Fatalf("self-referential slot must return unchanged (origin lost), got %q", got)
	}
	if got := s.resolvePresetURL(1, "http://radio.example/other"); got != "http://radio.example/other" {
		t.Fatalf("a real origin URL must pass through untouched, got %q", got)
	}
}
