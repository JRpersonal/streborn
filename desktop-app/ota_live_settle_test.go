package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveUpdateDuringBoot reproduces the 2026-07-31 support case: the user
// power-cycles the speaker and clicks update while it is still booting. The
// preflight then reaches either a refused connection or the box's own :17008
// listener (bare 400), and before the settle-retry the app escalated straight
// to the SSH path, which reboots the box again and can drop the Spotify
// engine. The assertion here is that UpdateBoxAgent rides out the boot window
// on the HTTP path: the OTA journal must contain the settle marker (or prove
// the preflight passed cleanly because the agent won the race), and must NOT
// contain the SSH escalation marker.
//
// Opt-in: STR_LIVE_SETTLE_BOX=<ip> (STR_LIVE_PORT default 17008). The test
// REBOOTS that speaker. Run it only against your own test hardware.
func TestLiveUpdateDuringBoot(t *testing.T) {
	host := os.Getenv("STR_LIVE_SETTLE_BOX")
	if host == "" {
		t.Skip("set STR_LIVE_SETTLE_BOX=<ip> (and optionally STR_LIVE_PORT) to run; it reboots that speaker")
	}
	port := 17008
	if p := os.Getenv("STR_LIVE_PORT"); p != "" {
		fmt.Sscanf(p, "%d", &port)
	}
	a := NewApp()

	url := fmt.Sprintf("http://%s:%d/api/box/reboot", host, port)
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("reboot request: %v", err)
	}
	resp.Body.Close()
	t.Logf("reboot sent (%d), starting the update into the boot window", resp.StatusCode)
	// A beat so the listener is really gone before the preflight fires.
	time.Sleep(10 * time.Second)

	start := time.Now()
	err = a.UpdateBoxAgent(host, port)
	t.Logf("UpdateBoxAgent returned after %s: %v", time.Since(start).Round(time.Second), err)

	journal := a.otaHistoryTail(host, 60)
	// Only judge this run: everything from the last "start:" line onward.
	if i := strings.LastIndex(journal, "start: port="); i >= 0 {
		journal = journal[i:]
	}
	t.Logf("ota journal (this run):\n%s", journal)
	if strings.Contains(journal, "HTTP preflight rejected -> trying SSH") {
		t.Fatalf("update escalated to SSH during the boot window; the settle-retry should have held the HTTP path")
	}
	if err != nil {
		t.Fatalf("UpdateBoxAgent: %v", err)
	}
}
