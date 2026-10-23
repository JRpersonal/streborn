package boxlog

import (
	"testing"
	"time"
)

// The three lines are the ST30's own from the boot of 2026-10-23 19:18, in
// their order: the controller entering standby from Bootup (leaving it in the
// same message), the no-clock standby re-entered, and the clock arriving.
const (
	lineDisplayLeavesBootup = `Oct 23 19:18:12 BoseApp HSM:INFO DisplayController: HandleMessage(EVT_SYSTEM_STATUS_STANDBY) >> Bootup > Main > Top [ChangeState(Bootup >> StandbyNoClock), Bootup(EXIT)] [Standby(ENTER), StandbyNoClock(ENTER), StandbyNoClock(START)]`
	lineDisplayNoClock      = `Oct 23 19:18:12 BoseApp HSM:INFO DisplayController: HandleMessage(EVT_SYSTEM_STATUS_STANDBY) >> StandbyNoClock > Standby > Main > Top [ChangeState(StandbyNoClock >> StandbyNoClock), StandbyNoClock(EXIT)] [StandbyNoClock(ENTER), StandbyNoClock(START)]`
	lineDisplayClock        = `Oct 23 19:19:01 BoseApp HSM:INFO DisplayController: HandleMessage(PDO_CLOCK_TIME_UPDATE) >> StandbyNoClock [ChangeState(StandbyNoClock >> Standby), StandbyNoClock(EXIT)] [Standby(START)]`
	lineDisplayInBootup     = `Oct 23 19:18:10 BoseApp HSM:INFO DisplayController: HandleMessage(EVT_DISPLAY_INIT) >> Bootup > Main > Top`
	lineSysCtlrStandby      = `Oct 23 19:18:12 BoseApp HSM:INFO SysCtlr: HandleMessage(EVT_SYSTEM_SW_UPDATE_VALIDATE_STATUS) >> Bootup [ChangeState(Bootup >> Standby), Bootup(EXIT)] [StdOp(ENTER), Standby(ENTER), Standby(START)]`
)

func TestDisplayControllerPastBootup(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		line string
		want bool
	}{
		{"leaving Bootup counts", lineDisplayLeavesBootup, true},
		{"standby without clock is past boot", lineDisplayNoClock, true},
		{"clock state is past boot", lineDisplayClock, true},
		{"still in Bootup", lineDisplayInBootup, false},
		{"the system controller is not the display", lineSysCtlrStandby, false},
		{"unrelated line", "Oct 23 19:18:12 TPDA Network:INFO TcpSocket::Open", false},
		{"controller named but no state", "DisplayController: HandleMessage(X)", false},
	}
	for _, c := range cases {
		if got := displayControllerPastBootup(c.line); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// The reader latches the first sighting and keeps it; lines that show the
// controller still booting leave it unset.
func TestReaderRemembersTheDisplayControllerPastBoot(t *testing.T) {
	t.Parallel()
	r := New(nil, nil, nil)
	base := time.Date(2026, 10, 23, 17, 18, 10, 0, time.UTC)
	if r.DisplayControllerUp() {
		t.Fatal("up before any line")
	}
	r.InjectLine(lineDisplayInBootup, base)
	r.InjectLine(lineSysCtlrStandby, base.Add(2*time.Second))
	if r.DisplayControllerUp() {
		t.Fatal("up while the controller is still in Bootup")
	}
	r.InjectLine(lineDisplayLeavesBootup, base.Add(2*time.Second))
	if !r.DisplayControllerUp() {
		t.Fatal("not up after the controller left Bootup")
	}
	r.InjectLine(lineDisplayInBootup, base.Add(3*time.Second))
	if !r.DisplayControllerUp() {
		t.Fatal("a later Bootup line must not clear the latch")
	}
}
