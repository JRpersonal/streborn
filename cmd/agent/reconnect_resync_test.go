// Regression tests for the deep-standby fix (#119): a gabbo reconnect while
// the box idles in STANDBY must NOT schedule the forced AddPreset re-sync
// (those blind writes every ~12 min reset the firmware's deep-standby
// countdown forever), while any other box state - and a failed probe - must
// keep the pre-existing unconditional ask so the dead-key heal never regresses.

package main

import (
	"log/slog"
	"testing"
)

func TestReconnectResync_SkippedWhileIdleInStandby(t *testing.T) {
	h := &presetWsHandler{
		logger:  slog.Default(),
		resync:  newResyncAsks(),
		boxHost: "127.0.0.1",
		boxSummaryFn: func() (string, string, string) {
			return "STANDBY", "", ""
		},
	}
	h.resyncUnlessIdleStandby()
	if h.resync.ask.Load() {
		t.Fatal("idle-standby reconnect scheduled a forced re-sync; deep standby can never engage")
	}
}

func TestReconnectResync_ForcedWhenAwake(t *testing.T) {
	h := &presetWsHandler{
		logger:  slog.Default(),
		resync:  newResyncAsks(),
		boxHost: "127.0.0.1",
		boxSummaryFn: func() (string, string, string) {
			return "UPNP", "Antenne", "PLAY_STATE"
		},
	}
	h.resyncUnlessIdleStandby()
	if !h.resync.ask.Load() {
		t.Fatal("awake-box reconnect must keep the forced re-sync (dead-key heal)")
	}
}

func TestReconnectResync_ForcedOnProbeFailure(t *testing.T) {
	h := &presetWsHandler{
		logger:  slog.Default(),
		resync:  newResyncAsks(),
		boxHost: "127.0.0.1",
		boxSummaryFn: func() (string, string, string) {
			return "", "", "" // probe failed / box unreachable
		},
	}
	h.resyncUnlessIdleStandby()
	if !h.resync.ask.Load() {
		t.Fatal("unknown box state must fall back to the unconditional ask")
	}
}
