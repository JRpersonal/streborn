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

// The settle reading feeds the decision now, so the earlier "changed nothing"
// assertion has been replaced by the invariants that must survive the change.
//
// The gate is still the box's own play state, the confirmed-incremental skip is
// untouched, and the fresh-form path asks the MEMBERS rather than inferring
// from the master. That last one is the whole guard against Martin's silent
// members coming back.
func TestTheFreshFormSkipStillAsksTheBoxAndTheMembers(t *testing.T) {
	src := srcOf(t, "zones_stereo.go")
	at := strings.Index(src, "func (s *Server) resumeAfterZoneForm")
	body := src[at : at+4200]

	gateAt := strings.Index(body, "if standby, busy := s.boxPlayState(); busy && !standby {")
	if gateAt < 0 {
		t.Fatal("the outer gate no longer asks the box for busy AND not standby")
	}
	incrAt := strings.Index(body, "if rz.survivorReachesMembers {")
	if incrAt < 0 || incrAt < gateAt {
		t.Fatal("the confirmed-incremental skip is gone or no longer sits inside the play-state gate")
	}
	if !strings.Contains(body[incrAt:incrAt+320], "stream survived the group change") {
		t.Error("the incremental skip's log line is gone, so a bundle can no longer see it happen")
	}
	// The fresh-form half.
	if !strings.Contains(body, "everyMemberCarriesTheGroup") {
		t.Error("the fresh-form path does not ask the members; that is the only thing keeping Martin's case safe")
	}
	if !strings.Contains(body, "masterResumeForZone(settleNP, rz.ref)") {
		t.Error("the fresh-form path does not re-check the master through masterResumeForZone; a string compare would be wrong across the two location encodings")
	}
}
