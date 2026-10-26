package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/presets"
)

// outcomeHandler is a handler wired for notePresetOutcome with every
// "the press played" signal reading no, and the speaker on a source that is
// not playing.
func outcomeHandler() (*presetWsHandler, *string, *bool) {
	src, playing := "INVALID_SOURCE", false
	h := &presetWsHandler{
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		store:            presets.New(),
		standbyStopAfter: func(time.Time) bool { return false },
		slotPulled:       func(int, time.Time) bool { return false },
		sourceAndPlaying: func(context.Context) (string, bool, bool) {
			return src, playing, true
		},
	}
	return h, &src, &playing
}

func shortOutcomeWait(t *testing.T) {
	old := presetOutcomeAfter
	presetOutcomeAfter = time.Millisecond
	t.Cleanup(func() { presetOutcomeAfter = old })
}

// runOutcome makes one press and runs its outcome check to the end.
func runOutcome(h *presetWsHandler, pressAt time.Time) {
	seq := h.pressSeq.Add(1)
	h.notePresetOutcome(context.Background(), seq, pressAt, 5, "Station")
}

func TestPresetOutcomeFailedWhenNothingPlayed(t *testing.T) {
	shortOutcomeWait(t)
	notices := swapShowNotice(t)
	h, _, _ := outcomeHandler()
	runOutcome(h, time.Now())
	if n := notices.only(t, oled.NoticePresetFailed, 5); n.Line2 == "" {
		t.Fatalf("failure notice without the station name: %+v", n)
	}
}

// The field case (Portable bundle 2026-10-26): the station played, then the
// remote switched the speaker off before the check. No FAILED on the panel of
// a speaker in standby, whichever of the signals answers first.
func TestPresetOutcomeNoNoticeAfterPowerOff(t *testing.T) {
	cases := []struct {
		name string
		set  func(h *presetWsHandler, src *string, pressAt time.Time)
	}{
		{"power-off seen after the press", func(h *presetWsHandler, _ *string, pressAt time.Time) {
			h.standbyStopAfter = func(t time.Time) bool { return t.Equal(pressAt) }
		}},
		{"speaker in standby", func(_ *presetWsHandler, src *string, _ time.Time) {
			*src = "STANDBY"
		}},
		{"stream pulled for this press", func(h *presetWsHandler, _ *string, pressAt time.Time) {
			h.slotPulled = func(slot int, t time.Time) bool { return slot == 5 && t.Equal(pressAt) }
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			shortOutcomeWait(t)
			notices := swapShowNotice(t)
			h, src, _ := outcomeHandler()
			pressAt := time.Now()
			c.set(h, src, pressAt)
			runOutcome(h, pressAt)
			notices.none(t)
		})
	}
}

func TestPresetOutcomeOtherStandDowns(t *testing.T) {
	t.Run("playing", func(t *testing.T) {
		shortOutcomeWait(t)
		notices := swapShowNotice(t)
		h, src, playing := outcomeHandler()
		*src, *playing = "UPNP", true
		runOutcome(h, time.Now())
		notices.none(t)
	})
	// The press switched the source (the firmware posts a recents record on
	// every source change) but the stream is dead: still a failure.
	t.Run("source changed, nothing playing", func(t *testing.T) {
		shortOutcomeWait(t)
		notices := swapShowNotice(t)
		h, src, _ := outcomeHandler()
		*src = "UPNP"
		runOutcome(h, time.Now())
		notices.only(t, oled.NoticePresetFailed, 5)
	})
	t.Run("speaker did not answer", func(t *testing.T) {
		shortOutcomeWait(t)
		notices := swapShowNotice(t)
		h, _, _ := outcomeHandler()
		h.sourceAndPlaying = func(context.Context) (string, bool, bool) { return "", false, false }
		runOutcome(h, time.Now())
		notices.none(t)
	})
	t.Run("newer press", func(t *testing.T) {
		shortOutcomeWait(t)
		notices := swapShowNotice(t)
		h, _, _ := outcomeHandler()
		seq := h.pressSeq.Add(1)
		h.pressSeq.Add(1)
		h.notePresetOutcome(context.Background(), seq, time.Now(), 5, "Station")
		notices.none(t)
	})
	t.Run("only playingNow wired", func(t *testing.T) {
		shortOutcomeWait(t)
		notices := swapShowNotice(t)
		h, _, _ := outcomeHandler()
		h.sourceAndPlaying = nil
		h.playingNow = func(context.Context) bool { return false }
		runOutcome(h, time.Now())
		notices.only(t, oled.NoticePresetFailed, 5)
	})
}
