package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Silence at every layer is a speaker that is not back yet; any sign of life
// means it is on the network and the agent really did not come up.
func TestSpeakerNotBackOnNetworkVerdict(t *testing.T) {
	cases := []struct {
		name  string
		facts agentWaitFacts
		want  bool
	}{
		{"silent everywhere (the 2026-09-06 SoundTouch 10)", agentWaitFacts{PingRan: true}, true},
		{"no ping binary, still silent", agentWaitFacts{}, true},
		{"ping answers", agentWaitFacts{PingRan: true, PingAlive: true}, false},
		{"ARP entry only", agentWaitFacts{PingRan: true, ARP: true}, false},
		{"Bose port open, agent ports closed", agentWaitFacts{PingRan: true, BosePort: true}, false},
		{"SSH still open", agentWaitFacts{PingRan: true, SSH: true}, false},
	}
	for _, c := range cases {
		if got := c.facts.speakerNotBackOnNetwork(); got != c.want {
			t.Errorf("%s: speakerNotBackOnNetwork = %v, want %v", c.name, got, c.want)
		}
	}
}

func stubWaitFacts(t *testing.T, f agentWaitFacts) {
	t.Helper()
	old := agentWaitFactsFn
	agentWaitFactsFn = func(context.Context, string) agentWaitFacts { return f }
	t.Cleanup(func() { agentWaitFactsFn = old })
}

func stubLateSSH(t *testing.T, running bool) {
	t.Helper()
	old := agentRunningLateFn
	agentRunningLateFn = func(*App, string) bool { return running }
	t.Cleanup(func() { agentRunningLateFn = old })
}

// The install that reported "agent did not come up" for a speaker that was
// merely off the network for a few more minutes: with nothing answering at
// any layer the result now says so, with its own code for the checklist, and
// the journal carries the reason.
func TestAgentNotUpReportsASilentSpeakerAsNotBackYet(t *testing.T) {
	journal := useTempJournal(t)
	old := installLateRecheckDelay
	installLateRecheckDelay = time.Hour // the re-check itself is covered below
	t.Cleanup(func() { installLateRecheckDelay = old })
	stubWaitFacts(t, agentWaitFacts{PingRan: true})

	a := fastTestApp()
	res := a.agentNotUp(InstallResult{Step: "wait-agent"}, "192.0.2.36", "SoundTouch 10", "generic wording")
	if res.OK {
		t.Fatal("result reads as success")
	}
	if res.Code != speakerNotBackCode {
		t.Errorf("code = %q, want %q", res.Code, speakerNotBackCode)
	}
	for _, want := range []string{"not back on the network yet", "restarting or reconnecting to Wi-Fi", "may well have succeeded"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("message lacks %q:\n%s", want, res.Message)
		}
	}
	if strings.Contains(res.Message, "generic wording") {
		t.Error("the generic agent-not-up wording leaked into the not-back message")
	}
	j := readJournal(t, journal)
	if !strings.Contains(j, "answers nothing at any layer") || !strings.Contains(j, "pingRan=true pingAlive=false arp=false") {
		t.Errorf("journal lacks the reason and the facts:\n%s", j)
	}

	// A speaker that shows a sign of life keeps the stricter verdict and the
	// caller's own wording.
	stubWaitFacts(t, agentWaitFacts{PingRan: true, PingAlive: true})
	res = a.agentNotUp(InstallResult{Step: "wait-agent"}, "192.0.2.36", "SoundTouch 10", "generic wording")
	if res.Code != "agent-not-up" || res.Message != "generic wording" {
		t.Errorf("live speaker: code=%q message=%q, want agent-not-up with the caller's wording", res.Code, res.Message)
	}
}

