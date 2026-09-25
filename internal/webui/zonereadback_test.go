// A failed zone read-back used to answer {"ok":true} and return, which skipped
// the follower verification and, worse, the post-form resume. The group was live
// with nobody pushing the stream to it, so the members stayed silent while the
// app reported success. The firmware's own zone read is exactly what hangs on
// some chassis, so this is not a rare path.

package webui

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func zoneFormSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("zones_stereo.go")
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

func TestAFailedZoneReadBackNeverReturnsPastTheResume(t *testing.T) {
	src := zoneFormSource(t)

	// Both read-backs, the first form and the re-form fallback, are followed by
	// the same recovery rather than an early answer.
	if n := strings.Count(src, "getZone read-back failed"); n != 2 {
		t.Fatalf("expected both read-back sites to be handled, found %d", n)
	}
	if strings.Contains(src, `writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "native"})`) {
		t.Error("a read-back failure still answers ok:true and returns, skipping the resume")
	}
	if n := strings.Count(src, "z2 = boxapi.Zone{Master: master.DeviceID, Members: slaves}"); n != 2 {
		t.Errorf("expected both sites to fall back to the requested members, found %d", n)
	}
}

func TestTheAnswerSaysTheReadBackDidNotHappen(t *testing.T) {
	src := zoneFormSource(t)
	if !strings.Contains(src, `out["readBack"] = "failed"`) {
		t.Error("a caller cannot tell a verified group from an unverified one")
	}
	// And the flag is only set on the failure path.
	if n := strings.Count(src, "readBackFailed = true"); n != 2 {
		t.Errorf("readBackFailed is set %d times, want once per read-back site", n)
	}
}

// The resume must still run, because it is the half that makes the group
// audible. It is gated on masterFormed, which a synthesised zone satisfies only
// when the caller actually asked for members.
func TestTheResumeStillRunsAfterAFailedReadBack(t *testing.T) {
	src := zoneFormSource(t)
	i := strings.Index(src, "if resume != nil && masterFormed {")
	if i < 0 {
		t.Fatal("the post-form resume is gone")
	}
	// Nothing between the read-back recovery and the resume may return early.
	between := src[strings.Index(src, "readBackFailed = true"):i]
	if regexp.MustCompile(`\n\t\treturn\n`).MatchString(between) {
		t.Error("a path between the read-back and the resume returns early again")
	}
	if !strings.Contains(src, "members: z2.Members") {
		t.Error("the resume no longer receives the member list")
	}
}
