// Test for the agent side of the #487 heal: OnStandbyExit schedules the
// forced key re-sync (rate-limited by the existing routine budget).

package main

import (
	"context"
	"log/slog"
	"testing"
)

func TestOnStandbyExitSchedulesResync(t *testing.T) {
	h := &presetWsHandler{logger: slog.Default(), resync: newResyncAsks()}
	h.OnStandbyExit(context.TODO())
	if !h.resync.ask.Load() {
		t.Fatal("standby exit must schedule the forced key re-sync (#487)")
	}
}
