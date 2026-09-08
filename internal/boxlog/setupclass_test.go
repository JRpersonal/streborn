package boxlog

// The setup / network state machine, and the tail that survives it.
//
// #873: two reporters' bundles could not answer what the firmware was doing
// while the speaker was off the LAN, because none of these line families had a
// class and the 150-line rolling tail had rolled them out long before the
// bundle was exported.

import (
	"strings"
	"testing"
	"time"
)

func TestClassifyTheSetupAndNetworkFamilies(t *testing.T) {
	cases := []struct {
		name string
		line string
		want Class
	}{
		{"the firmware's own Wi-Fi state machine",
			"Sep  6 20:50:19 rhino user.info BoseApp[512]: [(1234):WifiConfig:INFO]ChangeState(WifiConfigTopState >> WifiConfigSetupAP)",
			ClassSetupState},
		{"the system setup event",
			"Sep  6 20:50:19 rhino user.info BoseApp[512]: [(1234):SysCtrl:INFO]handling EVT_SYSTEM_SETUP",
			ClassSetupState},
		{"the setup state reply",
			"Sep  6 20:50:19 rhino user.info BoseApp[512]: [(1234):WebServer:INFO]setupStateResponse sent",
			ClassSetupState},
		{"the access point itself",
			"Sep  6 20:50:24 rhino user.info BoseApp[512]: [(1234):NetMgr:INFO]switching to ACCESS_POINT mode",
			ClassSetupAP},
		{"NetManager losing its control socket",
			"Sep  6 20:50:25 rhino user.err NetManager[733]: [(99):WiFiManager:ERROR]wpa_ctrl_request failed: SIGNAL_POLL (Transport endpoint is not connected)",
			ClassNetManager},
		{"the supplicant's own association events",
			"Sep  6 20:50:30 rhino daemon.info wpa_supplicant[401]: wlan0: CTRL-EVENT-SCAN-STARTED ",
			ClassWPASupplicant},
		{"the DHCP client",
			"Sep  6 20:50:41 rhino daemon.info udhcpc[820]: Sending discover...",
			ClassDHCP},
	}
	for _, c := range cases {
		l, ok := ParseLine(c.line)
		if !ok {
			t.Fatalf("%s: the line does not parse at all", c.name)
		}
		got, classified := Classify(l)
		if !classified || got != c.want {
			t.Errorf("%s: Classify = %q (ok=%v), want %q", c.name, got, classified, c.want)
		}
	}
}

// The standby/wake classifier reads the same ChangeState shape, and it must
// keep winning for the HSM lines it was written for.
func TestSetupGatesDoNotSwallowTheStandbyTransitions(t *testing.T) {
	l, ok := ParseLine("Sep  6 21:02:11 rhino user.info BoseApp[512]: [(1234):HSM:INFO]ChangeState(PlayingState >> StandbyState)")
	if !ok {
		t.Fatal("the line does not parse")
	}
	if got, _ := Classify(l); got != ClassStandby {
		t.Errorf("Classify = %q, want %q", got, ClassStandby)
	}
}

// The chatty families must stay out of the agent log, which is mirrored to
// NAND on every write (SCHONE die Box-Hardware).
func TestOnlyTheDecisiveSetupClassesReachTheAgentLog(t *testing.T) {
	for _, c := range []Class{ClassSetupState, ClassSetupAP, ClassNetManager} {
		if c.logGap() != rareLogGap {
			t.Errorf("%s should reach the agent log at the rare gap, got %s", c, c.logGap())
		}
	}
	for _, c := range []Class{ClassWPASupplicant, ClassDHCP} {
		if c.logGap() != 0 {
			t.Errorf("%s must be ring-only, got a log gap of %s", c, c.logGap())
		}
	}
}

