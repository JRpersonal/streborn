package webui

import (
	"sync"
	"time"
)

// quietWakeState is the group-wake bookkeeping of this speaker: what is known
// about the level a quiet wake muted from and how long that hold lasts, the
// wakes that are muting the speaker right now, the settling window of the
// group operation the wake was made for, and when the firmware last reported a
// zone change. One mutex guards all of it; the methods below are the only way
// to it, and the arm/release decisions that read and change two of the fields
// together stay one critical section.
type quietWakeState struct {
	mu sync.Mutex
	// waking counts the wakes in progress that mute this speaker for STR's
	// own purposes: a quiet wake for a group operation, from before its mute
	// to the end of the wake, and a silent wake for a play, until its level
	// comes back. While one runs, or while a quiet wake holds the level down
	// (until), the speaker's volume frames are STR's own mute, the firmware's
	// power-on level or a level set for the group operation, and none of them
	// is taken for the level the speaker played at before (NoteBoxVolumeLevel).
	// Guarded by mu.
	waking int
	// in and until are the quiet wake: a member wake done for a group join,
	// what is known about the level the speaker played at before it was muted
	// for the wake, and how long the hold lasts. The level comes back once the
	// speaker joins a zone or the window runs out. Guarded by mu.
	in    wakeLevelInputs
	until time.Time
	// powerOn is the first non-zero level the speaker reported while a wake
	// above was running: the firmware's own power-on level, before STR's mute
	// took it down. The restore falls back to it when nothing better is
	// known (wakelevel.go). Guarded by mu.
	powerOn int
	// episodeUntil is how long this speaker counts as "STR woke me for
	// a group operation", independently of the mute. until answers a
	// different question (is the level still held down) and the zone join
	// CLEARS it, which is why the preset reconcile could not use it: the join
	// lands a second before the reconcile looks (#900). This one is a plain
	// deadline that nothing clears early. Guarded by mu.
	episodeUntil time.Time
	// zoneChangedAt is when the speaker last joined, led or left a zone, as the
	// firmware itself reported it. It exists so the re-push watchdog can tell a
	// stream that DROPPED from one that ended because the group changed under it
	// (#900). Deliberately not derived from until: the zone join is what
	// CLEARS that, so reading it here is a race that the push wins or loses by a
	// quarter of a second. Guarded by mu, which already covers the other
	// piece of group-wake state and is taken on the same events.
	zoneChangedAt time.Time
}

// noteEpisode stamps the settling window of a group operation.
func (q *quietWakeState) noteEpisode(window time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.episodeUntil = time.Now().Add(window)
}

// episodeActive reports whether the settling window is still open.
func (q *quietWakeState) episodeActive() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return time.Now().Before(q.episodeUntil)
}

// episode reports whether the settling window is still open and, if so, when
// the wake that opened it happened (the window is `window` long).
func (q *quietWakeState) episode(window time.Duration) (active bool, since time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !time.Now().Before(q.episodeUntil) {
		return false, time.Time{}
	}
	return true, q.episodeUntil.Add(-window)
}

// active reports whether a quiet wake still holds the level down.
func (q *quietWakeState) active() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return !q.until.IsZero()
}

// holdingLocked reports whether STR holds this speaker's level: a wake that
// mutes it is running, or a quiet wake still holds it down. Caller holds mu.
func (q *quietWakeState) holdingLocked() bool {
	return q.waking > 0 || !q.until.IsZero()
}

// begin marks a muting wake as running. A hold that starts from nothing
// forgets the power-on level the previous one saw.
func (q *quietWakeState) begin() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.holdingLocked() {
		q.powerOn = 0
	}
	q.waking++
}

// end marks a muting wake begun with begin as finished.
func (q *quietWakeState) end() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.waking > 0 {
		q.waking--
	}
}

// noteLevel sees every volume frame. While STR holds the level it keeps the
// first non-zero level a running wake reports (the firmware's power-on level)
// and reports held, so the caller does not take the frame for the level the
// speaker plays at.
func (q *quietWakeState) noteLevel(level int) (held bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.holdingLocked() {
		return false
	}
	if q.waking > 0 && level > 0 && q.powerOn == 0 {
		q.powerOn = level
	}
	return true
}

// heldPowerOn returns the power-on level noteLevel kept, 0 when none.
func (q *quietWakeState) heldPowerOn() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.powerOn
}

// arm remembers what is known about the level a quiet wake muted from and
// sets the deadline of the bounded fallback restore. What the first arming
// knew wins, and a later one only fills in what the first did not know: the
// app races each wake against a 4 s deadline and retries, while a real wake
// out of standby needs longer than that, so a second quiet wake inside the
// window is the normal case. A repeat arming only pushes the deadline out.
//
// The rule used to be the whole defence against a second wake snapshotting
// the 0 the first one wrote, and it only held while the first hold was still
// armed (#1774 follow-up: a pair formed, put back to standby, formed again
// restored 0 forever). The inputs now come from the level the speaker
// reported while awake and never from a 0 (wakelevel.go), so a repeat arming
// cannot capture STR's own mute whatever the timing.
func (q *quietWakeState) arm(in wakeLevelInputs, window time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.until.IsZero() {
		q.in = in
	} else {
		if q.in.remembered <= 0 {
			q.in.remembered = in.remembered
		}
		if q.in.standby <= 0 {
			q.in.standby = in.standby
		}
	}
	q.until = time.Now().Add(window)
}

// armed returns what the hold knows about the level to put back, and whether
// a quiet wake holds the level at all.
func (q *quietWakeState) armed() (in wakeLevelInputs, powerOn int, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.in, q.powerOn, !q.until.IsZero()
}

// release ends the hold and reports what is known about the level to put
// back; restore is false when nothing was armed. A timeout release leaves the
// arming alone when the deadline is still ahead: a repeat arming pushed the
// deadline out and started its own timer, so the timer of the earlier wake
// must not un-mute a speaker that a later quiet wake is still holding down;
// the later timer does the work. A zone join is not a deadline and always
// restores.
func (q *quietWakeState) release(timeout bool) (in wakeLevelInputs, powerOn int, restore bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	armed := !q.until.IsZero()
	if armed && timeout && time.Now().Before(q.until) {
		return q.in, q.powerOn, false
	}
	q.until = time.Time{}
	return q.in, q.powerOn, armed
}

// noteZoneChanged stamps a zone membership change.
func (q *quietWakeState) noteZoneChanged() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.zoneChangedAt = time.Now()
}

// lastZoneChange returns when the firmware last reported a zone change, zero
// when never.
func (q *quietWakeState) lastZoneChange() time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.zoneChangedAt
}
