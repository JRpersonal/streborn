package main

import (
	"strings"
	"testing"
	"time"
)

// A speaker that comes back from an update on OLDER software than this app
// carries has not been updated, whatever else its answer says.
//
// Field case 2026-11-09 (SoundTouch 10, NAND tight): the journal of a v1.5.4
// app said "confirmed: box is on build 2026-11-03-2355", which is the v1.5.1
// build the speaker had before. The app then reported the update as done,
// dropped its engine re-delivery bookkeeping, and the next attempt found a
// speaker whose agent no longer started. The "no longer the pre-OTA build"
// and "no longer the pre-OTA version" rules accept ANY change, so a reading
// that moved backwards (a stick boot-sync revert, a swap that fell back, a
// pre-read that saw a not-yet-persisted newer agent) counted as success.

func TestUpdateConfirmsNeverAcceptsOlderSoftware(t *testing.T) {
	const (
		app    = "v1.5.4"
		appB   = "2026-11-08-2036"
		oldVer = "v1.5.1"
		oldB   = "2026-11-03-2355"
	)
	cases := []struct {
		name                 string
		preBuild, preVersion string
		v                    map[string]string
		want                 bool
	}{
		{"a build that moved BACKWARDS from the pre-update reading is not the update",
			appB, app,
			map[string]string{"version": oldVer, "build": oldB, "agentRunningSha256": testOtherSha}, false},
		{"a version that moved, but to one older than this app's, is not the update",
			"", app,
			map[string]string{"version": oldVer}, false},
		{"a running checksum that names this app's agent while the version is older is a lying checksum",
			oldB, oldVer,
			map[string]string{"version": oldVer, "build": oldB, "agentRunningSha256": testAppSha}, false},
		// The rules the guard must not break.
		{"the new agent with a build stamp a minute off the app's still confirms",
			oldB, oldVer,
			map[string]string{"version": app, "build": "2026-11-08-2035"}, true},
		{"the new agent by its running checksum still confirms",
			oldB, oldVer,
			map[string]string{"version": app, "build": "2026-11-08-2035", "agentRunningSha256": testAppSha}, true},
		{"an agent too old to report a build still confirms when its version reached this app's",
			"", oldVer,
			map[string]string{"version": app}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := updateConfirms(c.v, testAppSha, appB, app, c.preBuild, c.preVersion); got != c.want {
				t.Fatalf("updateConfirms = %v, want %v", got, c.want)
			}
		})
	}
}

// An unstamped dev build reports 1.0.0 and knows nothing about which version
// is newer, so it keeps the old rules.
func TestUpdateConfirmsDevBuildKeepsTheOldRules(t *testing.T) {
	v := map[string]string{"version": "v0.9.87", "build": "2026-09-25-1715"}
	if !updateConfirms(v, "", "dev", "1.0.0", "", "v0.9.86") {
		t.Fatal("a dev build has no version to compare against and must not refuse a moved version")
	}
}

// The whole sequence, as the 2026-11-09 journal shows it: the reading before
// the push already names this app's build (the speaker ran it, but not from
// its disk), the push reboots the speaker and it comes back on the build it
// had before that. The run must not end "confirmed: box is on build <old>".
func TestSpeakerUpdateDoesNotConfirmARegression(t *testing.T) {
	fb := newOtaFakeBox()
	regressed := func() map[string]string {
		return map[string]string{"version": testOldVer, "build": testOldBuild, "agentRunningSha256": testOtherSha, "goLibrespot": "present", "uptimeSec": "60"}
	}
	fb.versionAt = func(t time.Time) (map[string]string, error) {
		pt := fb.pushTime()
		if pt.IsZero() {
			// Pre-update: this app's agent answers, its engine is present, so
			// the agent push is not skipped.
			return newVersion("present"), nil
		}
		since := t.Sub(pt)
		switch {
		case since < 4*time.Second:
			// The agent that took the upload, still up for its 1.5 s + 2 s
			// before the reboot.
			return newVersion("present"), nil
		case since < 64*time.Second:
			return nil, errUnreachable
		default:
			return regressed(), nil
		}
	}
	fb.verdicts = []string{"not-landed"}
	out, err := runSingle(t, fb)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range fb.outcomes {
		if strings.HasPrefix(o, "confirmed") && strings.Contains(o, testOldBuild) {
			t.Fatalf("journal confirms the update on the OLD build: %q (all: %v)", o, fb.outcomes)
		}
	}
	if out.Confirmed || out.Outcome == outcomeDone {
		t.Fatalf("a speaker that came back on older software is not updated: %+v", out)
	}
}

