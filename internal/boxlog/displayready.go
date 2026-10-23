package boxlog

import "strings"

// The display controller's boot.
//
// BoseApp's DisplayController is an HSM like the system controller, and the
// firmware logs every message it handles with the state path it is in, for
// example
//
//	DisplayController: HandleMessage(EVT_SYSTEM_STATUS_STANDBY) >> Bootup > Main > Top [ChangeState(Bootup >> StandbyNoClock), Bootup(EXIT)] ...
//	DisplayController: HandleMessage(PDO_CLOCK_TIME_UPDATE) >> StandbyNoClock [ChangeState(StandbyNoClock >> Standby), ...]
//
// While it is in Bootup the DirectFB flip thread behind the panel is still
// being set up. Holding that thread then, as the agent's splash does right
// after an update reboot, left an ST30 with a panel the firmware never drew
// on again: the clock state machine ran on, logged its transitions, answered
// every order to draw the standby clock, and the glass stayed black until the
// next reboot (Wohnzimmer, 2026-10-23, the splash started 4 s after the agent
// and 72 s before BoseApp's HTTP came up). STR could still draw on it, so the
// panel and the agent's own path were fine; only the firmware's flip was gone.
//
// The reader therefore remembers when it saw the display controller leave
// Bootup, and the splash waits for that.

// displayControllerPastBootup reports whether a syslog line shows the display
// controller in a state after Bootup, or leaving it.
func displayControllerPastBootup(line string) bool {
	i := strings.Index(line, "DisplayController: HandleMessage(")
	if i < 0 {
		return false
	}
	rest := line[i:]
	if strings.Contains(rest, "ChangeState(Bootup >>") {
		return true
	}
	j := strings.Index(rest, ">> ")
	if j < 0 {
		return false
	}
	state := strings.TrimSpace(rest[j+3:])
	return state != "" && !strings.HasPrefix(state, "Bootup")
}

// DisplayControllerUp reports whether the firmware's display controller has
// been seen past its boot state since the reader started. False on a reader
// that never saw a display controller line at all: a speaker without a
// display never logs one, and so does a BoseApp that booted before the
// reader followed the log, so callers pair this with the age of BoseApp.
func (r *Reader) DisplayControllerUp() bool {
	return r.displayUp.Load()
}

func (r *Reader) noteDisplayLine(line string) {
	if r.displayUp.Load() || !strings.Contains(line, "DisplayController") {
		return
	}
	if displayControllerPastBootup(line) {
		r.displayUp.Store(true)
	}
}
