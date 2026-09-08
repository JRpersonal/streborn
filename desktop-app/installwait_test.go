package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Silence at every layer is a speaker that is not back yet; any sign of life
// at a layer that cannot go stale means it is on the network and the agent
// really did not come up.
func TestSpeakerNotBackOnNetworkVerdict(t *testing.T) {
	cases := []struct {
		name  string
		facts agentWaitFacts
		want  bool
	}{
		{"silent everywhere (the 2026-09-06 SoundTouch 10)", agentWaitFacts{PingRan: true}, true},
		{"no ping binary, still silent", agentWaitFacts{}, true},
		{"ping answers", agentWaitFacts{PingRan: true, PingAlive: true}, false},
		// The correction of 2026-09-08. Reporter A's facts line read
		// "pingRan=true pingAlive=false arp=true" while the speaker answered
		// nothing at any layer for seventeen minutes: a stale Windows ARP
		// entry outlives the peer, and the app's own failed dials refresh it.
		// Counting it as life called a successful install a hard failure and
		// never armed the re-check.
		{"a stale ARP entry is not a sign of life", agentWaitFacts{PingRan: true, ARP: true}, true},
		{"Bose port open, agent ports closed", agentWaitFacts{PingRan: true, BosePort: true}, false},
		{"Bose port answered a real GET", agentWaitFacts{PingRan: true, BoseHTTP: true}, false},
		{"only the UPnP renderer answers", agentWaitFacts{PingRan: true, MediaPort: true}, false},
		{"SSH still open", agentWaitFacts{PingRan: true, SSH: true}, false},
	}
	for _, c := range cases {
		if got := c.facts.speakerNotBackOnNetwork(); got != c.want {
			t.Errorf("%s: speakerNotBackOnNetwork = %v, want %v", c.name, got, c.want)
		}
	}
}

// The speaker's own two documents, and the two DIFFERENT things they can say:
// a real out-of-box setup, which takes the Wi-Fi down, and #367's merely stuck
// source, which does not.
func TestBoxSetupPhaseReadsTheSpeakersOwnDocuments(t *testing.T) {
	cases := []struct {
		name, setup, nowPlaying string
		wantOOB, wantStuck      bool
	}{
		{"a factory-fresh box raising its own AP",
			`<setup state="SETUP_AP_OOB" systemstate="SETUP_LANG_NOT_SET"/>`, "", true, false},
		{"the language gate is still open",
			`<setup state="SETUP_INACTIVE" systemstate="SETUP_LANG_NOT_SET"/>`, "", true, false},
		// The blocker of 2026-09-08: SETUP_LANG_SET is what a FULLY configured
		// speaker reports, and a generic SETUP_ prefix rule called every
		// healthy box on the LAN mid-setup - which armed the 25-minute
		// watcher on every install and buried every real failure behind
		// "wait fifteen minutes".
		{"a fully configured speaker playing radio",
			`<setup state="SETUP_INACTIVE" systemstate="SETUP_LANG_SET"/>`, `<nowPlaying source="UPNP"/>`, false, false},
		{"a fully configured speaker in standby",
			`<setup state="SETUP_INACTIVE" systemstate="SETUP_LANG_SET"/>`, `<nowPlaying source="STANDBY"/>`, false, false},
		// A stuck source is its own, weaker verdict: the speaker is fully on
		// the network, so it must never claim the out-of-box setup.
		{"the source is stuck on setup while /setup says it is done (#367)",
			`<setup state="SETUP_INACTIVE" systemstate="SETUP_LANG_SET"/>`, `<nowPlaying source="SETUP"/>`, false, true},
		{"nothing readable at all is not a setup claim", "", "", false, false},
		// Attribute ORDER must not decide the answer. A substring scan for
		// `state="` also hits inside `systemstate="`, so a body that emits
		// systemstate first made the SETUP_AP rule read the wrong attribute -
		// and every other fixture here happens to put state first, so nothing
		// caught it.
		{"systemstate before state, healthy speaker",
			`<setup systemstate="SETUP_LANG_SET" state="SETUP_INACTIVE"/>`, `<nowPlaying source="UPNP"/>`, false, false},
		{"systemstate before state, speaker raising its AP",
			`<setup systemstate="SETUP_LANG_NOT_SET" state="SETUP_AP_OOB"/>`, "", true, false},
		{"a body that does not parse claims nothing",
			`<setup state="SETUP_AP_OOB"`, "", false, false},
	}
	for _, c := range cases {
		oob, stuck := boxSetupPhase(c.setup, c.nowPlaying)
		if oob != c.wantOOB || stuck != c.wantStuck {
			t.Errorf("%s: boxSetupPhase = (oob=%v, stuck=%v), want (oob=%v, stuck=%v)",
				c.name, oob, stuck, c.wantOOB, c.wantStuck)
		}
	}
}

