package main

import (
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
)

// Wake signal from two origins. The gabbo bus reports a standby entry and a
// standby exit as source changes, and cmd/agent acts on them through
// OnEnterStandby / OnStandbyExit. Some chassis never send the power frame,
// and the source change is lost while the WebSocket is between its idle
// recycles, so the same two transitions are also read from the firmware's
// syslog ring (internal/boxlog, PowerEvent). Both origins describe the same
// physical moment and must reach the handler once: whichever arrives first
// is delivered, the other one inside powerSignalWindow is a duplicate. The
// handler's actions are unchanged, only the door they come through is.
//
// What a syslog transition is mapped to, deliberately:
//   - wake -> OnStandbyExit. The HSM line is the system controller leaving
//     Standby, which is exactly what the STANDBY -> X source change means.
//     OnPowerWake is NOT fired from the ring: on the bus it additionally
//     says the box restored one of STR's own selections it cannot play
//     (DO_NOT_RESUME), which the ring cannot tell, and ResumeLastPlay on a
//     box that woke to Bluetooth or AUX would start a stream the bus never
//     would have.
//   - standby -> OnEnterStandby, under the dispatcher's own gate (STR's
//     UPnP source was the active one, boxws.Client.UPnPActiveRecently), so a
//     power-off of AUX or a native station is left alone exactly as before.
//
// The standby door is NOT first-come while the bus is live. The standby
// handler (webui.HandleEnterStandby) tells a user power-off from the firmware
// dropping the source on its own by the age of the last user key, and the
// fresh stamp for a power press arrives on the bus (userActivityUpdate) in
// the same moment as the bus's own standby frame. The HSM line can be read
// from the ring before that frame lands; acting on it then classifies a
// deliberate power-off against a stale stamp, takes the spontaneous-drop
// branch (no stop latch, no #197 transport clear, possibly a re-push that
// switches the box back on) and the bus frame that carried the right answer
// is dropped as the duplicate. So while the socket is up the ring's standby
// is held for standbyHoldForBus (a one-shot timer, no polling): a bus
// delivery cancels it, a wake supersedes it, and only when the bus stays
// silent does the ring's report go through the door. With the socket down
// the ring delivers at once, as nothing else will.

// powerSignalWindow is how long after one origin delivered a transition the
// other origin's report of it counts as a duplicate. The bus frame and the
// HSM line are within about a second of each other; five seconds covers a
// loaded box and is still far shorter than any deliberate off/on.
const powerSignalWindow = 5 * time.Second

// standbyHoldForBus is how long a standby read from the ring waits for the
// bus's own report while the socket is up. A var so tests can shorten it.
var standbyHoldForBus = powerSignalWindow

// powerSignal is the handler door a transition goes through.
type powerSignal int

const (
	powerSignalStandby powerSignal = iota // OnEnterStandby
	powerSignalWake                       // OnStandbyExit
	powerSignalCount
)

func (s powerSignal) String() string {
	if s == powerSignalStandby {
		return "standby"
	}
	return "wake"
}

// powerOrigin is who reported the transition.
type powerOrigin string

const (
	originGabbo  powerOrigin = "gabbo"
	originSyslog powerOrigin = "syslog"
)

// powerGateEntry is the per-signal state.
type powerGateEntry struct {
	// deliveredAt / deliveredBy describe the newest delivery through this
	// door, from either origin.
	deliveredAt time.Time
	deliveredBy powerOrigin
	// seen stamps the newest report per origin, delivered or not, so a
	// bundle shows which origin reports at all on this chassis.
	seenGabbo  time.Time
	seenSyslog time.Time
}

// powerGate is the dedupe between the two origins. Zero value ready.
type powerGate struct {
	mu      sync.Mutex
	entries [powerSignalCount]powerGateEntry
	// heldStandby is the ring's standby report waiting for the bus (see the
	// file comment); nil when none is pending.
	heldStandby *time.Timer
	// counters for the debug section
	deliveredSyslog uint64
	dupSyslog       uint64
	dupGabbo        uint64
	heldSyslog      uint64
}

