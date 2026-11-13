package oled

// The look at the panel in a long standby (#841, 2026-11-13).
//
// The watch in darkwatch.go looks at the panel in the first minute after a
// power-off. A bundle from an ST20 on v1.6.1 showed that this is not enough:
// after the power-off at 22:04 the clock was drawn, and when the owner
// switched the speaker on and off again at 09:13 the panel was black with
// BoseApp's 0x01 fill. The clock had stopped some time in the night, and
// nothing looked at it until the next morning.
//
// So, while the speaker is in standby, the same watch runs again every
// longStandbyEvery. It needs no timer of its own: it rides on the firmware's
// once-a-minute Wi-Fi quality line, which the agent reads anyway
// (boxlog.SetMinuteHandler). A working clock costs two reads of the
// framebuffer in RAM per look and nothing else: no NAND, no request to the
// speaker, so deep standby is untouched. Only a stuck panel goes on to the
// watch's own steps (the standby nudge, then one restart).
//
// A watch that finds the clock stuck and cannot bring it back stops the
// looks until the next standby. The nudge sends the speaker a standby, and
// repeating that every half hour all night would keep it out of deep standby
// for nothing.

import (
	"sync"
	"time"
)

// longStandbyEvery is how often the panel is looked at in a long standby.
const longStandbyEvery = 30 * time.Minute

// longStandbyState decides when the next look is due.
type longStandbyState struct {
	mu sync.Mutex
	// inStandby is the agent's word on the speaker's state.
	inStandby bool
	// last is the last look, or when the standby began (zero: unknown, the
	// first minute line starts the count).
	last time.Time
	// blocked is set by a watch that found the clock stuck and could not
	// bring it back; the next standby clears it.
	blocked bool
}

var longStandby longStandbyState

// note records that the speaker went to standby (on) or left it. A standby
// the state already knows about (the bus frame and the syslog line of the
// same power-off) does not restart the count.
func (s *longStandbyState) note(on bool, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on && !s.inStandby {
		s.last = now
		s.blocked = false
	}
	s.inStandby = on
}

// block stops the looks until the next standby.
func (s *longStandbyState) block() {
	s.mu.Lock()
	s.blocked = true
	s.mu.Unlock()
}

// due reports whether a look should start now, and counts it when it does.
func (s *longStandbyState) due(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.inStandby || s.blocked {
		return false
	}
	if s.last.IsZero() {
		s.last = now
		return false
	}
	if now.Sub(s.last) < longStandbyEvery {
		return false
	}
	s.last = now
	return true
}

// NotePanelStandby is the agent telling the panel code that the speaker went
// to standby (true) or left it (false). Cheap; call it on every switch.
func NotePanelStandby(on bool) { longStandby.note(on, time.Now()) }

// StandbyMinute is the firmware's once-a-minute line. In a long standby it
// starts a look at the panel every longStandbyEvery. Returns at once.
func StandbyMinute(at time.Time) {
	if currentDarkHooks().ClockExpected == nil || panelHeldBySTR() || darkWatchLive.Load() != 0 {
		return
	}
	if !longStandby.due(at) {
		return
	}
	startDarkWatch("standby, long", true, longStandbyVerdict)
}

// longStandbyVerdict hears how a watch ended: a clock that stayed stuck
// stops the long looks until the next standby.
func longStandbyVerdict(v darkVerdict) {
	if v == darkLeft || v == darkStuck {
		longStandby.block()
	}
}
