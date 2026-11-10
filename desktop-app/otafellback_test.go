package main

import (
	"testing"
	"time"
)

// The engine step's "nothing left to do" took any version other than the one
// before the push as the updated agent: a step back, and a move to any other
// old version, read as done.
func TestReachedNeverAcceptsAVersionOlderThanTheApp(t *testing.T) {
	target := otaTarget{sha: testAppSha, build: testAppBuild, version: testAppVer, preVersion: "v1.0.18"}
	moved := map[string]string{"version": testOldVer, "agentRunningSha256": testOtherSha, "goLibrespot": "present"}
	if done, missing := target.reached(moved, true); done || missing != "agent" {
		t.Errorf("reached = (%v, %q) for a version that moved but is older than this app's, want (false, agent)", done, missing)
	}
	lying := map[string]string{"version": testOldVer, "agentRunningSha256": testAppSha, "goLibrespot": "present"}
	if done, _ := target.reached(lying, true); done {
		t.Error("reached took this app's checksum next to an older version string")
	}
	if done, _ := target.reached(newVersion("present"), true); !done {
		t.Error("the plain case broke")
	}
	// A dev build pushed over a later-stamped build of the same version is not
	// taken back: only the version counts here.
	dev := otaTarget{sha: testAppSha, build: testAppBuild, version: testAppVer, preBuild: "2026-10-23-1300", preVersion: testAppVer}
	if done, _ := dev.reached(newVersion("present"), true); !done {
		t.Error("an earlier build stamp of the same version was taken back")
	}
}

// The whole run: the update is confirmed, the Spotify engine is still missing,
// and while the app waits to deliver it the speaker restarts onto the software
// it had before (which has its engine). The engine being present must not end
// the run as updated.
func TestSpeakerUpdateDoesNotEndDoneOnASpeakerThatFellBack(t *testing.T) {
	fb := newOtaFakeBox()
	fb.rebootsTo(40*time.Second, func(now time.Time) map[string]string {
		if now.Sub(fb.pushTime()) < 100*time.Second {
			return newVersion("missing")
		}
		return oldVersion()
	})
	fb.verdicts = []string{"not-landed"}
	out, err := runSingle(t, fb)
	if err != nil {
		t.Fatal(err)
	}
	if !fb.hasOutcome("confirmed") {
		t.Fatalf("the script is off: the update was never confirmed in the window (%v)", fb.outcomes)
	}
	if out.Outcome == outcomeDone || out.Confirmed {
		t.Fatalf("a speaker back on older software ended as updated: %+v", out)
	}
	if out.Outcome != outcomeTimeout || out.Verdict != "not-landed" {
		t.Fatalf("outcome %q verdict %q, want timeout / not-landed", out.Outcome, out.Verdict)
	}
	if len(fb.clears) != 0 {
		t.Error("the record of what this speaker should run was cleared")
	}
}

// A classifier that finds the speaker on this app's agent after all overrules
// one odd reading, and the engine step goes on.
func TestSpeakerUpdateGoesOnWhenTheClassifierConfirmsAfterAnOlderReading(t *testing.T) {
	fb := newOtaFakeBox()
	fb.rebootsTo(40*time.Second, func(now time.Time) map[string]string {
		since := now.Sub(fb.pushTime())
		switch {
		case since < 100*time.Second:
			return newVersion("missing")
		case since < 104*time.Second:
			return oldVersion()
		default:
			return newVersion("present")
		}
	})
	fb.verdicts = []string{"confirmed"}
	out, err := runSingle(t, fb)
	if err != nil {
		t.Fatal(err)
	}
	if out.Outcome != outcomeDone || !out.Confirmed {
		t.Fatalf("the speaker is on this app's agent with its engine: %+v", out)
	}
}
