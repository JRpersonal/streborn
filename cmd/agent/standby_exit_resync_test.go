// Test for the agent side of the #487 heal: OnStandbyExit schedules the
// forced key re-sync (rate-limited by the existing routine budget).

package main

import (
	"context"
	"log/slog"
	"testing"
)

func TestOnStandbyExitSchedulesResync(t *testing.T) {
	presetResyncAsk.Store(false)
	presetResyncLast.Store(0)
	h := &presetWsHandler{logger: slog.Default()}
	h.OnStandbyExit(context.TODO())
	if !presetResyncAsk.Load() {
		t.Fatal("standby exit must schedule the forced key re-sync (#487)")
	}
}
