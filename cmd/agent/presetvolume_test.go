package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// A hardware press asks for the key's own volume (#1323) whatever plays the
// key, a native station included, and a press that a newer one overtook
// does not.
func TestHardwarePressAsksForTheKeyVolume(t *testing.T) {
	var got []int
	h := &presetWsHandler{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		store:        presets.New(),
		presetVolume: func(slot int, why string) { got = append(got, slot) },
	}
	seq := h.pressSeq.Add(1)
	h.recallPreset(context.Background(), seq, time.Now(), 3, "/station?data=example", "Station")
	if len(got) != 1 || got[0] != 3 {
		t.Fatalf("presetVolume calls %v, want [3]", got)
	}
	got = nil
	old := seq
	h.pressSeq.Add(1)
	h.recallPreset(context.Background(), old, time.Now(), 4, "/station?data=example", "Station")
	if len(got) != 0 {
		t.Fatalf("a superseded press asked for its volume: %v", got)
	}
}
