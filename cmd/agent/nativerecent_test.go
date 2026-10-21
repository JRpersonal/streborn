package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// nativeRecentHandler builds a hardware-press handler whose store holds p and
// whose now_playing source answers from src, recording every Recently-played
// note. The watch timings are shortened for the test.
func nativeRecentHandler(t *testing.T, p presets.Preset, src func() string) (*presetWsHandler, *atomic.Int32) {
	t.Helper()
	origWait, origPoll := nativeRecentWait, nativeRecentPoll
	nativeRecentWait, nativeRecentPoll = 200*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { nativeRecentWait, nativeRecentPoll = origWait, origPoll })
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatalf("presets.Load: %v", err)
	}
	if err := store.SetSlot(p); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}
	var notes atomic.Int32
	h := &presetWsHandler{
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		store:       store,
		boxSourceFn: src,
		noteRecentPreset: func(got presets.Preset) {
			if got.Native == nil || got.Native.Location != p.Native.Location {
				t.Errorf("noted %+v, want the pressed key", got)
			}
			notes.Add(1)
		},
	}
	return h, &notes
}

// A Pandora key on the speaker gets its Recently-played card once the speaker
// switched to Pandora, which is what the card's replay needs (v1.0.15 promised
// it, but only the Find tab recorded one).
func TestHardwarePandoraKeyNotesRecentAfterTheSpeakerSwitched(t *testing.T) {
	p := nativeTestPreset(4)
	var probes atomic.Int32
	h, notes := nativeRecentHandler(t, p, func() string {
		if probes.Add(1) < 3 {
			return "INTERNET_RADIO" // the previous source, before the switch lands
		}
		return "PANDORA"
	})
	seq := h.pressSeq.Add(1)
	h.recallPreset(context.Background(), seq, time.Now(), 4, "4071226281950183516", "Little Big Town Radio")
	if n := notes.Load(); n != 1 {
		t.Fatalf("notes = %d, want 1", n)
	}
}

// A key the speaker never switched to (account gone, service down) leaves no
// card behind.
func TestHardwarePandoraKeyRefusedNotesNothing(t *testing.T) {
	h, notes := nativeRecentHandler(t, nativeTestPreset(4), func() string { return "STANDBY" })
	seq := h.pressSeq.Add(1)
	h.recallPreset(context.Background(), seq, time.Now(), 4, "4071226281950183516", "")
	if n := notes.Load(); n != 0 {
		t.Fatalf("notes = %d, want 0", n)
	}
}

// A newer press ends the watch: the card belongs to what the user chose last.
func TestHardwarePandoraKeySupersededNotesNothing(t *testing.T) {
	var h *presetWsHandler
	h, notes := nativeRecentHandler(t, nativeTestPreset(4), func() string {
		h.pressSeq.Add(1) // the user pressed another key meanwhile
		return "PANDORA"
	})
	seq := h.pressSeq.Add(1)
	h.recallPreset(context.Background(), seq, time.Now(), 4, "4071226281950183516", "")
	if n := notes.Load(); n != 0 {
		t.Fatalf("notes = %d, want 0", n)
	}
}

// iHeartRadio has no replay path, so its key is not even watched.
func TestHardwareIHeartKeyNotesNothing(t *testing.T) {
	p, _ := presets.NewNativePreset(5, presets.NativeItem{
		Source: "IHEART", SourceAccount: "listener@example.com", Location: "555", ItemType: "stationurl",
	}, "Hits")
	probed := false
	h, notes := nativeRecentHandler(t, p, func() string { probed = true; return "IHEART" })
	seq := h.pressSeq.Add(1)
	h.recallPreset(context.Background(), seq, time.Now(), 5, "555", "Hits")
	if n := notes.Load(); n != 0 || probed {
		t.Fatalf("notes = %d probed = %v, want neither", n, probed)
	}
}
