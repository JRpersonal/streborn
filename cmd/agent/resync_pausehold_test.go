package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Field bundle, ST10, 2026-10-31: a forced re-sync held while an STR file
// played ran seven seconds after the listener paused it. IsPlaying is
// PLAY/BUFFERING only and UPNP is a resync-safe source, so the paused file read
// as a free box; the native preset write then switched the speaker from UPNP to
// LOCAL_INTERNET_RADIO, off the paused file.

func TestAPausedPlayHoldsTheForcedPass(t *testing.T) {
	now := time.Now()
	playing, paused := false, true
	for _, src := range []string{"UPNP", "LOCAL_INTERNET_RADIO", "INVALID_SOURCE"} {
		// playing||paused, as both write paths pass it.
		if !forcedWriteBusy(src, playing || paused, true) {
			t.Errorf("a paused play on %s read as a free box", src)
		}
		if hold, ceiling := forcedWriteHold(src, playing || paused, true, true, time.Time{}, now); !hold || ceiling {
			t.Errorf("a paused play on %s did not hold the forced pass: hold=%v ceiling=%v", src, hold, ceiling)
		}
		if got := forcedHoldReason(src, playing, paused, true); got != "paused" {
			t.Errorf("forcedHoldReason(%s, paused) = %q, want paused", src, got)
		}
	}
	// Stopped is still free: the pass runs the moment the file is stopped.
	if forcedWriteBusy("UPNP", false, true) {
		t.Error("a stopped UPNP file must not hold the pass")
	}
}

// A speaker left paused for hours is bounded by the same ceiling, and runs the
// pass there only when keys are actually missing.
func TestAPausedPlayIsBoundedByTheCeiling(t *testing.T) {
	now := time.Now()
	hold, ceiling := forcedWriteHold("UPNP", true, true, true, now.Add(-forcedPlayHoldCeiling), now)
	if hold || !ceiling {
		t.Fatalf("at the ceiling a paused play must hand over to the ceiling check: hold=%v ceiling=%v", hold, ceiling)
	}
	run, missing, err := ceilingRunsPass(false, func() ([]int, error) { return []int{3}, nil })
	if !run || len(missing) != 1 || err != nil {
		t.Fatalf("a missing key must run the pass at the ceiling: run=%v missing=%v err=%v", run, missing, err)
	}
	if run, _, _ := ceilingRunsPass(false, func() ([]int, error) { return nil, nil }); run {
		t.Fatal("with every key listed the pass must keep waiting")
	}
	if run, _, _ := ceilingRunsPass(false, func() ([]int, error) { return nil, errors.New("unreadable") }); run {
		t.Fatal("an unreadable list must keep the pass waiting")
	}
}

// The wiring a pure helper test cannot see: both write paths read the pause
// and hand it to the busy check.
func TestBothWritePathsReadThePause(t *testing.T) {
	b, err := os.ReadFile("reconcile.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "forcedWriteHold(src, playing || paused, playKnown,") {
		t.Error("the forced pass no longer counts a paused play as busy")
	}
	if !strings.Contains(src, "boxSourcePlayState(boxHost); forcedWriteBusy(src, playing || paused, playKnown)") {
		t.Error("the routine pass no longer counts a paused play as busy")
	}
}
