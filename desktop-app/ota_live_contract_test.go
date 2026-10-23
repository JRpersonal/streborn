package main

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"streborn-app/agentbin"
)

// intentOnFile reads the update-intent record for host straight off the disk.
// PendingUpdateIntent cannot be asked mid-run: it answers "install or update
// running" while the app is busy, which is exactly when the question is asked.
func intentOnFile(host string) bool {
	intentMu.Lock()
	defer intentMu.Unlock()
	path, err := updateIntentPath()
	if err != nil {
		return false
	}
	_, ok := findIntent(loadUpdateIntentsFrom(path), host, 0)
	return ok
}

// TestLiveUpdateContract checks the promises the update flow makes, against a
// real speaker, without the GUI and without any manual SSH or curl. It drives
// the SAME sequence the button runs, RunSpeakerUpdate, and watches its phases
// through the callback a window would see as events.
//
// What it asserts, in the order the user experiences it:
//  1. the target is written down BEFORE anything is copied (it is on file by
//     the first phase, which precedes the push),
//  2. the app reports itself busy for the whole run, so the window would
//     refuse to close,
//  3. the run ends in "done" with the speaker on the agent this build carries
//     and the Spotify engine present,
//  4. the record is cleared once the speaker is there,
//  5. a failure produces a report that actually names the problem.
//
// STR_LIVE_BOX=<ip> [STR_LIVE_PORT=17008] go test ./... -run LiveUpdateContract -v -timeout 30m
func TestLiveUpdateContract(t *testing.T) {
	host := os.Getenv("STR_LIVE_BOX")
	if host == "" {
		t.Skip("set STR_LIVE_BOX=<ip> to run the live contract test")
	}
	port := 8888
	if p := os.Getenv("STR_LIVE_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	a := NewApp()

	before, err := a.BoxAgentVersion(host, port)
	if err != nil {
		t.Fatalf("baseline read %s:%d: %v", host, port, err)
	}
	t.Logf("BEFORE version=%s build=%s engine=%s nandFree=%s", before["version"], before["build"], before["goLibrespot"], before["nandFreeBytes"])
	name := before["friendlyName"]

	var mu sync.Mutex
	var phases []string
	intentAtFirstPhase, busyThroughout := false, true
	onPhase := func(p updatePhase) {
		mu.Lock()
		defer mu.Unlock()
		if len(phases) == 0 {
			// 1. The first phase precedes the push: the target must already be
			// on file.
			intentAtFirstPhase = intentOnFile(host)
		}
		// 2. Busy at every phase, so the window would ask before closing.
		if !otaRunning.Load() {
			busyThroughout = false
		}
		if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
			phases = append(phases, p.Phase)
			t.Logf("  phase %-16s attempt=%d reachable=%v version=%s engine=%s remaining=%ds",
				p.Phase, p.Attempt, p.Reachable, p.Version, p.Engine, p.RemainingMs/1000)
		}
	}

	// 3. The run itself: exactly what the button calls.
	t.Log("App.RunSpeakerUpdate ...")
	out, runErr := a.runSpeakerUpdate(host, port, before["deviceID"], name, onPhase)
	t.Logf("outcome=%s confirmed=%v verdict=%q engineDelivered=%v engineTooFull=%v unmet=%q attempts=%d err=%q",
		out.Outcome, out.Confirmed, out.Verdict, out.EngineDelivered, out.EngineTooFull, out.Unmet, out.Attempts, out.Err)

	if !intentAtFirstPhase {
		t.Error("the target must be on file before the first phase, i.e. before anything is copied")
	}
	if !busyThroughout {
		t.Error("the app must report itself busy at every phase, or closing the window asks nothing")
	}
	if otaRunning.Load() {
		t.Error("busy must be cleared once the run is over")
	}

	// A dev build embeds a 0-byte engine stub. There is then nothing to deliver,
	// and the engine half cannot be proven. Say so loudly: that is exactly the
	// half most worth proving.
	engineProvable := agentbin.GoLibrespotAvailable()
	if !engineProvable {
		t.Log("BUILD HAS NO EMBEDDED ENGINE: run `make engine-embed` with a real go-librespot to cover the engine half")
	}

	reached := runErr == nil && out.Outcome == outcomeDone &&
		agentReached(out.Version, before["version"], before["build"]) &&
		(!engineProvable || out.Version["goLibrespot"] == "present")
	if !reached {
		// 5. The failure path must produce something the user can send.
		rep := a.UpdateFailureReport(host, port, "live-contract", "target state not reached: "+out.Outcome+" "+out.Err, appVersion)
		t.Logf("failure report:\n%s", rep)
		// The header says "failure report" since 2026-08-23: the same builder
		// now serves the install screens too. The network facts are asserted
		// as well because they are what the report was rewritten for; the
		// advice section is NOT, because a speaker that answers its version
		// probe produces a report with no advice at all, which is correct.
		for _, must := range []string{
			"ST Reborn failure report", "speaker", "wanted version", "network facts",
		} {
			if !strings.Contains(rep, must) {
				t.Errorf("the failure report is missing %q, which is what makes it useful", must)
			}
		}
		t.Fatalf("speaker did not reach the target state (err=%v); phases %v; last reading: %v", runErr, phases, out.Version)
	}
	if !out.Confirmed && !agentCurrentEngineMissing(before, appVersion, appBuild, embeddedAgentSha256()) {
		t.Errorf("a done update that pushed must have been confirmed by this run; before=%v", before)
	}

	// 4. Reaching the target clears the record.
	if intentOnFile(host) {
		t.Errorf("the record must be cleared once the speaker is there, still on file for %s", host)
	}
	if got := a.PendingUpdateIntent(host, port); got["action"] != "" {
		t.Errorf("the supervisor must have nothing pending, still: %v", got)
	}
	t.Logf("AFTER  version=%s engine=%s nandFree=%s phases=%v", out.Version["version"], out.Version["goLibrespot"], out.Version["nandFreeBytes"], phases)
}
