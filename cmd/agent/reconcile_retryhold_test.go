package main

import "testing"

// The retry gate on a sleeping box: a pending ask is deferred, not left armed.
// Leaving it armed is what made the maintenance wait exit immediately and the
// loop spin at one /now_playing read per pass (field bundle 2026-09-07, two
// ST10s at ~25 reads a second for hours).
func TestRetryHoldDefersAPendingAskOnASleepingBox(t *testing.T) {
	for _, src := range []string{"STANDBY", ""} {
		if got := retryHoldDecision(src, true, false); got != retryHoldDefer {
			t.Errorf("src=%q ask pending, no standby-OK: got %v, want defer", src, got)
		}
		if got := retryHoldDecision(src, false, false); got != retryHoldDefer {
			t.Errorf("src=%q no ask: got %v, want defer", src, got)
		}
		// standby-OK without an ask has nothing to run.
		if got := retryHoldDecision(src, false, true); got != retryHoldDefer {
			t.Errorf("src=%q standby-OK but no ask: got %v, want defer", src, got)
		}
	}
}

// Repeated dead-key presses (the standby-OK grant) are user-present evidence,
// so that one ask still runs against a STANDBY reading.
func TestRetryHoldRunsAStandbyOKAsk(t *testing.T) {
	if got := retryHoldDecision("STANDBY", true, true); got != retryHoldRunAsk {
		t.Errorf("got %v, want run", got)
	}
}
