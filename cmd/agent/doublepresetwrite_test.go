package main

import (
	"testing"
	"time"
)

// Two writers used to push every preset slot on each agent start, fifteen
// seconds apart, neither knowing about the other. Measured 2026-09-29:
//
//	17:16:57  preset reconcile: full re-sync after box became ready  slots=3
//	17:17:12  starting initial box preset sync                       count=3
//
// The firmware side confirmed six writes rather than three, and the same bundle
// repeats it on the next boot. That is avoidable NAND wear on every speaker, on
// every start, on hardware nobody can replace.
func TestAFullPassStandsTheInitialSyncDown(t *testing.T) {
	t.Cleanup(func() { fullPresetWriteAt.Store(0) })

	// The measured gap: the reconcile writes, the initial sync follows.
	fullPresetWriteAt.Store(time.Now().Unix() - 15)
	if !initialSyncShouldStandDown() {
		t.Error("the initial sync would write every slot again fifteen seconds after a full pass")
	}
}

// An old stamp must not silence it forever: a speaker whose presets were written
// an hour ago and has since rebooted needs the initial sync to run.
func TestAnOldPassDoesNotSilenceTheInitialSync(t *testing.T) {
	t.Cleanup(func() { fullPresetWriteAt.Store(0) })

	for _, ago := range []int64{121, 600, 3600} {
		fullPresetWriteAt.Store(time.Now().Unix() - ago)
		if initialSyncShouldStandDown() {
			t.Errorf("a pass %d seconds ago still suppresses the initial sync", ago)
		}
	}
}

// A fresh agent has never written anything, so the initial sync must run.
func TestWithNoPassAtAllTheInitialSyncRuns(t *testing.T) {
	t.Cleanup(func() { fullPresetWriteAt.Store(0) })
	fullPresetWriteAt.Store(0)
	if initialSyncShouldStandDown() {
		t.Error("the initial sync stood down although nothing has ever written the slots")
	}
}
