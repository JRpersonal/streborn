package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestUserStopAbortsVerify guards the stand-down decision of the hardware
// recall verifies: only a deliberate user stop that happened strictly AFTER
// the recall started aborts the re-push loop (stop-after-recall-start,
// mirroring the webui side), never an older stop and never a rolling window.
func TestUserStopAbortsVerify(t *testing.T) {
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		recallStart time.Time
		lastStop    time.Time
		want        bool
	}{
		{"no stop ever recorded", base, time.Time{}, false},
		{"stop long before the recall must not suppress it", base, base.Add(-time.Hour), false},
		{"stop just before the recall must not suppress it", base, base.Add(-time.Millisecond), false},
		{"same-instant tie completes the recall (transport-flip transient)", base, base, false},
		{"stop during the verify window aborts", base, base.Add(3 * time.Second), true},
		{"stop long after the recall started aborts", base, base.Add(20 * time.Second), true},
	}
	for _, c := range cases {
		if got := userStopAbortsVerify(c.recallStart, c.lastStop); got != c.want {
			t.Errorf("%s: userStopAbortsVerify(%v, %v) = %v, want %v",
				c.name, c.recallStart, c.lastStop, got, c.want)
		}
	}
}

// TestPresetWsHandlerUserStoppedSince exercises the handler wiring: OnUserStop
// (the gabbo STOP_STATE hook) records the stop time the verifies read, and the
// existing webui forward keeps firing.
func TestPresetWsHandlerUserStoppedSince(t *testing.T) {
	forwarded := 0
	h := &presetWsHandler{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		onUserStop: func() { forwarded++ },
	}

	recallStart := time.Now().Add(-time.Second)
	if h.userStoppedSince(recallStart) {
		t.Fatal("no stop recorded yet, verify must not stand down")
	}

	h.OnUserStop(context.Background())
	if forwarded != 1 {
		t.Fatalf("OnUserStop must still forward to the webui hook, forwarded = %d", forwarded)
	}
	if !h.userStoppedSince(recallStart) {
		t.Fatal("a stop after the recall started must stand the verify down")
	}

	// A recall that starts AFTER the stop is a fresh user request: the old
	// stop must not suppress its verify.
	h.lastUserStopMu.Lock()
	stopTS := h.lastUserStop
	h.lastUserStopMu.Unlock()
	if h.userStoppedSince(stopTS.Add(time.Millisecond)) {
		t.Fatal("a recall newer than the last stop must not be suppressed")
	}
}

// TestPresetWsHandlerOnUserStopNilHook confirms the timestamp recording works
// without the webui forward wired (nil-safe like every other handler hook).
func TestPresetWsHandlerOnUserStopNilHook(t *testing.T) {
	h := &presetWsHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.OnUserStop(context.Background()) // must not panic
	if !h.userStoppedSince(time.Now().Add(-time.Minute)) {
		t.Fatal("stop must be recorded even without the webui hook")
	}
}