// The freeze: on the first ACCESS POINT event the rolling tail is snapshotted
// and keeps filling for a bounded number of further lines. Without it a
// fifteen-minute episode rolls straight out of a 150-line ring.
func TestSetupTailFreezesTheEpisode(t *testing.T) {
	f := newForensics()
	now := time.Now()
	const apLine = "Sep  6 20:50:24 rhino user.info BoseApp[512]: [(1234):NetMgr:INFO]switching to ACCESS_POINT mode"

	// Nothing frozen on a healthy box: that is the answer for that box, and it
	// must not cost a buffer.
	if snap := f.setupTailSnapshot(); snap["frozen"] != false {
		t.Fatalf("a box that never entered setup reports %v", snap["frozen"])
	}

	// Fill the rolling tail past its capacity so the snapshot has to be a copy
	// of what is CURRENTLY in it, not of everything ever seen.
	for i := 0; i < tailRingLen+20; i++ {
		f.observe("Sep  6 20:40:00 rhino user.info BoseApp[512]: [(1):Misc:INFO]filler line", now)
	}
	f.observe(apLine, now)

	lines, trigger, ok := f.takeFreezeNotice()
	if !ok {
		t.Fatal("the freeze produced no notice for the agent log")
	}
	if trigger != ClassSetupAP {
		t.Errorf("trigger = %q, want %q", trigger, ClassSetupAP)
	}
	if lines < tailRingLen {
		t.Errorf("the frozen tail carries %d lines, want at least the ring's %d", lines, tailRingLen)
	}
	// Claimed once, so the log line cannot repeat per line for the rest of the
	// episode.
	if _, _, again := f.takeFreezeNotice(); again {
		t.Error("the freeze notice can be claimed twice")
	}

	// Everything after the trigger keeps landing in the frozen buffer, up to
	// the follow-up cap, and then it stops.
	for i := 0; i < setupTailFollow+50; i++ {
		f.observe("Sep  6 20:50:30 rhino daemon.info wpa_supplicant[401]: wlan0: CTRL-EVENT-SCAN-STARTED ", now)
	}
	snap := f.setupTailSnapshot()
	if snap["frozen"] != true || snap["complete"] != true {
		t.Fatalf("after the follow-up window the tail should be frozen and complete, got %v", snap)
	}
	got, _ := snap["lines"].([]string)
	if len(got) > setupTailLen {
		t.Errorf("the frozen buffer grew to %d lines, past its %d bound", len(got), setupTailLen)
	}
	// The trigger line itself has to be in there, or the snapshot answers the
	// wrong question.
	found := false
	for _, l := range got {
		if strings.Contains(l, "ACCESS_POINT") {
			found = true
			break
		}
	}
	if !found {
		t.Error("the frozen tail lost the line that triggered it")
	}
}

// The firmware's own Wi-Fi state machine must NOT arm the freeze.
// "WifiConfigTopState" logs during an ordinary association at every boot, and
// "setupStateResponse" is provoked by STR's own /setup reads: arming on either
// spends the one-shot capture on the boot and leaves the real episode, minutes
// later, recorded nowhere.
func TestSetupStateLinesDoNotArmTheFreeze(t *testing.T) {
	f := newForensics()
	now := time.Now()
	f.observe("Sep  6 20:40:19 rhino user.info BoseApp[512]: [(1):WifiConfig:INFO]ChangeState(WifiConfigTopState >> WifiConfigIdle)", now)
	f.observe("Sep  6 20:40:20 rhino user.info BoseApp[512]: [(1):WebServer:INFO]setupStateResponse sent", now)
	if _, _, froze := f.takeFreezeNotice(); froze {
		t.Fatal("an ordinary boot's Wi-Fi state machine armed the setup tail")
	}
	if snap := f.setupTailSnapshot(); snap["frozen"] != false {
		t.Fatalf("the tail is frozen without an access point event: %v", snap)
	}
	// The real episode, later, still gets its capture.
	f.observe("Sep  6 20:50:24 rhino user.info BoseApp[512]: [(1234):NetMgr:INFO]switching to ACCESS_POINT mode", now.Add(10*time.Minute))
	if _, trigger, froze := f.takeFreezeNotice(); !froze || trigger != ClassSetupAP {
		t.Errorf("the access point event did not arm the freeze (froze=%v trigger=%q)", froze, trigger)
	}
}

