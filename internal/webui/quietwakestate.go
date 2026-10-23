package webui

import (
	"sync"
	"time"
)

// quietWakeState is the group-wake bookkeeping of this speaker: the level a
// quiet wake muted from and how long that hold lasts, the settling window of
// the group operation the wake was made for, and when the firmware last
// reported a zone change. One mutex guards all of it; the methods below are
// the only way to it, and the arm/release decisions that read and change two
// of the fields together stay one critical section.
type quietWakeState struct {
	mu sync.Mutex
	// vol and until are the quiet wake: a member wake done for a group join,
	// the level the speaker had before it was muted for the wake, restored
	// once the speaker joins a zone or the window runs out. Guarded by mu.
	vol   int
	until time.Time
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

// arm remembers the level a quiet wake muted from and sets the deadline of
// the bounded fallback restore. The level already armed wins: a SECOND quiet
// wake inside the window would otherwise snapshot the level the first one
// already muted (0) and then "restore" the speaker to silence. That is the
// normal case, not an edge one: the app races each wake against a 4 s
// deadline and retries, while a real wake out of standby needs longer than
// that. So a repeat arming only pushes the deadline out.
func (q *quietWakeState) arm(vol int, window time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.until.IsZero() {
		q.vol = vol
	}
	q.until = time.Now().Add(window)
}

// release ends the hold and reports the level to put back; restore is false
// when nothing was armed. A timeout release leaves the arming alone when the
// deadline is still ahead: a repeat arming pushed the deadline out and
// started its own timer, so the timer of the earlier wake must not un-mute a
// speaker that a later quiet wake is still holding down; the later timer does
// the work. A zone join is not a deadline and always restores.
func (q *quietWakeState) release(timeout bool) (vol int, restore bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	armed := !q.until.IsZero()
	if armed && timeout && time.Now().Before(q.until) {
		return q.vol, false
	}
	q.until = time.Time{}
	return q.vol, armed
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