// The classifier's checksum arm has the same hole: a running checksum that
// names this app's agent next to an older version string.
func TestClassifyAgentVersionDistrustsAChecksumNextToAnOlderVersion(t *testing.T) {
	ver := map[string]string{"version": "v1.5.1", "build": "2026-11-03-2355", "agentRunningSha256": testAppSha}
	verdict, _ := classifyAgentVersion(ver, testAppSha, "2026-11-08-2036", "v1.5.4")
	if verdict == "confirmed" {
		t.Fatal("an agent that reports an older version is not this app's agent, whatever its checksum says")
	}
	verdict, _ = classifyAgentVersion(map[string]string{"version": "v1.5.4", "build": "2026-11-08-2035", "agentRunningSha256": testAppSha}, testAppSha, "2026-11-08-2036", "v1.5.4")
	if verdict != "confirmed" {
		t.Fatalf("the new agent by its checksum: got %q", verdict)
	}
}

// The same hole before the push: a speaker that reports an older version next
// to this app's checksum was taken as "already on this agent, only the Spotify
// engine is missing", so it got the engine and never the agent, and the run
// ended as updated.
func TestAnOlderVersionIsNeverAlreadyCurrent(t *testing.T) {
	target := otaTarget{sha: testAppSha, build: testAppBuild, version: testAppVer}
	stale := map[string]string{"version": testOldVer, "build": testOldBuild, "agentRunningSha256": testAppSha, "goLibrespot": "missing"}
	if target.onlyEngineMissing(stale) {
		t.Fatal("a speaker that reports an older version is not on this app's agent, whatever its checksum says")
	}

	fb := newOtaFakeBox()
	fb.versionAt = func(now time.Time) (map[string]string, error) {
		pt := fb.pushTime()
		switch {
		case pt.IsZero():
			return stale, nil
		case now.Sub(pt) < 40*time.Second:
			return nil, errUnreachable
		default:
			return newVersion("present"), nil
		}
	}
	out, err := runSingle(t, fb)
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.pushes) != 1 {
		t.Fatalf("the agent was pushed %d time(s) to a speaker on older software, want 1 (journal: %v)", len(fb.pushes), fb.outcomes)
	}
	if !out.Confirmed || out.Outcome != outcomeDone {
		t.Fatalf("the pushed update took: %+v", out)
	}
}

// And after the window: the poll refuses a reading on older software for six
// minutes, so the classifier asked right after it must not confirm the same
// reading on its build stamp.
func TestClassifyDoesNotConfirmAnOlderVersionOnItsBuildStamp(t *testing.T) {
	target := otaTarget{sha: testAppSha, build: testAppBuild, version: testAppVer}
	v := map[string]string{"version": testOldVer, "build": testAppBuild}
	if target.confirms(v) {
		t.Fatal("the poll confirmed older software")
	}
	if verdict, _ := target.classify(v); verdict == "confirmed" {
		t.Fatal("confirmed late on a reading the poll refused: the version is older than this app's")
	}
	if verdict, _ := target.classify(map[string]string{"version": testAppVer, "build": testAppBuild}); verdict != "confirmed" {
		t.Fatalf("an agent that reports only version and build, both this app's: got %q", verdict)
	}
}