// One capture at a time, and at most one replacement per agent run: a box that
// flaps in and out of setup keeps the FIRST episode while it is still filling,
// but a capture that completed and was followed by real quiet may be replaced
// once, so a freeze spent on an unrelated line does not cost the whole run.
func TestSetupTailFreezesOnlyOnceWithOneRearm(t *testing.T) {
	f := newForensics()
	now := time.Now()
	const trigger = "Sep  6 20:50:24 rhino user.info BoseApp[512]: [(1234):NetMgr:INFO]switching to ACCESS_POINT mode"
	f.observe(trigger, now)
	first, _, _ := f.takeFreezeNotice()
	if first == 0 {
		t.Error("the first freeze captured nothing")
	}
	// Still filling: a second event inside the same episode must not restart it.
	f.observe(trigger, now.Add(time.Minute))
	if _, _, again := f.takeFreezeNotice(); again {
		t.Error("an event inside the same episode re-armed the freeze")
	}
	// Complete it, then let the box go genuinely quiet for longer than the
	// re-arm gap before the next access-point line.
	for i := 0; i < setupTailFollow; i++ {
		f.observe("Sep  6 20:50:30 rhino daemon.info wpa_supplicant[401]: wlan0: CTRL-EVENT-SCAN-STARTED ", now)
	}
	quiet := now.Add(time.Minute).Add(setupRearmQuiet + time.Minute)
	f.observe(trigger, quiet)
	if _, _, again := f.takeFreezeNotice(); !again {
		t.Error("a completed capture after real quiet must be replaceable once")
	}
	// And only once.
	f.mu.Lock()
	f.setupDone = true
	f.mu.Unlock()
	f.observe(trigger, quiet.Add(2*setupRearmQuiet))
	if _, _, again := f.takeFreezeNotice(); again {
		t.Error("the freeze re-armed a second time; the budget is one")
	}
}

// The regression the re-arm gate was rewritten for (2026-09-08): a live
// episode must never lose its opening snapshot.
//
// The gate used to read the AGE OF THE FREEZE, so a capture that completed and
// then simply sat there for the rest of a fifteen-minute episode was "stale"
// and the very next access-point line - still inside that same episode -
// replaced it. What that threw away is the only part nothing else can
// reconstruct: the lines from BEFORE the speaker went into setup. The gate now
// reads quiet time since the last setup event instead, and an episode that
// keeps re-raising its access point is by definition not quiet.
func TestSetupTailKeepsTheOpeningSnapshotThroughALongEpisode(t *testing.T) {
	f := newForensics()
	now := time.Now()
	const apLine = "Sep  6 20:50:24 rhino user.info BoseApp[512]: [(1234):NetMgr:INFO]switching to ACCESS_POINT mode"
	const opener = "Sep  6 20:50:20 rhino user.info BoseApp[512]: [(1):Misc:INFO]OPENING-MARKER before the speaker went into setup"

	f.observe(opener, now)
	f.observe(apLine, now)
	if _, _, froze := f.takeFreezeNotice(); !froze {
		t.Fatal("the first access-point line did not arm the freeze")
	}
	// Complete the capture, exactly as a real episode does within seconds.
	for i := 0; i < setupTailFollow; i++ {
		f.observe("Sep  6 20:50:30 rhino daemon.info wpa_supplicant[401]: wlan0: CTRL-EVENT-SCAN-STARTED ", now)
	}
	// Fifteen more minutes of the SAME episode, with the firmware re-raising
	// the access point every three minutes. Every one of these is older than
	// the freeze by more than the old age gate allowed.
	for i := 1; i <= 5; i++ {
		at := now.Add(time.Duration(i) * 3 * time.Minute)
		f.observe(apLine, at)
		if _, _, again := f.takeFreezeNotice(); again {
			t.Fatalf("the freeze re-armed %d minutes into a continuing episode", i*3)
		}
	}
	snap := f.setupTailSnapshot()
	if snap["rearmed"] != nil {
		t.Errorf("a continuing episode re-armed the capture: %v", snap["rearmed"])
	}
	lines, _ := snap["lines"].([]string)
	found := false
	for _, l := range lines {
		if strings.Contains(l, "OPENING-MARKER") {
			found = true
			break
		}
	}
	if !found {
		t.Error("the episode-start snapshot was discarded while the episode was still running")
	}
}
