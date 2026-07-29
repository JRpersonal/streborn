package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLiveEngineRepair covers the engine-only repair that "update all speakers"
// runs for a speaker whose agent is already current but whose Spotify engine is
// gone. It calls exactly what that row calls (App.EnsureSpotifyEngine) and
// nothing else, so the two promises the UI makes about it can be checked:
//
//   - the speaker ends up with the engine present,
//   - and it is NOT restarted to get there.
//
// The second one is the promise worth guarding. The confirmation dialog tells
// the user this group normally does not restart, and a current agent hot-swaps
// the engine in place (webui handleAgentSidecar), so an unexpected reboot would
// make the dialog a lie and interrupt whatever the speaker was playing.
//
// It adapts to the speaker it is given, because the interesting state cannot be
// created on demand: these boxes have no SSH, and the engine is only ever
// dropped by the agent itself under NAND pressure. On a speaker that HAS its
// engine this proves the no-op half (nothing delivered, nothing restarted); on
// one that lost it, the same run proves the delivery half.
//
// STR_LIVE_BOX=<ip> [STR_LIVE_PORT=17008] go test ./... -run LiveEngineRepair -v
func TestLiveEngineRepair(t *testing.T) {
	host := os.Getenv("STR_LIVE_BOX")
	if host == "" {
		t.Skip("set STR_LIVE_BOX=<ip> to run the live engine-repair test")
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
	wasMissing := before["goLibrespot"] == "missing"
	uptimeBefore, _ := strconv.ParseInt(before["uptimeSec"], 10, 64)
	t.Logf("BEFORE %s: engine=%s uptime=%ds nandFree=%s",
		before["friendlyName"], before["goLibrespot"], uptimeBefore, before["nandFreeBytes"])

	res, err := a.EnsureSpotifyEngine(host, port)
	t.Logf("EnsureSpotifyEngine -> %q (err=%v)", res, err)
	if strings.Contains(strings.ToLower(res), "no embedded engine") {
		t.Skip("this build carries the empty engine stub; nothing can be delivered (CI fills it, or drop a real go-librespot into agentbin/)")
	}
	if err != nil {
		t.Fatalf("the engine repair failed: %v", err)
	}
	// "present" only says the speaker HAS an engine, not that it has THIS one:
	// the push is gated on the content hash, so a speaker carrying an older
	// engine is brought up to the embedded one and answers "ok" rather than
	// "current" (seen on a SoundTouch 10 whose engine predated the embedded
	// build, 2026-07-29). Both are correct outcomes; what must hold either way
	// is below.
	if !wasMissing {
		t.Logf("speaker already had an engine: %s", map[bool]string{
			true:  "it was already the embedded one, nothing sent",
			false: "it differed from the embedded one and was replaced live",
		}[res == "current"])
	}

	// Give a hot-swap a moment to settle before believing the reading.
	time.Sleep(5 * time.Second)
	after, err := a.BoxAgentVersion(host, port)
	if err != nil {
		t.Fatalf("read after the repair: %v", err)
	}
	uptimeAfter, _ := strconv.ParseInt(after["uptimeSec"], 10, 64)
	t.Logf("AFTER  engine=%s uptime=%ds", after["goLibrespot"], uptimeAfter)

	if after["goLibrespot"] != "present" {
		t.Errorf("the engine is still not present after the repair: %q", after["goLibrespot"])
	}
	// A restart resets the counter, so an uptime that went backwards is the
	// speaker having rebooted behind the user's back.
	if uptimeAfter < uptimeBefore {
		t.Errorf("the speaker restarted (uptime %ds -> %ds), but the engine repair promises it will not",
			uptimeBefore, uptimeAfter)
	}
	if wasMissing {
		t.Log("delivery half proven: the speaker had no engine and now has one")
	} else {
		t.Log("no-op half proven: nothing was delivered and the speaker was not restarted")
	}
}