// The background look two minutes later: when the agent answers, the journal
// gets a "confirmed late" line that supersedes the UNCONFIRMED one and the box is
// pinned as STR for discovery; when it does not, the journal says that with
// fresh facts.
func TestLateRecheckCorrectsTheJournalWhenTheAgentAnswers(t *testing.T) {
	journal := useTempJournal(t)
	old := installLateRecheckDelay
	installLateRecheckDelay = 10 * time.Millisecond
	t.Cleanup(func() { installLateRecheckDelay = old })
	stubWaitFacts(t, agentWaitFacts{PingRan: true})
	stubLateSSH(t, false)

	a := fastTestApp()
	a.probeSTRFn = func(context.Context, string) (BoxInfo, bool) {
		return BoxInfo{Host: "192.0.2.36", Port: 8888, Kind: "str", Version: "v0.9.75", Build: "b9"}, true
	}
	a.agentNotUp(InstallResult{Step: "wait-agent"}, "192.0.2.36", "SoundTouch 10", "generic")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(readJournal(t, journal), "confirmed late") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	j := readJournal(t, journal)
	if !strings.Contains(j, "install: confirmed late - the agent answered on :8888") || !strings.Contains(j, "version v0.9.75 build b9") {
		t.Fatalf("journal lacks the corrective line:\n%s", j)
	}
	// The failure report counts journal lines carrying the FAILED token as
	// failed attempts. This attempt succeeded, so neither of its lines may
	// carry it (the wait-out line says UNCONFIRMED, and the corrective line
	// must name that one, not FAILED).
	if strings.Contains(j, "FAILED") {
		t.Errorf("a late-confirmed install left a FAILED token in the journal:\n%s", j)
	}
	if !strings.Contains(j, "the UNCONFIRMED line above is superseded") {
		t.Errorf("the corrective line does not name the UNCONFIRMED line it supersedes:\n%s", j)
	}
	a.discMu.Lock()
	_, pinned := a.otaPinned["192.0.2.36"]
	a.discMu.Unlock()
	if !pinned {
		t.Error("the late-confirmed box was not pinned as STR through its boot")
	}
}

func TestLateRecheckJournalsAStillSilentSpeaker(t *testing.T) {
	journal := useTempJournal(t)
	old := installLateRecheckDelay
	installLateRecheckDelay = 10 * time.Millisecond
	t.Cleanup(func() { installLateRecheckDelay = old })
	stubWaitFacts(t, agentWaitFacts{PingRan: true})
	stubLateSSH(t, false)

	a := fastTestApp()
	a.probeSTRFn = func(context.Context, string) (BoxInfo, bool) { return BoxInfo{}, false }
	a.agentNotUp(InstallResult{Step: "wait-agent"}, "192.0.2.36", "", "generic")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(readJournal(t, journal), "still not answering") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	j := readJournal(t, journal)
	if !strings.Contains(j, "install: still not answering") {
		t.Fatalf("journal lacks the second look:\n%s", j)
	}
	if strings.Contains(j, "confirmed late") {
		t.Errorf("a silent speaker was confirmed:\n%s", j)
	}
}

// On a chassis whose firewall hides the agent ports from the PC (rhino), the
// late look falls back to the SSH process check, exactly like the wait
// itself does.
func TestLateRecheckAcceptsARunningProcessOverSSH(t *testing.T) {
	journal := useTempJournal(t)
	old := installLateRecheckDelay
	installLateRecheckDelay = 10 * time.Millisecond
	t.Cleanup(func() { installLateRecheckDelay = old })
	stubWaitFacts(t, agentWaitFacts{PingRan: true})
	stubLateSSH(t, true)

	a := fastTestApp()
	a.probeSTRFn = func(context.Context, string) (BoxInfo, bool) { return BoxInfo{}, false }
	a.agentNotUp(InstallResult{Step: "wait-agent"}, "192.0.2.36", "", "generic")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(readJournal(t, journal), "confirmed late") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if j := readJournal(t, journal); !strings.Contains(j, "as a running process over SSH") {
		t.Fatalf("journal lacks the SSH-confirmed line:\n%s", j)
	}
}
