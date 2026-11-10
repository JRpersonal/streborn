package main

import (
	"testing"
	"time"
)

// testInstallSeams gives a its own copy of the production install seams and
// returns it, so a test replaces the ones it scripts and keeps the rest.
// Called before the code under test runs; nothing is restored afterwards,
// because nothing outside this App was changed.
func testInstallSeams(a *App) *installSeams {
	if a.instSeams == nil {
		a.instSeams = newInstallSeams(a)
	}
	return a.instSeams
}

// testInstallPolicy gives a its own copy of the production install waits and
// returns it.
func testInstallPolicy(a *App) *installPolicy {
	if a.instPolicy == nil {
		p := installProductionPolicy
		a.instPolicy = &p
	}
	return a.instPolicy
}

// The production waits are field-learned. Moving them into installPolicy must
// not have changed one of them, and no later edit may do so in passing.
func TestInstallProductionPolicyValues(t *testing.T) {
	want := installPolicy{
		lateRecheckDelay:      120 * time.Second,
		waitPoll:              20 * time.Second,
		waitCeiling:           15 * time.Minute,
		setupWaitCeiling:      15 * time.Minute,
		hungRebootCheckAfter:  60 * time.Second,
		hungRebootCheckWindow: 10 * time.Minute,
		firmwareSettle:        45 * time.Second,
		firmwarePoll:          5 * time.Second,
	}
	if installProductionPolicy != want {
		t.Fatalf("production install policy changed:\n got %+v\nwant %+v", installProductionPolicy, want)
	}
	if got := (&App{}).installPolicy(); got != want {
		t.Fatalf("an App without an override must run the production policy, got %+v", got)
	}
}

// An App without an override gets the production wiring, complete: a nil seam
// would only show as a panic in the middle of somebody's install.
func TestInstallSeamsProductionWiringIsComplete(t *testing.T) {
	s := (&App{}).installSeams()
	for name, set := range map[string]bool{
		"ensureEngine":       s.ensureEngine != nil,
		"sleep":              s.sleep != nil,
		"repairFreeBytes":    s.repairFreeBytes != nil,
		"repairMemAvailable": s.repairMemAvailable != nil,
		"repairStopSTR":      s.repairStopSTR != nil,
		"repairStageUpload":  s.repairStageUpload != nil,
		"waitFacts":          s.waitFacts != nil,
		"runningLate":        s.runningLate != nil,
		"inSetup":            s.inSetup != nil,
		"hungRebootProbe":    s.hungRebootProbe != nil,
		"forceReboot":        s.forceReboot != nil,
		"now":                s.now != nil,
		"readFirmware":       s.readFirmware != nil,
	} {
		if !set {
			t.Errorf("production seam %s is not wired", name)
		}
	}
}

// Two Apps do not share seams or waits: what one test scripts cannot reach
// another's App, which is what made the old package-level seams a data race
// (#1547).
func TestInstallSeamsArePerApp(t *testing.T) {
	a, b := &App{}, &App{}
	testInstallSeams(a).now = func() time.Time { return time.Unix(1, 0) }
	testInstallPolicy(a).waitPoll = time.Millisecond
	if got := b.installSeams().now(); got.Unix() == 1 {
		t.Error("a seam set on one App reached another")
	}
	if b.installPolicy().waitPoll != installProductionPolicy.waitPoll {
		t.Error("a wait set on one App reached another")
	}
	if installProductionPolicy.waitPoll != 20*time.Second {
		t.Error("a wait set on an App changed the production policy")
	}
}
