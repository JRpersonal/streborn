package main

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

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

// TestLiveFleetUpdate runs "update all speakers" the way the button does, one
// layer below it: RunFleetUpdate, the same Go batch, with the SAME selection
// the frontend makes (splitUpdateTargets): a speaker behind this build gets
// the full update, a speaker already on it that lost its Spotify engine gets
// the engine put back, and a speaker that is current with its engine is left
// alone. STR_LIVE_FLEET_FORCE=1 pushes every speaker regardless, for a re-run
// that must exercise the push on current speakers too.
//
// The batch starts every speaker at once and serialises only the byte pushes
// through its gate, exactly like the app, so the interesting findings show up
// in the comparison afterwards: the tight one that loses its Spotify engine
// while the others keep it, or the one that rebooted without taking the update
// at all.
//
// STR_LIVE_FLEET="ip:port,ip:port,..." go test ./... -run LiveFleetUpdate -v -timeout 40m
func TestLiveFleetUpdate(t *testing.T) {
	spec := os.Getenv("STR_LIVE_FLEET")
	if spec == "" {
		t.Skip(`set STR_LIVE_FLEET="192.0.2.5:17008,192.0.2.6:8888" to run the fleet test`)
	}
	force := os.Getenv("STR_LIVE_FLEET_FORCE") != ""
	a := NewApp()

	type result struct {
		host, name, before, after, engineBefore, engineAfter string
		buildBefore, buildAfter                              string
		engineOnly, skipped, reached                         bool
		note                                                 string
	}
	results := map[string]*result{}
	var order []string
	var targets []FleetTarget

	// --- selection, exactly as the app decides it ---
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
		r := &result{host: host}
		results[host] = r
		order = append(order, host)
		v0, err := a.BoxAgentVersion(host, port)
		if err != nil {
			r.note = "not reachable before the run: " + err.Error()
			t.Errorf("%s: %s", host, r.note)
			continue
		}
		r.name, r.before, r.engineBefore, r.buildBefore = v0["friendlyName"], v0["version"], v0["goLibrespot"], v0["build"]
		t.Logf("=== %s (%s) BEFORE version=%s build=%s engine=%s nandFree=%s", r.name, host, r.before, r.buildBefore, r.engineBefore, v0["nandFreeBytes"])
		// Only a build with a real embedded agent can call a speaker current;
		// the dev stub cannot, and UpdateBoxAgent refuses to push it anyway.
		current := len(agentbin.Bytes()) > 0 && agentReached(v0, "", "") && !force
		switch {
		case current && r.engineBefore == "missing":
			r.engineOnly = true
			targets = append(targets, FleetTarget{Host: host, Port: port, DeviceID: v0["deviceID"], Name: r.name, EngineOnly: true})
		case current:
			r.skipped, r.reached = true, true
			r.note = "already on this build with its engine: left alone, as the app would"
		default:
			targets = append(targets, FleetTarget{Host: host, Port: port, DeviceID: v0["deviceID"], Name: r.name})
		}
	}
	if len(targets) == 0 {
		t.Log("nothing to update: every reachable speaker is already on this build with its engine (STR_LIVE_FLEET_FORCE=1 pushes anyway)")
		return
	}

	var mu sync.Mutex
	last := map[string]string{}
	intentFirst := map[string]bool{}
	busy := true
	onPhase := func(p updatePhase) {
		mu.Lock()
		defer mu.Unlock()
		if _, seen := intentFirst[p.Host]; !seen {
			// The batch records every speaker's target before its turn: on
			// file by the first phase, which precedes the push.
			intentFirst[p.Host] = intentOnFile(p.Host)
		}
		if !otaRunning.Load() {
			busy = false
		}
		if last[p.Host] != p.Phase {
			last[p.Host] = p.Phase
			name := p.Host
			if r := results[p.Host]; r != nil && r.name != "" {
				name = r.name
			}
			t.Logf("  %-22s %-16s attempt=%d reachable=%v version=%s engine=%s remaining=%ds",
				name, p.Phase, p.Attempt, p.Reachable, p.Version, p.Engine, p.RemainingMs/1000)
		}
	}

	engineProvable := agentbin.GoLibrespotAvailable()
	if !engineProvable {
		t.Log("BUILD HAS NO EMBEDDED ENGINE: run `make engine-embed` with a real go-librespot to cover the engine half")
	}

	t.Logf("App.RunFleetUpdate over %d speaker(s) ...", len(targets))
	fleet, err := a.runFleetUpdate(targets, onPhase)
	if err != nil {
		t.Fatalf("the batch was refused: %v", err)
	}
	if !busy {
		t.Error("the app must report itself busy for the whole batch")
	}
	if otaRunning.Load() {
		t.Error("busy must be cleared once the batch is over")
	}

	reportShown := false
	for _, row := range fleet.Rows {
		r := results[row.Host]
		if r == nil {
			t.Errorf("row for an unknown host %s", row.Host)
			continue
		}
		if !intentFirst[row.Host] {
			t.Errorf("%s: the target must be on file before the first phase", r.name)
		}
		if row.Version != nil {
			r.after, r.engineAfter, r.buildAfter = row.Version["version"], row.Version["goLibrespot"], row.Version["build"]
		}
		t.Logf("=== %s row: outcome=%s confirmed=%v verdict=%q engineDelivered=%v tooFull=%v unmet=%q err=%q",
			r.name, row.Outcome, row.Confirmed, row.Verdict, row.EngineDelivered, row.EngineTooFull, row.Unmet, row.Err)
		switch {
		case row.EngineOnly:
			// The engine-only row promises no restart and ends with the engine
			// confirmed present by the speaker itself.
			r.reached = row.Outcome == outcomeDone || (!engineProvable && row.Unmet == unmetNoEmbeddedEngine)
			if !engineProvable && row.Unmet == unmetNoEmbeddedEngine {
				r.note = "build carries no engine"
			}
		default:
			// BOTH halves, or the run lies. Waiting only for the engine let a
			// speaker whose engine had survived the reboot pass instantly while
			// its agent was still being replaced: the Portable "passed" on the
			// old build and only finished minutes later (2026-07-29). The batch
			// now asks agentReached of the final reading and the engine of the
			// honesty re-check it already did.
			agentDone := row.Version != nil && agentReached(row.Version, r.before, r.buildBefore)
			engineDone := !engineProvable || r.engineAfter == "present"
			r.reached = row.Outcome == outcomeDone && agentDone && engineDone
			if !engineProvable && row.Outcome == outcomeDone {
				r.note = "build carries no engine"
			}
			if !r.reached && row.Version != nil && r.engineAfter == "present" && !agentMoved(row.Version, r.before, r.buildBefore) {
				// Distinguish "still updating" from "refused the update": both
				// look like an unchanged version, and only the uptime tells them
				// apart.
				r.note = "agent version unchanged (uptime " + row.Version["uptimeSec"] + "s): still applying, or the update did not take"
			}
		}
		if r.reached {
			if intentOnFile(row.Host) {
				t.Errorf("%s: the record must be cleared once the speaker is there", r.name)
			}
			continue
		}
		if r.note == "" {
			r.note = "outcome " + row.Outcome
			if row.Verdict != "" {
				r.note += " (" + row.Verdict + ")"
			}
			if row.Err != "" {
				r.note += ": " + row.Err
			}
		}
		t.Errorf("%s: %s", r.name, r.note)
		if !reportShown {
			// Only the first failure produces a report, like the batch does.
			reportShown = true
			port := 8888
			for _, tg := range targets {
				if tg.Host == row.Host {
					port = tg.Port
				}
			}
			t.Logf("failure report for %s:\n%s", r.name,
				a.UpdateFailureReport(row.Host, port, "fleet-test", "target state not reached", appVersion))
		}
	}

	t.Log("---- fleet summary ----")
	failed := 0
	for _, host := range order {
		r := results[host]
		kind := "update"
		if r.engineOnly {
			kind = "engine"
		}
		if r.skipped {
			kind = "skip"
		}
		t.Logf("%-22s %-6s %s (%s) -> %s (%s) | engine %s -> %s | reached=%v %s",
			r.name, kind, r.before, r.buildBefore, r.after, r.buildAfter, r.engineBefore, r.engineAfter, r.reached, r.note)
		if !r.reached {
			failed++
		}
	}
	if failed > 0 {
		t.Fatalf("%d of %d speakers did not reach the target state", failed, len(order))
	}
}
