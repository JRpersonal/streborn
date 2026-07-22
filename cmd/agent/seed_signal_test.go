package main

import (
	"io"
	"log/slog"
	"testing"
)

// TestSeedFirstAttemptSignalFiresOnEarlyReturn: autopair's start waits
// (bounded) for the preset recovery's first box read, so the first forced
// re-assert cannot wipe the box preset list before the recovery snapshots it
// (#252 warm-restart race). The signal must fire even when the seed exits
// early (no box host / no store), or every stickless start would sit out the
// fallback timeout for nothing.
func TestSeedFirstAttemptSignalFiresOnEarlyReturn(t *testing.T) {
	fired := 0
	seedBoxPresetsAndRecoverStore(nil, nil, "", nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() { fired++ })
	if fired != 1 {
		t.Fatalf("the first-attempt signal must fire exactly once on an early return, got %d", fired)
	}
}
