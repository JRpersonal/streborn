package boxlog

import "testing"

// The exact line a speaker logs when it refuses a source because its group is
// not whole, taken from a field bundle (2026-09-29). The speaker was the RIGHT
// half of a stereo pair whose LEFT partner had been off the network for 18 days:
// it woke on every attempt and was back in standby five seconds later, and the
// owner saw only "box_not_ready".
func TestTheFirmwareGroupErrorIsClassified(t *testing.T) {
	line := Line{
		Process:  "BoseApp",
		Facility: "HSM",
		Message:  "SysCtlr: HandleMessage(EVT_SYSTEM_GROUP_STATE_IN_ERROR) >> SrcActivate > SrcSelect > On > StdOpOn [ChangeState(SrcActivate >> Standby), SrcActivate(EXIT), SrcSelect(EXIT), On(EXIT), StdOpOn(EXIT)] [Standby(ENTER), Standby(START)]",
	}
	got, ok := Classify(line)
	if !ok {
		t.Fatal("the group error was not classified at all, which is how it stayed invisible")
	}
	if got != ClassGroupError {
		t.Errorf("classified as %q, want %q: the line carries its own ChangeState clause and must not be filed as an ordinary standby transition", got, ClassGroupError)
	}
}

// The ordinary standby transition must keep its own class, or every sleep would
// read as a broken group.
func TestAnOrdinaryStandbyIsStillAStandby(t *testing.T) {
	line := Line{
		Process:  "BoseApp",
		Facility: "HSM",
		Message:  "SysCtlr: HandleMessage(EVT_POWER_OFF) >> On [ChangeState(On >> Standby), On(EXIT)] [Standby(ENTER)]",
	}
	got, ok := Classify(line)
	if !ok || got == ClassGroupError {
		t.Errorf("an ordinary standby was classified as %q (ok=%v), which would cry group error on every sleep", got, ok)
	}
}
