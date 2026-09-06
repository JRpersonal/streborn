package boxlog

import (
	"strings"
	"time"
)

// Wake signal: the firmware writes every standby and wake transition into
// its syslog ring, from two daemons. BoseApp's system controller (HSM) logs
// the state change itself ("ChangeState(On >> Standby)", "ChangeState(Standby
// >> ProductSrcChangeWaiting)"), and on the scm chassis scmmond adds its
// low-power notification ("SCMMOND_NETWORK_STANDBY" going down,
// "SCMMOND_WAKEUP_NETWORK_STANDBY" coming back) plus the sleep stages. That
// makes the ring a wake signal the agent can act on where the gabbo bus is
// silent: some chassis never send a powerStateUpdated frame, and the source
// change frame that stands in for it is lost while the WebSocket is between
// its idle recycles. The classifier below folds the several lines one
// transition produces into ONE PowerEvent; the agent dedupes it against what
// the bus reported (see cmd/agent). No timer, no probe, no NAND.
//
// Measured 2026-09-06 on a Portable/taigan: standby is logged by HSM first
// and by scmmond four seconds later (then the sleep stage); a wake is logged
// by scmmond first and by HSM one second later.

// PowerKind is the direction of a transition.
type PowerKind string

const (
	// PowerStandby is the speaker going to standby.
	PowerStandby PowerKind = "standby"
	// PowerWake is the speaker leaving standby.
	PowerWake PowerKind = "wake"
)

// PowerSource names the daemon whose line carried the transition.
type PowerSource string

const (
	// PowerSourceHSM is BoseApp's system controller state machine.
	PowerSourceHSM PowerSource = "hsm"
	// PowerSourceScmmond is the scm chassis power daemon.
	PowerSourceScmmond PowerSource = "scmmond"
)

// PowerEvent is one standby or wake transition read from the syslog ring.
type PowerEvent struct {
	Kind   PowerKind
	Source PowerSource
	// At is when the first line of the transition was read.
	At time.Time
	// Class is the raw class of that first line (standby, wake, power_event,
	// power_sleep).
	Class Class
}

// PowerHandler receives every deduplicated PowerEvent, on the reader's
// goroutine. It must return quickly; anything slow belongs in a goroutine of
// its own.
type PowerHandler func(PowerEvent)

// powerDedupeWindow is how long after the first line of a transition the
// other daemon's line about the same transition is folded into it. The
// measured spread is four seconds (HSM standby, then scmmond); the window
// is generous against a loaded box, and a transition in the other direction
// is never folded, so a quick off/on inside the window still yields both
// events.
const powerDedupeWindow = 10 * time.Second

// powerTransition maps a classified event to a transition. ok is false for
// the classes that carry no direction: "sets processor clock" and "Go Sleep
// stage" are logged both ways, and a low power notification with a client
// name that was never measured is left alone rather than guessed.
func powerTransition(ev Event) (kind PowerKind, source PowerSource, ok bool) {
	switch ev.Class {
	case ClassStandby:
		return PowerStandby, PowerSourceHSM, true
	case ClassWake:
		return PowerWake, PowerSourceHSM, true
	case ClassPowerEvent:
		switch {
		case strings.Contains(ev.Message, "SCMMOND_WAKEUP_NETWORK_STANDBY"):
			return PowerWake, PowerSourceScmmond, true
		case strings.Contains(ev.Message, "SCMMOND_NETWORK_STANDBY"):
			return PowerStandby, PowerSourceScmmond, true
		}
	case ClassPowerSleep:
		// "Go Sleep stage N" is logged on the way DOWN and on the way UP (the
		// Portable printed "Go Sleep stage 5" together with the WAKEUP
		// notification, 2026-09-06 22:32), so it carries no direction either.
		return "", "", false
	}
	return "", "", false
}

// powerEvent folds ev into the current transition. It returns the PowerEvent
// to deliver and true when ev is the FIRST line of a new transition; a line
// of the same direction inside powerDedupeWindow of that first line is a
// duplicate and is only counted.
func (f *forensics) powerEvent(ev Event) (PowerEvent, bool) {
	kind, source, ok := powerTransition(ev)
	if !ok {
		return PowerEvent{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastPower.Kind == kind && ev.At.Sub(f.lastPower.At) < powerDedupeWindow {
		f.powerDuplicates++
		return PowerEvent{}, false
	}
	f.lastPower = PowerEvent{Kind: kind, Source: source, At: ev.At, Class: ev.Class}
	f.powerTransitions++
	return f.lastPower, true
}

// powerSnapshot is the wake-signal part of the box_syslog_events section.
func (f *forensics) powerSnapshot() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]any{
		"transitions": f.powerTransitions,
		"duplicates":  f.powerDuplicates,
	}
	if f.lastPower.Kind != "" {
		out["lastKind"] = string(f.lastPower.Kind)
		out["lastSource"] = string(f.lastPower.Source)
		out["lastClass"] = string(f.lastPower.Class)
		out["lastAt"] = f.lastPower.At.Format(time.RFC3339)
	}
	return out
}

// SetPowerHandler installs the hook that receives one PowerEvent per standby
// or wake transition the firmware logs. Set before Run; a nil handler
// disables the hook.
func (r *Reader) SetPowerHandler(h PowerHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.powerHandler = h
}

// firePower runs the dedupe and hands a new transition to the hook.
func (r *Reader) firePower(ev Event) {
	pev, ok := r.forensics.powerEvent(ev)
	if !ok {
		return
	}
	r.mu.Lock()
	h := r.powerHandler
	r.mu.Unlock()
	if h != nil {
		h(pev)
	}
}