func stubBoxInSetup(t *testing.T, inSetup bool) {
	t.Helper()
	old := boxInSetupFn
	boxInSetupFn = func(*App, string) bool { return inSetup }
	t.Cleanup(func() { boxInSetupFn = old })
}

// shortWaitCeilings keeps the watcher's give-up path inside a test's patience.
func shortWaitCeilings(t *testing.T) {
	t.Helper()
	oldW, oldS, oldP := installWaitCeiling, setupWaitCeiling, installWaitPoll
	installWaitCeiling, setupWaitCeiling, installWaitPoll = 0, 0, 5*time.Millisecond
	t.Cleanup(func() { installWaitCeiling, setupWaitCeiling, installWaitPoll = oldW, oldS, oldP })
}

// A speaker running the firmware's OWN out-of-box setup gets its own verdict.
// It is not a failed install: STR is on the box, and nothing the user does to
// the app can end the state.
func TestAgentNotUpNamesTheSpeakersOwnSetupPhase(t *testing.T) {
	journal := useTempJournal(t)
	old := installLateRecheckDelay
	installLateRecheckDelay = time.Hour
	t.Cleanup(func() { installLateRecheckDelay = old })
	stubWaitFacts(t, agentWaitFacts{PingRan: true, BoseHTTP: true, BosePort: true})
	stubBoxInSetup(t, true)

	a := fastTestApp()
	res := a.agentNotUp(InstallResult{Step: "wait-agent"}, "192.0.2.36", "SoundTouch 10", "generic wording")
	if res.Code != speakerInSetupCode {
		t.Fatalf("code = %q, want %q", res.Code, speakerInSetupCode)
	}
	for _, want := range []string{"its own out-of-box setup", "blinks fast", "already on the speaker"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("message lacks %q:\n%s", want, res.Message)
		}
	}
	if strings.Contains(res.Message, "generic wording") {
		t.Error("the generic agent-not-up wording leaked into the in-setup message")
	}
	if j := readJournal(t, journal); !strings.Contains(j, "running its OWN out-of-box setup") {
		t.Errorf("journal lacks the setup reason:\n%s", j)
	}
}

// The genuinely dead install still fails fast: the speaker answers its own
// Bose port, it is NOT in setup, so the strict wording stands. This is the
// case the three-way verdict must not have gone soft on.
func TestAgentNotUpKeepsTheStrictVerdictForALiveSpeaker(t *testing.T) {
	useTempJournal(t)
	stubWaitFacts(t, agentWaitFacts{PingRan: true, PingAlive: true, BoseHTTP: true})
	stubBoxInSetup(t, false)

	a := fastTestApp()
	res := a.agentNotUp(InstallResult{Step: "wait-agent"}, "192.0.2.36", "SoundTouch 10", "generic wording")
	if res.Code != "agent-not-up" || res.Message != "generic wording" {
		t.Fatalf("live speaker: code=%q message=%q, want agent-not-up with the caller's wording", res.Code, res.Message)
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
	stubBoxInSetup(t, false)

	a := fastTestApp()
	res := a.agentNotUp(InstallResult{Step: "wait-agent"}, "192.0.2.36", "SoundTouch 10", "generic wording")
	if res.OK {
		t.Fatal("result reads as success")
	}
	if res.Code != speakerNotBackCode {
		t.Errorf("code = %q, want %q", res.Code, speakerNotBackCode)
	}
	for _, want := range []string{"not back on the network yet", "still restarting", "Your network is not the problem"} {
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
	stubBoxInSetup(t, false)

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
	stubBoxInSetup(t, false)
	shortWaitCeilings(t)

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
	stubBoxInSetup(t, false)

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
