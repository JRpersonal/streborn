// telemetry.go: fetch/liveness telemetry — audio-gap stamps, per-slot and
// global fetch tracking, and the accessors the wedge detector and hardware
// recall verify consume.

package streamproxy

import (
	"time"
)

// markByteDelivery stamps the moment audio last reached the box. Called from the
// streamOne copy loop on every successful write; cheap (one guarded time.Now),
// never logged per call.
func (s *Server) markByteDelivery() {
	s.gapMu.Lock()
	s.lastByteToBox = time.Now()
	s.gapMu.Unlock()
}

// resetAudioGap clears the last-byte stamp at the start of a fresh stream so a
// stale gap from a previous station is not reported on the first reconnect.
func (s *Server) resetAudioGap() {
	s.gapMu.Lock()
	s.lastByteToBox = time.Time{}
	s.gapMu.Unlock()
}

// audioGap returns how long it has been since audio last reached the box, or 0
// if no byte has been delivered yet (so the very first connect attempt is not
// reported as a gap).
func (s *Server) audioGap() time.Duration {
	s.gapMu.Lock()
	defer s.gapMu.Unlock()
	if s.lastByteToBox.IsZero() {
		return 0
	}
	return time.Since(s.lastByteToBox)
}

// Register registers /stream/<slot> as well as /stream/raw for ad-hoc URLs
// (e.g. from the radio search) on the supplied mux.
// noteFetchOpen records that the box opened a proxied stream just now and
// marks the connection open; the returned func marks it closed and re-stamps
// the activity, so "served until the connection dropped a moment ago" reads
// as fresh. A bare open-time stamp was not enough: a radio stream holds ONE
// long GET for its whole playback, so after 9m42s of flawless audio the
// spontaneous-off recovery read lastFetch as ten minutes stale and stood
// down, leaving the speaker dead after a firmware source-drop (#491
// Cinemate, field bundle 2026-08-01).
func (s *Server) noteFetchOpen() func() {
	s.fetchMu.Lock()
	s.lastFetch = time.Now()
	s.openFetches++
	s.fetchMu.Unlock()
	return func() {
		s.fetchMu.Lock()
		s.lastFetch = time.Now()
		s.openFetches--
		s.fetchMu.Unlock()
	}
}

// noteSlotFetch records that the box opened THIS slot's proxied stream. Called
// only after the slot validated and the store had a playable preset, so a 404
// or a foreign fetch can never stamp it. Paired with noteSlotFetchDone.
func (s *Server) noteSlotFetch(slot int) {
	if slot < 1 || slot > 6 {
		return
	}
	s.fetchMu.Lock()
	s.slotFetch[slot] = time.Now()
	s.slotOpen[slot]++
	s.fetchMu.Unlock()
}

// noteSlotFetchDone records that a slot connection closed, for the liveness
// half of SlotPulledSince.
func (s *Server) noteSlotFetchDone(slot int) {
	if slot < 1 || slot > 6 {
		return
	}
	s.fetchMu.Lock()
	if s.slotOpen[slot] > 0 {
		s.slotOpen[slot]--
	}
	s.slotFetchEnd[slot] = time.Now()
	s.fetchMu.Unlock()
}

// LastFetchForSlot reports when the box last opened the given slot's proxied
// stream (zero time = never, or slot out of range). The global LastActivity
// stamp stays for the wedge detector, which deliberately counts any fetch.
// No production caller since the recall verify moved to the liveness-aware
// SlotPulledSince (a bare open-stamp certified failed recalls); kept as the
// low-level accessor its tests and future diagnostics read the raw stamp
// through.
func (s *Server) LastFetchForSlot(slot int) time.Time {
	if slot < 1 || slot > 6 {
		return time.Time{}
	}
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()
	return s.slotFetch[slot]
}

// minSustainedFetch is how long a now-closed slot fetch must have served to
// still count as "the box played this recall". The box's re-login source
// bounce opens the stream for 36ms-2.4s and drops it; a genuine playback
// session either stays open or lasted well past this.
const minSustainedFetch = 3 * time.Second

// SlotPulledSince reports whether the box is credibly playing this slot's
// proxied stream for a recall anchored at t: a connection opened after t that
// is still OPEN, or one that served at least minSustainedFetch before closing.
// This is the hardware recall verify's success signal; "opened once since t"
// alone certified dead recalls as healthy (#252 field bundles).
func (s *Server) SlotPulledSince(slot int, t time.Time) bool {
	if slot < 1 || slot > 6 {
		return false
	}
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()
	start := s.slotFetch[slot]
	if start.IsZero() || !start.After(t) {
		return false
	}
	if s.slotOpen[slot] > 0 {
		return true
	}
	return s.slotFetchEnd[slot].Sub(start) >= minSustainedFetch
}

// SlotFetchLive reports whether the box currently has an OPEN connection to this
// slot's proxied stream. Unlike SlotPulledSince it makes no sustained-duration
// judgement on a closed fetch: it is the "is the box pulling audio right now"
// signal the recall verify uses to distinguish a genuine playback from a login
// error's brief source bounce (which opens the stream, serves ~3s and closes).
func (s *Server) SlotFetchLive(slot int) bool {
	if slot < 1 || slot > 6 {
		return false
	}
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()
	return s.slotOpen[slot] > 0
}

// SetBoxStateFn wires the speaker-side condition reporter for
// /api/stream-status (see boxStateFn).
func (s *Server) SetBoxStateFn(fn func() string) {
	s.boxStateFn = fn
}

// LastActivity reports when the box was last SERVED by any proxied stream
// (activity is "now" while a connection is open, the close moment after it
// ends, the open moment otherwise) and when the last terminal upstream
// failure happened (zero times = never). Consumed by the webui's wedge
// detector, the recall inert-check and the spontaneous-off recovery.
func (s *Server) LastActivity() (lastFetch, lastFailure time.Time) {
	s.fetchMu.Lock()
	lastFetch = s.lastFetch
	if s.openFetches > 0 {
		lastFetch = time.Now()
	}
	s.fetchMu.Unlock()
	s.errMu.Lock()
	lastFailure = s.lastErr.when
	s.errMu.Unlock()
	return lastFetch, lastFailure
}
