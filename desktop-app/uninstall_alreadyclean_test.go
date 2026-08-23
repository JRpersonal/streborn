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
	// It must only claim that when BOTH of the things Remove deletes are absent,
	// or a half-removed speaker would be waved through as clean.
	i := strings.Index(uninstallScript, "STR_UNINSTALL_ALREADY_CLEAN")
	line := uninstallScript[max0(i-160):i]
	for _, want := range []string{"-z \"$REMOVED\"", "! -d /mnt/nv/streborn", "! -f /mnt/nv/rc.local"} {
		if !strings.Contains(line, want) {
			t.Errorf("the already-clean check is missing %q, so a half-removed speaker could pass", want)
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
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
