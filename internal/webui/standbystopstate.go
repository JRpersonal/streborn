package webui

import (
	"sync"
	"time"
)

// standbyStopState is the power-off bookkeeping of this speaker: when STR
// last saw its UPnP source drop to STANDBY, the rate limits of the
// standby-bounce transport clear and its log line, when the user last asked
// for playback, and the single-flight stamp of the spontaneous-off recovery.
// One mutex guards all of it; the methods below are the only way to it, and
// every check-then-stamp stays one critical section.
type standbyStopState struct {
	mu sync.Mutex
	// lastStop debounces the #197 standby-bounce mitigation: some ST20
	// (scm) firmware oscillates UPNP->STANDBY->UPNP on a power-off, re-selecting
	// STR's UPnP source so the box turns itself back on. HandleEnterStandby clears
	// the transport once per standbyStopDebounce so the rapid flip does not issue a
	// burst of Stops.
	lastStop time.Time
	// lastClear rate-limits the transport clear during a power-off bounce
	// (separate from lastStop, which gates the resume-suppression window):
	// the clear re-fires on each flip of the UPNP<->STANDBY oscillation so a clear
	// that lost the ~170 ms race is retried, bounded by standbyClearMinGap.
	lastClear time.Time
	// lastZoneLog rate-limits the "zoned box powered off, leaving its
	// transport alone" line to one per power-off episode (the firmware can flap
	// UPNP<->STANDBY several times within a second).
	lastZoneLog time.Time
	// lastUserPlayStart is when a user last explicitly asked for playback (a
	// hardware preset press or an app play). HandleEnterStandby reads it to tell
	// a UPNP->STANDBY flip that interrupts the user's own fresh recall (firmware
	// settling, #419) from a real power-off, and NoteUserPlay stamps it while
	// clearing the stop latches (the press is newer intent than any prior stop).
	// Guarded by mu.
	lastUserPlayStart time.Time
	// lastSpontResume single-flights the spontaneous-power-off recovery so a
	// flapping source cannot stack resume goroutines. Guarded by mu.
	lastSpontResume time.Time
}

// lastStandbyStop returns when STR last saw the UPnP source drop to STANDBY,
// zero when never (or cleared by a user play).
func (st *standbyStopState) lastStandbyStop() time.Time {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.lastStop
}

// stoppedAfter reports whether a power-off was seen strictly after t.
func (st *standbyStopState) stoppedAfter(t time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return !st.lastStop.IsZero() && st.lastStop.After(t)
}

// stoppedWithin reports whether a power-off was seen less than window ago.
func (st *standbyStopState) stoppedWithin(window time.Duration) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return !st.lastStop.IsZero() && time.Since(st.lastStop) < window
}

// noteStop stamps a power-off.
func (st *standbyStopState) noteStop() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.lastStop = time.Now()
}

// noteUserPlay stamps a user play and clears the power-off stamp in the same
// section: the press is newer intent than any prior stop.
func (st *standbyStopState) noteUserPlay() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.lastUserPlayStart = time.Now()
	st.lastStop = time.Time{}
}

// userPlayStart returns when a user last explicitly asked for playback, zero
// when never.
func (st *standbyStopState) userPlayStart() time.Time {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.lastUserPlayStart
}

// userPlayedWithin reports whether a user play is less than window old.
func (st *standbyStopState) userPlayedWithin(window time.Duration) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return !st.lastUserPlayStart.IsZero() && time.Since(st.lastUserPlayStart) < window
}

// userPlayedAfter reports whether a user play was stamped strictly after t.
func (st *standbyStopState) userPlayedAfter(t time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.lastUserPlayStart.After(t)
}

// claimZoneLog reports whether the zoned power-off line may be logged now
// (none in the last gap) and stamps it when so.
func (st *standbyStopState) claimZoneLog(now time.Time, gap time.Duration) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	logIt := st.lastZoneLog.IsZero() || now.Sub(st.lastZoneLog) >= gap
	if logIt {
		st.lastZoneLog = now
	}
	return logIt
}

// claimClear reports whether the transport clear may fire now (none in the
// last gap) and stamps it when so.
func (st *standbyStopState) claimClear(gap time.Duration) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	doClear := st.lastClear.IsZero() || time.Since(st.lastClear) >= gap
	if doClear {
		st.lastClear = time.Now()
	}
	return doClear
}

// claimSpontResume single-flights the spontaneous-off recovery: it reports
// tooSoon when one ran less than gap ago and otherwise stamps this one.
func (st *standbyStopState) claimSpontResume(gap time.Duration) (tooSoon bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	tooSoon = !st.lastSpontResume.IsZero() && time.Since(st.lastSpontResume) < gap
	if !tooSoon {
		st.lastSpontResume = time.Now()
	}
	return tooSoon
}
