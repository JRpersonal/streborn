package main

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"streborn-app/agentbin"
)

// agentReached answers the question that actually matters: is the speaker now
// running the very agent this build carries? The version and build stamp are
// linked into the ARM binary, so finding both of the speaker's strings in the
// embedded bytes is a direct answer rather than an inference.
//
// It replaces "did the version string change", which cannot work for a local
// build: `git describe` off an unchanged commit returns the same string every
// time, so a speaker that took the update looked identical to one that refused
// it, and every speaker burned its full deadline before being reported as a
// failure it was not (2026-07-29). It is also the honest check for a re-run,
// where a speaker is already on the target and nothing needs to move at all.
//
// Falls back to agentMoved when the build carries only the empty dev stub.
func agentReached(v map[string]string, beforeVersion, beforeBuild string) bool {
	if bin := agentbin.Bytes(); len(bin) > 0 && v["version"] != "" && v["build"] != "" {
		return bytes.Contains(bin, []byte(v["version"])) && bytes.Contains(bin, []byte(v["build"]))
	}
	return agentMoved(v, beforeVersion, beforeBuild)
}

// agentMoved reports whether the speaker is running a different agent than it
// was before the run. Used only where no embedded agent is available to compare
// against, and for telling "still applying" apart from "refused the update".
func agentMoved(v map[string]string, beforeVersion, beforeBuild string) bool {
	if v["version"] == "" {
		return false
	}
	if beforeVersion == "" {
		return true
	}
	if v["version"] != beforeVersion {
		return true
	}
	return v["build"] != "" && v["build"] != beforeBuild
}

// TestLiveFleetUpdate mirrors what "update all speakers" does, one layer below
// the buttons: the same bound methods, in the same order, sequentially, against
// several real speakers.
//
// Sequential on purpose, exactly like the batch in the app. Two speakers
// rebooting at once makes a failure impossible to attribute, and the interesting
// findings only show up when the speakers are compared afterwards: the tight one
// that loses its Spotify engine while the others keep it, or the one that
// rebooted without taking the update at all.
//
// STR_LIVE_FLEET="ip:port,ip:port,..." go test ./... -run LiveFleetUpdate -v -timeout 40m
func TestLiveFleetUpdate(t *testing.T) {
	spec := os.Getenv("STR_LIVE_FLEET")
	if spec == "" {
		t.Skip(`set STR_LIVE_FLEET="192.0.2.5:17008,192.0.2.6:8888" to run the fleet test`)
	}
	a := NewApp()

	type result struct {
		host, name, before, after, engineBefore, engineAfter string
		buildBefore, buildAfter                              string
		reached                                              bool
		note                                                 string
	}
	var results []result
	reportShown := false

	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		host, portStr, ok := strings.Cut(entry, ":")
		port := 8888
		if ok {
			if n, err := strconv.Atoi(portStr); err == nil {
				port = n
			}
		}
		r := result{host: host}
		v0, err := a.BoxAgentVersion(host, port)
		if err != nil {
			r.note = "not reachable before the run: " + err.Error()
			results = append(results, r)
			t.Errorf("%s: %s", host, r.note)
			continue
		}
		r.name, r.before, r.engineBefore, r.buildBefore = v0["friendlyName"], v0["version"], v0["goLibrespot"], v0["build"]
		t.Logf("=== %s (%s) BEFORE version=%s build=%s engine=%s nandFree=%s", r.name, host, r.before, r.buildBefore, r.engineBefore, v0["nandFreeBytes"])

		// The batch records every speaker's target before its turn.
		a.RecordUpdateIntent(host, port, appVersion, v0["deviceID"], r.name, true)
		a.SetOTARunning(true)

		if err := a.UpdateBoxAgent(host, port); err != nil {
			t.Logf("  %s: UpdateBoxAgent returned %v (the state below decides)", r.name, err)
		}

		deadline := time.Now().Add(10 * time.Minute)
		var last map[string]string
		for time.Now().Before(deadline) {
			time.Sleep(10 * time.Second)
			v, verr := a.BoxAgentVersion(host, port)
			if verr != nil {
				continue
			}
			last = v
			if v["goLibrespot"] == "missing" {
				res, _ := a.EnsureSpotifyEngine(host, port)
				if strings.Contains(strings.ToLower(res), "no embedded engine") {
					r.note = "build carries no engine"
					r.reached = true
					break
				}
				continue
			}
			// BOTH halves, or the run lies. Waiting only for the engine let a
			// speaker whose engine had survived the reboot pass instantly while
			// its agent was still being replaced: the Portable "passed" on the
			// old build and only finished minutes later (2026-07-29).
			//
			// "Moved" means version OR build stamp. A release bumps the version,
			// but a local build off an unchanged commit keeps the exact same
			// `git describe` string and only the build stamp moves, so a
			// version-only check can never come true and every speaker burns its
			// full deadline before being called a failure it is not (seen while
			// verifying the update-all engine repair, 2026-07-29).
			agentDone := agentReached(v, r.before, r.buildBefore)
			if v["goLibrespot"] == "present" && agentDone {
				r.reached = true
				break
			}
		}
		a.SetOTARunning(false)
		if last != nil {
			r.after, r.engineAfter, r.buildAfter = last["version"], last["goLibrespot"], last["build"]
		}
		if r.reached {
			a.ClearUpdateIntent(host, port)
		} else if last != nil && last["goLibrespot"] == "present" && !agentMoved(last, r.before, r.buildBefore) {
			// Distinguish "still updating" from "refused the update": both look
			// like an unchanged version, and only the uptime tells them apart.
			r.note = "agent version unchanged (uptime " + last["uptimeSec"] + "s): still applying, or the update did not take"
			t.Errorf("%s: %s", r.name, r.note)
		} else if !reportShown {
			// Only the first failure produces a report, like the batch does.
			reportShown = true
			t.Logf("failure report for %s:\n%s", r.name,
				a.UpdateFailureReport(host, port, "fleet-test", "target state not reached", appVersion))
		}
		results = append(results, r)
		t.Logf("=== %s AFTER version=%s engine=%s reached=%v %s", r.name, r.after, r.engineAfter, r.reached, r.note)
	}

	t.Log("---- fleet summary ----")
	failed := 0
	for _, r := range results {
		t.Logf("%-22s %s (%s) -> %s (%s) | engine %s -> %s | reached=%v %s",
			r.name, r.before, r.buildBefore, r.after, r.buildAfter, r.engineBefore, r.engineAfter, r.reached, r.note)
		if !r.reached {
			failed++
		}
	}
	if failed > 0 {
		t.Fatalf("%d of %d speakers did not reach the target state", failed, len(results))
	}
}
