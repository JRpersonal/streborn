package main

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// After a boot the firmware reads its account from marge
// (GET /streaming/account/<id>/full) and drops its hardware-key registrations
// each time it does. The reconcile re-registers them when the speaker then
// lists no keys, but the firmware does not always read the account just once:
// on an ST10 the keys were re-registered at boot+15 s, a SECOND account read at
// +16 s wiped them again, and they stayed empty until the five-minute
// maintenance check healed them.
//
// So every account read inside the boot window (re)arms one key check a few
// seconds later. Debounced: a burst of reads (the firmware typically sends two
// within a second) ends in ONE check after the LAST read. The check itself is
// the reconcile's ordinary pass: one read of the speaker's key list, and
// writes only for the keys that list is missing. Nothing is written when the
// keys survived the read, and outside the boot window an account read changes
// nothing.

const (
	// accountResyncDelay is how long after the last account read the keys are
	// checked. Long enough for a second read of the same burst to land first,
	// short enough that a press right after the speaker comes up finds them.
	accountResyncDelay = 5 * time.Second
	// accountResyncBootWindow is how long after the agent starts an account
	// read still counts as part of the boot. The agent starts with the
	// speaker, and the firmware's account reads of a boot fall within its
	// first minutes.
	accountResyncBootWindow = 10 * time.Minute
)

// presetRecheckAsk wakes the reconcile for one ordinary (missing-only) pass
// ahead of its maintenance cadence. Unlike presetResyncAsk it never forces a
// full re-write: it only makes the next look at the speaker's key list happen
// now rather than in up to five minutes.
var presetRecheckAsk atomic.Bool

// accountResync debounces the account reads of a boot into one key check.
type accountResync struct {
	mu     sync.Mutex
	start  time.Time
	window time.Duration
	delay  time.Duration
	now    func() time.Time // a seam for the boot-window test
	timer  *time.Timer
	gen    int // which arming the pending timer belongs to
	reads  int
	fire   func(reads int)
}

// newAccountResync returns a debouncer whose boot window starts now. fire is
// called once per burst, with the number of account reads it covered.
func newAccountResync(window, delay time.Duration, fire func(reads int)) *accountResync {
	return &accountResync{
		start:  time.Now(),
		window: window,
		delay:  delay,
		now:    time.Now,
		fire:   fire,
	}
}

// noteAccountRead is the marge hook: one account read just went out. It
// returns at once; the check runs on its own timer.
func (r *accountResync) noteAccountRead() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.now().Sub(r.start) > r.window {
		return
	}
	r.reads++
	if r.timer != nil {
		r.timer.Stop()
	}
	r.gen++
	gen := r.gen
	r.timer = time.AfterFunc(r.delay, func() { r.expire(gen) })
}

// expire runs the check for the burst that just went quiet. A timer that was
// already firing when a newer read re-armed it belongs to an older arming and
// does nothing, so the check never runs before the LAST read's delay is up.
func (r *accountResync) expire(gen int) {
	r.mu.Lock()
	if gen != r.gen {
		r.mu.Unlock()
		return
	}
	reads := r.reads
	r.reads = 0
	r.timer = nil
	r.mu.Unlock()
	if reads == 0 {
		return
	}
	r.fire(reads)
}

// askPresetRecheckAfterAccountRead is the fire action wired in the agent:
// wake the reconcile for one ordinary pass.
func askPresetRecheckAfterAccountRead(logger *slog.Logger) func(reads int) {
	return func(reads int) {
		presetRecheckAsk.Store(true)
		if logger != nil {
			logger.Info("preset self-heal: the speaker read its account during boot, checking its keys once more",
				"accountReads", reads)
		}
	}
}
