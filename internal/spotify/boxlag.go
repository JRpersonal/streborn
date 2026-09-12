// boxlag.go: how far the audio the speaker is playing lags behind the audio
// STR has delivered.
//
// Why this exists. With STR's Spotify entry the lyrics in the Spotify app run
// ahead of the sound; with the speaker's own Spotify entry they line up (field
// report, 2026-09). The mechanism is understood: go-librespot reports its
// position from the bytes it has written to the pipe, while that audio is still
// on its way through STR's batch and the box's own buffer, and STR deliberately
// keeps roughly ten seconds there (see leadCapSec in engine.go and the chunk
// window in oggchain.go). The Spotify app therefore shows a position that is
// ahead of what the room hears, and the lyrics follow that position.
//
// What is NOT known is the size of the offset on real hardware, and nothing has
// ever recorded it. Any correction applied before that number exists would be a
// guess. This file measures it and does nothing else: no offset is applied, no
// behaviour changes.
//
// The two clocks:
//
//   - delivered: the drain publishes the continuous audio timeline of the last
//     page it accepted for the box (deliveredGran). That is the position
//     go-librespot has produced, i.e. the one the Spotify app shows.
//   - the box: UPnP GetPositionInfo's RelTime, the speaker's own account of how
//     much of this stream it has rendered.
//
// Both are measured from the moment the box received the first audio of this
// attachment (lagBaseGran is the delivered timeline at that page), so the
// difference is the audio in flight. The baseline is taken in the drain and not
// where the box attaches, because the two must share one instant: the engine
// keeps producing while no box is attached (engineHot, see recall.go), and
// basing on the counter's value at the previous detach folded that whole
// detached window into every later reading. Whether the box's RelTime really
// restarts on a re-attach is exactly what the attach measurement below is for:
// it is taken when both sides should read zero, so a bundle shows whether the
// boundary numbers can be trusted.
//
// What the delivered side deliberately does NOT count: audio the drain produced
// with no box attached, and audio a skip cut threw away. The box never received
// either, so its RelTime cannot contain it, and counting it would inflate every
// later difference by that amount (see drain.go).
//
// Event-driven only: once per attach and once per track boundary. No ticker, no
// standing poll - this runs on every speaker in the field and a periodic probe
// would cost NAND and CPU for a number that only changes when something happens.

package spotify

import (
	"context"
	"math"
	"time"
)

// boxLagMeasurement is one comparison of the two clocks. Seconds, rounded to
// two decimals: the interesting quantity is on the order of ten seconds and the
// SOAP round trip alone costs more precision than that.
type boxLagMeasurement struct {
	At           time.Time `json:"at"`
	Reason       string    `json:"reason"`
	DeliveredSec float64   `json:"deliveredSec"`
	BoxRelSec    float64   `json:"boxRelSec"`
	DiffSec      float64   `json:"diffSec"`
}

// boxLagReadTimeout bounds the one SOAP query a measurement makes. Short: a box
// that does not answer promptly is not worth waiting for, the measurement is
// diagnostic and the next track boundary comes around anyway.
const boxLagReadTimeout = 4 * time.Second

// boxLagReasonAttach names the one reading whose box clock may legitimately be
// zero: the box has just started this stream. Everywhere else a zero RelTime is
// a box with no usable clock, not a box at the start of a track.
const boxLagReasonAttach = "attach"

// SetBoxPositionFn wires in the box's own playback clock (UPnP
// RelPosition's RelTime). The reader must report ok=false when the box gave no
// USABLE clock, not merely when the query failed: see upnp.RelPosition. Without
// it no measurement is taken at all, which is what keeps the agent's own tests
// and any non-UPnP consumer free of it.
func (m *Manager) SetBoxPositionFn(fn func(context.Context) (time.Duration, bool)) {
	m.mu.Lock()
	m.boxPositionFn = fn
	m.mu.Unlock()
}

// noteStreamAttached ARMS the per-attachment baseline of the delivered
// timeline. Called from ServeOgg once the sink is registered; the drain takes
// the actual baseline on the first audio page that reaches this box, which is
// the moment its own clock starts. See the file header for why the counter's
// value at this instant is the wrong base.
func (m *Manager) noteStreamAttached() {
	m.mu.Lock()
	m.lagRebase = true
	m.mu.Unlock()
}

// triggerBoxLag is what the live paths call: it samples the delivered side HERE,
// at the event, and does the box's SOAP round trip in the background so the
// drain never waits on it.
//
// The sample cannot move into the goroutine. The drain runs on: the next page
// advances the delivered timeline and an attach re-baselines it outright, so a
// goroutine that sampled whenever it happened to be scheduled compared two
// different instants and logged the result as a measurement. A boundary reading
// that raced the following attach came out negative that way; the drain test
// pins the order.
func (m *Manager) triggerBoxLag(reason string) {
	fn, deliveredGran, seq, ok := m.boxLagSample(reason)
	if !ok {
		return
	}
	go m.readBoxLag(fn, reason, deliveredGran, seq)
}

// measureBoxLag is triggerBoxLag start to finish in the caller's goroutine, for
// tests that want the reading before they assert on it.
func (m *Manager) measureBoxLag(reason string) {
	if fn, deliveredGran, seq, ok := m.boxLagSample(reason); ok {
		m.readBoxLag(fn, reason, deliveredGran, seq)
	}
}

