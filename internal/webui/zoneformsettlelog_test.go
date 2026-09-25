package webui

import (
	"strings"
	"testing"
)

// srcOf reads a source file for the shape assertions below, via the existing
// readSourceFile helper.
func srcOf(t *testing.T, name string) string {
	t.Helper()
	b, err := readSourceFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

// A user reports that forming a TWO-speaker group briefly interrupts the music,
// while adding the 3rd and 4th does not. The asymmetry is real and the code
// explains it: a fresh form carries survivorReachesMembers=false, so the
// "did the stream survive?" question is never asked and the re-push always
// fires; an incremental join asks, sees the master still playing, and skips.
//
// What the code could NOT answer is whether the master kept playing through
// that particular speaker's /setZone, because the state was read only inside
// the branch that skips. On a fresh form nothing was recorded at all, so a
// diagnostic bundle could not tell a firmware tear-down apart from our own
// re-push, and those two want opposite fixes.
//
// This asserts the reading happens on every path and changes no decision.

func TestTheFormSettleStateIsLoggedOnEveryPath(t *testing.T) {
	src := srcOf(t, "zones_stereo.go")
	at := strings.Index(src, "func (s *Server) resumeAfterZoneForm")
	if at < 0 {
		t.Fatal("resumeAfterZoneForm not found")
	}
	body := src[at : at+3000]

	logAt := strings.Index(body, "zone: master state after the form settle")
	if logAt < 0 {
		t.Fatal("the settle state is not logged at all")
	}
	branchAt := strings.Index(body, "if rz.survivorReachesMembers {")
	if branchAt < 0 {
		t.Fatal("the survivor branch is gone; this test is describing code that no longer exists")
	}
	if logAt > branchAt {
		t.Error("the settle state is logged INSIDE or after the survivor branch, so a fresh form still leaves no trace")
	}

	// The four facts a bundle needs to tell a tear-down from our own re-push.
	for _, want := range []string{`"source"`, `"playStatus"`, `"location"`, `"wouldPush"`, `"incremental"`} {
		if !strings.Contains(body[logAt:branchAt+200], want) {
			t.Errorf("the settle log does not carry %s", want)
		}
	}
}

// The decision must be untouched: this change is instrumentation, and the zone
// path has regressed here repeatedly. The skip still asks boxPlayState and still
// requires busy and not standby, exactly as before.
func TestTheSettleLogDidNotChangeTheDecision(t *testing.T) {
	src := srcOf(t, "zones_stereo.go")
	at := strings.Index(src, "func (s *Server) resumeAfterZoneForm")
	body := src[at : at+3000]

	branchAt := strings.Index(body, "if rz.survivorReachesMembers {")
	if branchAt < 0 {
		t.Fatal("the survivor branch is gone")
	}
	decision := body[branchAt : branchAt+320]
	if !strings.Contains(decision, "s.boxPlayState()") {
		t.Error("the skip no longer asks the box its play state")
	}
	if !strings.Contains(decision, "busy && !standby") {
		t.Error("the skip condition changed; it must still be busy AND not standby")
	}
	if !strings.Contains(decision, "stream survived the group change") {
		t.Error("the skip's own log line is gone, so a bundle can no longer see the skip happen")
	}
}
