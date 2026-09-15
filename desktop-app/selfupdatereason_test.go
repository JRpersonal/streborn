package main

import (
	"runtime"
	"strings"
	"testing"
)

// shorty310's Mac, discussion #916. Two updates ended in "open the disk image
// and drag ST Reborn into Applications" and one, two days later, replaced
// /Applications/ST Reborn.app in place without asking. Four conditions can
// produce the first outcome and the app recorded none of them, so three days of
// thread went on guesses about a machine neither of us can see, and I told them
// their Mac says no when their own log from that morning says yes.
//
// This pins the part that makes the next report answerable in one reading: the
// app always says where it runs from and, when it cannot update itself, which
// condition said no.
func TestTheAppAlwaysSaysWhetherItCanUpdateItself(t *testing.T) {
	path, reason, ok := SelfUpdateState()

	if reason == "" {
		t.Fatal("no reason at all; a bare true/false is exactly what made #916 unanswerable")
	}
	if ok && reason != "ok" {
		t.Errorf("can self-update but reason = %q, want %q", reason, "ok")
	}
	if !ok && reason == "ok" {
		t.Error(`cannot self-update but the reason says "ok"`)
	}
	// The reason is read by a human out of a log line, and grepped for across
	// bundles, so it has to be a stable token rather than a sentence.
	if strings.ContainsAny(reason, " \t\"") {
		t.Errorf("reason %q is prose; it has to be a stable token", reason)
	}

	if runtime.GOOS != "darwin" {
		// Every other platform updates without the bundle swap, and says so
		// rather than pretending the question applies.
		if reason != "not_macos" {
			t.Errorf("reason on %s = %q, want %q", runtime.GOOS, reason, "not_macos")
		}
		if ok {
			t.Error("the darwin-only report claims it applies here")
		}
		if path != "" {
			t.Errorf("path = %q on %s, want empty", path, runtime.GOOS)
		}
	}
}
