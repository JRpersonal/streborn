package main

import (
	"errors"

	"testing"
	"time"
)

func TestRebootNeverHappened(t *testing.T) {
	cases := []struct {
		name      string
		uptime    time.Duration
		sinceSent time.Duration
		want      bool
	}{
		// The Wave from 2026-11-03: restart command 5 minutes ago, kernel up 22.
		{"stalled shutdown", 1363 * time.Second, 5 * time.Minute, true},
		{"restarted normally", 40 * time.Second, 70 * time.Second, false},
		{"inside the slack", 85 * time.Second, 70 * time.Second, false},
		{"just past the slack", 91 * time.Second, 70 * time.Second, true},
	}
	for _, c := range cases {
		if got := rebootNeverHappened(c.uptime, c.sinceSent); got != c.want {
			t.Errorf("%s: rebootNeverHappened(%s, %s) = %v, want %v", c.name, c.uptime, c.sinceSent, got, c.want)
		}
	}
}

func TestParseUptimeProbe(t *testing.T) {
	up, pid1, ok := parseUptimeProbe("1363\n    1 root       0:00 init [6]\n")
	if !ok || up != 1363*time.Second || pid1 != "1 root 0:00 init [6]" {
		t.Fatalf("got %s %q %v", up, pid1, ok)
	}
	if _, _, ok := parseUptimeProbe(""); ok {
		t.Error("empty output must not parse")
	}
	if _, _, ok := parseUptimeProbe("sh: awk: not found\n"); ok {
		t.Error("an error line must not parse as an uptime")
	}
	up, pid1, ok = parseUptimeProbe("42")
	if !ok || up != 42*time.Second || pid1 != "" {
		t.Fatalf("uptime without a ps line: got %s %q %v", up, pid1, ok)
	}
}

// stubHungReboot replaces the probe and the forced restart for one test, and
// clears the ledger entry for host before and after.
func stubHungReboot(t *testing.T, host, probeOut string, probeErr error) *int {
	t.Helper()
	// The forced path writes a journal line; keep it out of the real one.
	dir := t.TempDir()
	for _, k := range []string{"LOCALAPPDATA", "HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(k, dir)
	}
	forced := 0
	oldProbe, oldForce, oldAfter := hungRebootProbeFn, forceRebootFn, hungRebootCheckAfter
	hungRebootProbeFn = func(string) (string, error) { return probeOut, probeErr }
	forceRebootFn = func(string) error { forced++; return nil }
	rebootLedger.Delete(host)
	t.Cleanup(func() {
		hungRebootProbeFn, forceRebootFn, hungRebootCheckAfter = oldProbe, oldForce, oldAfter
		rebootLedger.Delete(host)
	})
	return &forced
}

func TestForceRebootIfHungForcesAStalledRestart(t *testing.T) {
	const host = "192.0.2.45"
	forced := stubHungReboot(t, host, "1363\n    1 root       0:00 init [6]\n", nil)
	rebootLedger.Store(host, time.Now().Add(-5*time.Minute))
	if !quietApp().forceRebootIfHung(host) {
		t.Fatal("a speaker still on its old boot five minutes after the restart command must be forced")
	}
	if *forced != 1 {
		t.Fatalf("forced restarts = %d, want 1", *forced)
	}
	sent, _ := rebootSentAt(host)
	if time.Since(sent) > time.Minute {
		t.Error("the forced restart must renew the ledger, so a second check starts from it")
	}
}

func TestForceRebootIfHungLeavesARestartedSpeakerAlone(t *testing.T) {
	const host = "192.0.2.46"
	forced := stubHungReboot(t, host, "30\n    1 root       0:02 init [5]\n", nil)
	rebootLedger.Store(host, time.Now().Add(-90*time.Second))
	if quietApp().forceRebootIfHung(host) || *forced != 0 {
		t.Fatal("a speaker whose uptime is younger than the restart command did restart")
	}
}

func TestForceRebootIfHungNeedsPositiveEvidence(t *testing.T) {
	const host = "192.0.2.47"
	cases := []struct {
		name     string
		sentAgo  time.Duration // 0 = no ledger entry
		probeOut string
		probeErr error
	}{
		{"no restart command on record", 0, "5000\n", nil},
		{"too early to tell", 30 * time.Second, "5000\n", nil},
		{"ledger entry too old", 30 * time.Minute, "5000\n", nil},
		{"SSH down", 2 * time.Minute, "", errors.New("connection refused")},
		{"uptime unreadable", 2 * time.Minute, "garbage\n", nil},
	}
	for _, c := range cases {
		forced := stubHungReboot(t, host, c.probeOut, c.probeErr)
		if c.sentAgo > 0 {
			rebootLedger.Store(host, time.Now().Add(-c.sentAgo))
		}
		if quietApp().forceRebootIfHung(host) || *forced != 0 {
			t.Errorf("%s: forced a restart without evidence", c.name)
		}
	}
}
