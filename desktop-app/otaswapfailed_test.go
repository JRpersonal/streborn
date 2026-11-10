package main

import (
	"strings"
	"testing"
	"time"
)

// A speaker whose storage could not take the new agent keeps STR alive by
// running the new build from RAM (the rescue agent, internal/webui/
// ota_rescue.go). That agent IS this app's build: same version, same build
// stamp, same running checksum. What tells it from an installed update is the
// one field it adds, otaSwapFailed, and its next restart starts whatever the
// storage holds. So the marker outranks every other sign: an update that is
// not on the speaker's storage is not installed.

const testSwapMarker = "copy to NAND did not verify"

func rescueVersion(engine string) map[string]string {
	v := newVersion(engine)
	v["otaSwapFailed"] = testSwapMarker
	return v
}

func TestAFailedSwapIsNeverTheUpdate(t *testing.T) {
	target := otaTarget{sha: testAppSha, build: testAppBuild, version: testAppVer, preBuild: testOldBuild, preVersion: testOldVer}
	if target.confirms(rescueVersion("present")) {
		t.Error("confirms took the rescue agent for the installed update")
	}
	if verdict, _ := target.classify(rescueVersion("present")); verdict != "swap-failed" {
		t.Errorf("classify = %q for the rescue agent on this app's build stamp, want swap-failed", verdict)
	}
	// The app's next push is the way on from a failed swap, so the push must
	// not be skipped because "the agent is already current".
	if target.onlyEngineMissing(rescueVersion("missing")) {
		t.Error("the agent push would be skipped for a speaker that runs the new build from RAM only")
	}
	// Without the marker all three still say what they said.
	if !target.confirms(newVersion("present")) || !target.onlyEngineMissing(newVersion("missing")) {
		t.Error("the marker rule broke the plain case")
	}
	if verdict, _ := target.classify(newVersion("present")); verdict != "confirmed" {
		t.Errorf("classify = %q for the installed update", verdict)
	}
}

// The whole run: the speaker comes back on the new build with the marker set
// and keeps it. The run must not end "done".
func TestSpeakerUpdateDoesNotConfirmAFailedSwap(t *testing.T) {
	fb := newOtaFakeBox()
	fb.rebootsTo(40*time.Second, func(time.Time) map[string]string { return rescueVersion("present") })
	fb.verdicts = []string{"swap-failed"}
	out, err := runSingle(t, fb)
	if err != nil {
		t.Fatal(err)
	}
	if out.Confirmed || out.Outcome != outcomeTimeout || out.Verdict != "swap-failed" {
		t.Fatalf("a speaker that runs the update from RAM only is not updated: %+v", out)
	}
	if fb.hasOutcome("confirmed") {
		t.Fatalf("journal confirms an update that is not on the speaker's storage: %v", fb.outcomes)
	}
	said := false
	for _, o := range fb.outcomes {
		if strings.Contains(o, "could not write it to its storage") && strings.Contains(o, testSwapMarker) {
			said = true
		}
	}
	if !said {
		t.Errorf("the journal does not say why the answering speaker was not taken as updated: %v", fb.outcomes)
	}
}

// The rescue agent writes itself to the storage two minutes after it started
// and drops the marker when that worked. From then on the update is installed.
func TestSpeakerUpdateConfirmsOnceTheRescueAgentReachedTheStorage(t *testing.T) {
	fb := newOtaFakeBox()
	fb.rebootsTo(40*time.Second, func(now time.Time) map[string]string {
		if now.Sub(fb.pushTime()) < 180*time.Second {
			return rescueVersion("present")
		}
		return newVersion("present")
	})
	out, err := runSingle(t, fb)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Confirmed || out.Outcome != outcomeDone {
		t.Fatalf("the marker cleared inside the window, the update is installed: %+v", out)
	}
	if fb.classifyCalls != 0 {
		t.Errorf("confirmed in the window, yet the classifier was asked %d time(s)", fb.classifyCalls)
	}
}