// holdStandby parks the ring's standby report until the bus has had
// standbyHoldForBus to deliver its own. release runs on the timer's goroutine
// if nothing cancels the hold. A hold already pending stands (the ring folds
// same-direction repeats anyway). Reports whether a new hold was started.
func (g *powerGate) holdStandby(now time.Time, release func()) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.entries[powerSignalStandby].seenSyslog = now
	if g.heldStandby != nil {
		return false
	}
	g.heldSyslog++
	g.heldStandby = time.AfterFunc(standbyHoldForBus, release)
	return true
}

// releaseStandby is the timer side of holdStandby: the hold is over, the
// caller admits the report as usual.
func (g *powerGate) releaseStandby() {
	g.mu.Lock()
	g.heldStandby = nil
	g.mu.Unlock()
}

// cancelHeldStandby drops a pending ring standby because the bus delivered
// the same transition (dup, the ring's copy was the duplicate after all) or
// because a wake superseded it. Reports whether a hold was stopped in time;
// a hold whose timer already fired admits itself through the gate, where a
// bus delivery inside powerSignalWindow still makes it the duplicate.
func (g *powerGate) cancelHeldStandby(dup bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.heldStandby == nil {
		return false
	}
	stopped := g.heldStandby.Stop()
	g.heldStandby = nil
	if stopped && dup {
		g.dupSyslog++
	}
	return stopped
}

// admit records a report and says whether it is to be delivered. A report
// is a duplicate when the OTHER origin delivered the same signal within
// powerSignalWindow; repeats from the same origin pass, so the bus path
// behaves exactly as it did without the ring (its handlers carry their own
// debounce). prev names the origin that already delivered when ok is false.
func (g *powerGate) admit(sig powerSignal, origin powerOrigin, now time.Time) (ok bool, prev powerOrigin, since time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := &g.entries[sig]
	if origin == originGabbo {
		e.seenGabbo = now
	} else {
		e.seenSyslog = now
	}
	if e.deliveredBy != "" && e.deliveredBy != origin && now.Sub(e.deliveredAt) < powerSignalWindow {
		if origin == originGabbo {
			g.dupGabbo++
		} else {
			g.dupSyslog++
		}
		return false, e.deliveredBy, now.Sub(e.deliveredAt)
	}
	e.deliveredAt = now
	e.deliveredBy = origin
	if origin == originSyslog {
		g.deliveredSyslog++
	}
	return true, "", 0
}

// snapshot is the "powerSignal" part of the box_syslog_events debug section:
// which origin last delivered standby and wake, when each origin last
// reported either, and the duplicate counts.
func (g *powerGate) snapshot() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	stamp := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format(time.RFC3339)
	}
	sb, wk := g.entries[powerSignalStandby], g.entries[powerSignalWake]
	return map[string]any{
		"lastStandbySource":   string(sb.deliveredBy),
		"lastStandbyAt":       stamp(sb.deliveredAt),
		"lastStandbySeen":     map[string]string{"gabbo": stamp(sb.seenGabbo), "syslog": stamp(sb.seenSyslog)},
		"lastWakeSource":      string(wk.deliveredBy),
		"lastWakeAt":          stamp(wk.deliveredAt),
		"lastWakeSeen":        map[string]string{"gabbo": stamp(wk.seenGabbo), "syslog": stamp(wk.seenSyslog)},
		"syslogDelivered":     g.deliveredSyslog,
		"syslogDuplicates":    g.dupSyslog,
		"gabboDuplicates":     g.dupGabbo,
		"syslogHeldForBus":    g.heldSyslog,
		"standbyHeld":         g.heldStandby != nil,
		"duplicateWindowSecs": int(powerSignalWindow / time.Second),
	}
}