// boxLagSample takes the delivered side of one measurement and reports whether
// there is anything to compare it against. ok=false means no reading is taken at
// all: no position reader wired, nothing attached, or no baseline for this
// attachment yet.
func (m *Manager) boxLagSample(reason string) (fn func(context.Context) (time.Duration, bool), deliveredGran int64, seq uint64, ok bool) {
	m.mu.Lock()
	// Ordering key for the store, handed out under the same lock that samples
	// the delivered side, so it numbers the events in the order they happened.
	m.lagSeq++
	seq = m.lagSeq
	fn = m.boxPositionFn
	attached := m.sink != nil
	rebasePending := m.lagRebase
	deliveredGran = m.deliveredGran - m.lagBaseGran
	// A delivered timeline that has fallen BELOW its own baseline was rebuilt
	// under the attached box: go-librespot restarted (the sink survives an
	// engine run, so nothing re-attaches), or a skip cut rolled unsent audio
	// back. The baseline belongs to a timeline that no longer exists, so retake
	// it here and report nothing rather than log a negative lag.
	rebuilt := deliveredGran < 0
	if rebuilt {
		m.lagBaseGran = m.deliveredGran
	}
	m.mu.Unlock()
	switch {
	case fn == nil || !attached:
		return nil, 0, 0, false
	case rebasePending:
		m.logger.Debug("spotify: no audio delivered since the box attached, no buffer-lag measurement", "at", reason)
		return nil, 0, 0, false
	case rebuilt:
		m.logger.Debug("spotify: delivered timeline was rebuilt under the attached box, re-baselined instead of measuring", "at", reason)
		return nil, 0, 0, false
	}
	return fn, deliveredGran, seq, true
}

// readBoxLag reads the speaker's own clock and logs it against the delivered
// side sampled at the trigger. reason names the event ("attach" /
// "track-boundary") so a bundle can tell the baseline reading from the
// steady-state ones.
//
// Best effort: a box that gives no usable clock means no line and no stored
// measurement. A made-up number here would be read as a measured one, which is
// the one wrong answer this could give.
func (m *Manager) readBoxLag(fn func(context.Context) (time.Duration, bool), reason string, deliveredGran int64, seq uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), boxLagReadTimeout)
	defer cancel()
	rel, ok := fn(ctx)
	// Not just "did the call succeed". A box that answers with no usable RelTime
	// reports zero, and a zero anywhere but at an attach says the box has played
	// none of the audio it was handed, i.e. the whole delivered timeline sits in
	// the buffer - the largest lag this can express, growing without bound over
	// a track and indistinguishable in a bundle from a real reading.
	if !ok || (rel <= 0 && reason != boxLagReasonAttach) {
		m.logger.Debug("spotify: no usable box position, no buffer-lag measurement",
			"at", reason, "readable", ok, "relSec", rel.Seconds())
		return
	}
	delivered := round2(float64(deliveredGran) / float64(vorbisRate))
	boxSec := round2(rel.Seconds())
	lag := boxLagMeasurement{
		At: time.Now(), Reason: reason,
		DeliveredSec: delivered, BoxRelSec: boxSec, DiffSec: round2(delivered - boxSec),
	}
	m.mu.Lock()
	// Keep the reading from the NEWEST event. The box read is a SOAP round trip
	// in its own goroutine, so two measurements in flight finish in whatever
	// order the scheduler picks, and the one that finishes last used to win the
	// store whatever it measured.
	//
	// The page that carries the first audio of an attachment can also be a
	// track boundary, so the boundary and the attach reading are dispatched
	// microseconds apart. A wall-clock stamp cannot separate them: on Windows
	// the two timestamps come out equal and the tie let the staler reading
	// through. The sequence number is taken under the same lock as the sample,
	// so it orders the EVENTS rather than the goroutines.
	if seq > m.lastLagSeq {
		m.lastLag = lag
		m.lastLagSeq = seq
	}
	m.mu.Unlock()
	// The raw inputs, not just the difference: the number is only readable
	// against them (a box whose RelTime did not restart on a re-attach produces
	// a nonsense difference, and the two sides are what show that).
	// The number is the point of the exercise, but a line per track boundary
	// on every box in the field is a NAND append every three minutes for a
	// measurement nothing acts on yet. The first one of a session is logged
	// at INFO so a bundle always carries one, the rest at Debug; the debug
	// section keeps the newest reading either way.
	if m.loggedBoxLag {
		m.logger.Debug("spotify: buffer lag measured (audio delivered vs audio played)",
			"at", reason, "deliveredSec", lag.DeliveredSec, "boxRelSec", lag.BoxRelSec, "diffSec", lag.DiffSec)
		return
	}
	m.loggedBoxLag = true
	m.logger.Info("spotify: buffer lag measured (audio delivered vs audio played)",
		"at", reason, "deliveredSec", lag.DeliveredSec, "boxRelSec", lag.BoxRelSec, "diffSec", lag.DiffSec)
}

// LastBoxLag returns the most recent measurement; the zero value when none has
// been taken.
func (m *Manager) LastBoxLag() boxLagMeasurement {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastLag
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
