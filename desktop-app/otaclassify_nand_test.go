package main

import (
	"strings"
	"testing"
)

// A push that lands on disk and never runs used to be journalled as a boot
// rollback with "an identical re-push cannot help", and nothing else. The space
// check in the same run had already measured that the speaker could not hold
// the second copy the swap needs, said so to the journal, and pushed anyway.
// The two were never connected, so the user read a generic failure.
//
// Live case 2026-09-30: a SoundTouch 20 with 11 MB free for a 16 MB binary
// failed exactly this way twice in one morning, and had been stuck on a build
// from 12 September for weeks while its owner's four other speakers updated
// normally. He found out when it stopped answering at all.
func TestLandedNotRunningNamesTheSpaceWhenItIsTooTight(t *testing.T) {
	old := appBuild
	appBuild = "b2"
	t.Cleanup(func() { appBuild = old })

	ver := map[string]string{
		"version": "v0.9.80", "build": "b1",
		"agentBinarySha256":  "aa",
		"agentRunningSha256": "bb",
		"nandFreeBytes":      "11112448", // the measured figure
	}
	const need = 16187552 // the measured binary

	verdict, journal := classifyAgentVersion(ver, "aa", appBuild, need)
	if verdict != "landed-not-running" {
		t.Fatalf("verdict = %q, want landed-not-running", verdict)
	}
	for _, want := range []string{"11112448", "16187552", "Free space on the speaker"} {
		if !strings.Contains(journal, want) {
			t.Errorf("journal does not mention %q, so the user still cannot act on it:\n%s", want, journal)
		}
	}
}

// Plenty of room: the same failure is a real rollback and must not be blamed on
// space, or the advice sends people deleting things for nothing.
func TestLandedNotRunningStaysSilentWhenThereIsRoom(t *testing.T) {
	old := appBuild
	appBuild = "b2"
	t.Cleanup(func() { appBuild = old })

	ver := map[string]string{
		"version": "v0.9.80", "build": "b1",
		"agentBinarySha256":  "aa",
		"agentRunningSha256": "bb",
		"nandFreeBytes":      "30000000",
	}
	verdict, journal := classifyAgentVersion(ver, "aa", appBuild, 16187552)
	if verdict != "landed-not-running" {
		t.Fatalf("verdict = %q", verdict)
	}
	if strings.Contains(journal, "Free space on the speaker") {
		t.Errorf("blamed the space on a box that has plenty:\n%s", journal)
	}
}

// An agent too old to report its free space must produce no space claim at all.
// Guessing there would be worse than saying nothing.
func TestLandedNotRunningSaysNothingWithoutTheFigures(t *testing.T) {
	old := appBuild
	appBuild = "b2"
	t.Cleanup(func() { appBuild = old })

	base := map[string]string{
		"version": "v0.9.80", "build": "b1",
		"agentBinarySha256":  "aa",
		"agentRunningSha256": "bb",
	}
	for name, mutate := range map[string]func(map[string]string) (map[string]string, int64){
		"no nandFreeBytes at all": func(m map[string]string) (map[string]string, int64) { return m, 16187552 },
		"garbled nandFreeBytes": func(m map[string]string) (map[string]string, int64) {
			m["nandFreeBytes"] = "n/a"
			return m, 16187552
		},
		"no pushed size known": func(m map[string]string) (map[string]string, int64) {
			m["nandFreeBytes"] = "11112448"
			return m, 0
		},
	} {
		ver := map[string]string{}
		for k, v := range base {
			ver[k] = v
		}
		ver, need := mutate(ver)
		_, journal := classifyAgentVersion(ver, "aa", appBuild, need)
		if strings.Contains(journal, "Free space on the speaker") {
			t.Errorf("%s: invented a space claim:\n%s", name, journal)
		}
	}
}
