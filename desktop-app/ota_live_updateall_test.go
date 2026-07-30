package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLiveUpdateAllBindings drives "update all speakers" the way the button
// does, one layer below it: the same bound App methods, in the same order, with
// the SAME selection the frontend makes.
//
// The existing fleet test updates every speaker it is given. This one first
// decides who takes part, which is the half that changed: a speaker already
// running this build is not updated, and a speaker that is up to date but has
// lost its Spotify engine is repaired instead of skipped. That selection is
// splitUpdateTargets in the frontend; it is mirrored here because the frontend
// cannot be driven headlessly.
//
// The engine-only group carries a promise the update group does not: the
// speaker is NOT restarted. That is checked here, because breaking it would
// interrupt whatever the speaker was playing without the user being told.
//
// STR_LIVE_FLEET="ip:port,ip:port,..." go test ./... -run LiveUpdateAllBindings -v -timeout 40m
func TestLiveUpdateAllBindings(t *testing.T) {
	spec := os.Getenv("STR_LIVE_FLEET")
	if spec == "" {
		t.Skip(`set STR_LIVE_FLEET="192.0.2.5:17008,192.0.2.6:8888" to run the update-all test`)
	}
	a := NewApp()

	type box struct {
		host, port string
		p          int
		name       string
		before     map[string]string
	}
	var updateTargets, engineTargets, upToDate []box

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
		v, err := a.BoxAgentVersion(host, port)
		if err != nil {
			t.Errorf("%s: not reachable before the run: %v", host, err)
			continue
		}
		b := box{host: host, p: port, name: v["friendlyName"], before: v}
		switch {
		case !agentReached(v, "", ""):
			updateTargets = append(updateTargets, b)
		case v["goLibrespot"] == "missing":
			engineTargets = append(engineTargets, b)
		default:
			upToDate = append(upToDate, b)
		}
	}
	t.Logf("selection: %d to update, %d engine-only repair, %d already fine",
		len(updateTargets), len(engineTargets), len(upToDate))
	for _, b := range upToDate {
		t.Logf("  untouched: %-22s %s (%s) engine=%s", b.name, b.before["version"], b.before["build"], b.before["goLibrespot"])
	}
	if len(updateTargets)+len(engineTargets) == 0 {
		t.Log("nothing to do: every speaker already runs this build with its engine present")
		return
	}

	// --- group 1: the full update ---
	for _, b := range updateTargets {
		t.Logf("=== UPDATE %s (%s) from %s (%s) engine=%s",
			b.name, b.host, b.before["version"], b.before["build"], b.before["goLibrespot"])
		a.RecordUpdateIntent(b.host, b.p, appVersion, b.before["deviceID"], b.name, true)
		a.SetOTARunning(true)
		if err := a.UpdateBoxAgent(b.host, b.p); err != nil {
			t.Logf("  UpdateBoxAgent returned %v (the state below decides)", err)
		}
		deadline := time.Now().Add(10 * time.Minute)
		var last map[string]string
		reached := false
		for time.Now().Before(deadline) {
			time.Sleep(10 * time.Second)
			v, verr := a.BoxAgentVersion(b.host, b.p)
			if verr != nil {
				continue
			}
			last = v
			if v["goLibrespot"] == "missing" {
				res, _ := a.EnsureSpotifyEngine(b.host, b.p)
				if strings.Contains(strings.ToLower(res), "no embedded engine") {
					t.Log("  BUILD HAS NO EMBEDDED ENGINE: the engine half is not covered by this run")
					reached = agentReached(v, "", "")
					break
				}
				continue
			}
			if agentReached(v, "", "") {
				reached = true
				break
			}
		}
		a.SetOTARunning(false)
		if reached {
			a.ClearUpdateIntent(b.host, b.p)
			t.Logf("  -> %s now on %s (%s) engine=%s", b.name, last["version"], last["build"], last["goLibrespot"])
		} else {
			t.Errorf("%s did not reach the target state; last reading: %v", b.name, last)
		}
	}

	// --- group 2: the engine-only repair, which must not restart the speaker ---
	for _, b := range engineTargets {
		upBefore, _ := strconv.ParseInt(b.before["uptimeSec"], 10, 64)
		t.Logf("=== ENGINE ONLY %s (%s) engine=%s uptime=%ds", b.name, b.host, b.before["goLibrespot"], upBefore)
		a.RecordUpdateIntent(b.host, b.p, appVersion, b.before["deviceID"], b.name, true)
		res, err := a.EnsureSpotifyEngine(b.host, b.p)
		t.Logf("  EnsureSpotifyEngine -> %q (err=%v)", res, err)
		if err != nil {
			t.Errorf("%s: engine repair failed: %v", b.name, err)
			continue
		}
		if strings.Contains(strings.ToLower(res), "no embedded engine") {
			t.Log("  build carries no engine, nothing to deliver")
			continue
		}
		time.Sleep(5 * time.Second)
		after, aerr := a.BoxAgentVersion(b.host, b.p)
		if aerr != nil {
			t.Errorf("%s: unreachable after the repair: %v", b.name, aerr)
			continue
		}
		upAfter, _ := strconv.ParseInt(after["uptimeSec"], 10, 64)
		if after["goLibrespot"] != "present" {
			t.Errorf("%s: engine still not present after the repair: %q", b.name, after["goLibrespot"])
			continue
		}
		if upAfter < upBefore {
			t.Errorf("%s: the speaker restarted (uptime %ds -> %ds); the engine-only group promises it will not",
				b.name, upBefore, upAfter)
		}
		a.ClearUpdateIntent(b.host, b.p)
		t.Logf("  -> engine present, uptime %ds -> %ds (no restart)", upBefore, upAfter)
	}
}