// admitPower is the handler-side wrapper: it runs the gate and writes the
// one DEBUG line a suppressed duplicate gets. Both origins' reports of one
// transition are bounded by the window, so this cannot repeat chronically.
func (h *presetWsHandler) admitPower(sig powerSignal, origin powerOrigin) bool {
	ok, prev, since := h.powerGate.admit(sig, origin, time.Now())
	if !ok {
		h.logger.Debug("box power signal already delivered, suppressed as duplicate",
			"signal", sig.String(), "source", string(origin), "deliveredBy", string(prev), "sinceMs", since.Milliseconds())
	}
	return ok
}

// OnBoxPowerEvent receives one standby or wake transition read from the
// speaker's own syslog ring (internal/boxlog). It delivers it through the
// same door the gabbo bus uses, unless the bus was first; see the file
// comment for the mapping. Runs on the reader's goroutine, so the standby
// action (which may read the box) is moved off it; the gate decision itself
// is taken synchronously so a bus frame arriving a moment later is seen as
// the duplicate it is.
func (h *presetWsHandler) OnBoxPowerEvent(ev boxlog.PowerEvent) {
	switch ev.Kind {
	case boxlog.PowerWake:
		// A standby still waiting for the bus is stale now: the box is on
		// again, and delivering it late would latch a stop against a box the
		// user just switched on.
		if h.powerGate.cancelHeldStandby(false) {
			h.logger.Debug("box power signal: held standby superseded by a wake, dropped", "source", "syslog")
		}
		if !h.admitPower(powerSignalWake, originSyslog) {
			return
		}
		h.logger.Info("box power signal: the speaker left standby, acting on it",
			"source", "syslog", "via", string(ev.Source), "class", string(ev.Class))
		h.standbyExit()
	case boxlog.PowerStandby:
		if h.strSourceRecently != nil && !h.strSourceRecently() {
			// The bus would not have reported this one as STR's playback
			// being powered off, so it does not act on it either; the gate
			// still notes that the ring saw it.
			h.powerGate.mu.Lock()
			h.powerGate.entries[powerSignalStandby].seenSyslog = time.Now()
			h.powerGate.mu.Unlock()
			h.logger.Debug("box power signal: standby while STR's source was not active, left to the firmware",
				"source", "syslog", "via", string(ev.Source), "class", string(ev.Class))
			return
		}
		if h.busLive != nil && h.busLive() {
			// The bus is up, so its own standby frame (and the key stamp
			// that tells the classifier it was a power press) is expected
			// in a moment; let it be the one that acts. See the file comment.
			if h.powerGate.holdStandby(time.Now(), func() { h.releaseHeldStandby(ev) }) {
				h.logger.Debug("box power signal: standby read from the ring, held for the bus's own report",
					"source", "syslog", "via", string(ev.Source), "class", string(ev.Class), "holdMs", standbyHoldForBus.Milliseconds())
			}
			return
		}
		if !h.admitPower(powerSignalStandby, originSyslog) {
			return
		}
		h.logger.Info("box power signal: the speaker powered off STR's source, acting on it",
			"source", "syslog", "via", string(ev.Source), "class", string(ev.Class))
		go h.enterStandby()
	}
}

// releaseHeldStandby runs when the bus stayed silent for the whole hold: the
// ring's report goes through the door as if the bus were down. Runs on the
// timer's goroutine, so the action needs no further hand-off. The gate is
// still consulted: a bus delivery that raced the timer makes this the
// duplicate.
func (h *presetWsHandler) releaseHeldStandby(ev boxlog.PowerEvent) {
	h.powerGate.releaseStandby()
	if !h.admitPower(powerSignalStandby, originSyslog) {
		return
	}
	h.logger.Info("box power signal: the speaker powered off STR's source and the bus did not report it, acting on the ring's report",
		"source", "syslog", "via", string(ev.Source), "class", string(ev.Class))
	h.enterStandby()
}
