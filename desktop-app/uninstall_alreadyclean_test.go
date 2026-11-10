package main

import (
	"strings"
	"testing"
)

// Pressing Remove on a speaker that is already clean is the most ordinary thing
// a user does: the first run gave no visible confirmation, so they press it
// again. That produced a red error, and the early return behind it skipped
// everything after: the app kept remembering a now-stock speaker as an STR one,
// and the reboot that closes root SSH never ran.
//
// A reporter hit it on 2026-08-23 and then asked exactly the question the bug
// produces: why did one speaker flip to "READY FOR STR" on its own while the
// other needed the app restarted. The first came off in one go. The second was
// a second attempt.

func TestUninstallScriptReportsAnAlreadyCleanSpeaker(t *testing.T) {
	// The Go side cannot tell "removed nothing because it failed" from "removed
	// nothing because there was nothing there" without the speaker saying so.
	if !strings.Contains(uninstallScript, "STR_UNINSTALL_ALREADY_CLEAN") {
		t.Fatal("the script no longer reports an already-clean speaker; the Go side cannot tell it from a silent failure")
	}
	// It must only claim that when nothing was removed AND nothing is left, or
	// a half-removed speaker would be waved through as clean: the claim sits in
	// the branch the leftover check did not take. (What the script really
	// reports on a half-removed tree is run in uninstall_leftover_test.go.)
	left := strings.Index(uninstallScript, `if [ -n "$LEFT" ]; then`)
	clean := strings.Index(uninstallScript, `elif [ -z "$REMOVED" ]; then`)
	i := strings.Index(uninstallScript, `echo "STR_UNINSTALL_ALREADY_CLEAN"`)
	if left < 0 || clean < left || i < clean {
		t.Error("the already-clean report is no longer tied to \"nothing removed and nothing left\"")
	}
	for _, want := range []string{"/mnt/nv/streborn", "/mnt/nv/rc.local"} {
		if !strings.Contains(uninstallScript[strings.Index(uninstallScript, `LEFT=""`):left], want) {
			t.Errorf("the leftover check does not look at %s, so a half-removed speaker could pass", want)
		}
	}
}

func TestUninstallStillFailsLoudlyWhenTheScriptSaysNothing(t *testing.T) {
	// The guard this replaces existed for a real case: a script that failed
	// silently. Output with neither marker must still be a failure, or a broken
	// uninstall would report success and leave STR running.
	out := "some unrelated chatter\n"
	if strings.Contains(out, "STR_UNINSTALL_ALREADY_CLEAN") {
		t.Fatal("fixture is wrong")
	}
	if strings.Contains(out, "STR_UNINSTALL_REMOVED:") {
		t.Fatal("fixture is wrong")
	}
	if got := parseUninstallOutput(out).outcome(); got != uninstallSilent {
		t.Errorf("outcome = %v, want the silent failure", got)
	}
}
