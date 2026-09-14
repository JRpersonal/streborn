package boxlog

import (
	"testing"
	"time"
)

// The #960 reporter's ST10 wrote 78 of the 181 lines in its thirteen-hour NAND
// log as one repeating setup_state line, on a box that played music all night
// and never entered setup once. The NAND log is the only one that survives a
// reboot and it is a 32 KB tail, so that poll was pushing the history a bundle
// needs straight out of it.
func TestSetupStatePollStaysOutOfTheAgentLog(t *testing.T) {
	polls := []string{
		`Sep 13 19:50:22 rhino user.info BoseApp[512]: [(1234):WifiConfigTopState:INFO]HandleMessage: EVT_SYSTEM_SETUP: SETUP_ENTER, RequestType =1`,
		`Sep 13 19:50:22 rhino user.info BoseApp[512]: [(1234):WifiConfigTopState:INFO]HandleMessage: EVT_SYSTEM_SETUP: ROM_GET , so sending response`,
		`Sep 13 19:50:22 rhino user.info BoseApp[512]: [(1234):WifiConfigTopState:INFO]WifiConfigTopState rsp(<?xml version="1.0" encoding="UTF-8" ?><setupStateResponse state="SETUP_INACTIVE" systemstate="SETUP_LANG_SET" />)`,
		`Sep 13 19:50:22 rhino user.info BoseApp[512]: [(1234):SYSCTRL-STD-OP-STATE:INFO]EVT_SYSTEM_SETUP: GET(SETUP_ENTER)`,
		`Sep 13 19:50:22 rhino user.info BoseApp[512]: [(1234):HSM:INFO]WifiConfig: HandleMessage(EVT_SYSTEM_SETUP) >> IdleState > Top`,
	}
	f := newForensics()
	now := time.Now()
	for i, line := range polls {
		ev, logIt, ok := f.observe(line, now.Add(time.Duration(i)*time.Hour))
		if !ok {
			t.Fatalf("line %d was not classified at all: %s", i, line)
		}
		if ev.Class != ClassSetupState {
			t.Errorf("line %d: class = %q, want %q", i, ev.Class, ClassSetupState)
		}
		// An hour apart, so the rate limiter is not what is holding them back.
		if logIt {
			t.Errorf("line %d reached the agent log: %s", i, line)
		}
	}
	if f.classified != uint64(len(polls)) {
		t.Errorf("the ring kept %d of %d lines, it must keep them all", f.classified, len(polls))
	}
}

// The gate is a list of query shapes, not a list of interesting ones, so a
// setup_state line it does not recognise still reaches the log. That is what
// keeps the #873 evidence this class was added for.
func TestARealSetupLineStillReachesTheAgentLog(t *testing.T) {
	f := newForensics()
	line := `Sep  6 20:50:24 rhino user.info BoseApp[512]: [(1234):WifiConfigTopState:INFO]ChangeState(WifiConfigTopState >> WifiConfigSetupState)`
	ev, logIt, ok := f.observe(line, time.Now())
	if !ok || ev.Class != ClassSetupState {
		t.Fatalf("class = %q ok=%v, want %q", ev.Class, ok, ClassSetupState)
	}
	if !logIt {
		t.Error("a setup_state line that is not a query must still be logged")
	}
}
