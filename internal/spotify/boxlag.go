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
// guess. This file measures it; no offset is applied to the Spotify app's
// position. Back reads it (see the consumer note below).
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
// Event-driven only: once per attach, once more when that attachment has
// settled (boxLagSettleDelay later, one timer per attachment), and once per
// track boundary. No ticker, no standing poll - this runs on every speaker in
// the field and a periodic probe would cost NAND and CPU for a number that only
// changes when something happens.
//
// One consumer acts on it: the Back key (HeardPrev, remotekeys.go) subtracts
// the lag from the engine position to tell what the room has heard. It takes
// only readings that ARE a lag (lagUsable): the attach reading is a zero check
// with both sides at zero by construction, and a reading at a skip's boundary
// is taken mid-jump.

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

// boxLagReasonAfterSkip names the reading at a track boundary a user skip
// caused: the box is dropping its buffer there, so both clocks are mid-jump.
const boxLagReasonAfterSkip = "track-boundary after a skip"

// boxLagReasonSettled names the one reading per attachment taken once the box
// has been playing it for boxLagSettleDelay.
const boxLagReasonSettled = "settled"

// boxLagSettleDelay is how long after an attachment's first audio its settled
// reading is taken: past the box's own start-up buffering and the lead prefill,
// well before the next natural track boundary.
const boxLagSettleDelay = 15 * time.Second

// scheduleSettledBoxLag arms the one settled reading of the attachment whose
// baseline was just taken. It stands down when the box has detached or
// re-attached by then: a newer attachment schedules its own.
func (m *Manager) scheduleSettledBoxLag() {
	m.mu.Lock()
	gen := m.lagAttachGen
	delay := m.lagSettleDelay
	m.mu.Unlock()
	if delay <= 0 {
		delay = boxLagSettleDelay
	}
	time.AfterFunc(delay, func() {
		m.mu.Lock()
		same := m.lagAttachGen == gen && m.sink != nil
		m.mu.Unlock()
		if same {
			m.triggerBoxLag(boxLagReasonSettled)
		}
	})
}

// lagUsable reports whether a stored reading is an actual buffer lag: taken
// with audio delivered and played on both sides, not the attach zero check
// and not mid-jump at a skip's boundary.
func lagUsable(l boxLagMeasurement) bool {
	return !l.At.IsZero() && l.Reason != boxLagReasonAttach && l.Reason != boxLagReasonAfterSkip &&
		l.DeliveredSec > 0 && l.BoxRelSec > 0 && l.DiffSec >= 0
}

// currentAttachLag is the buffer lag of the attachment the box is on right
// now, best source first: a usable box reading taken during this attachment
// ("box"), else the delivered audio since its first page less the wall clock
// since then ("wall-clock"; it misses the box's own start-up delay, so it
// errs a second or two short). ok is false with no box attached or no
// baseline yet.
func (m *Manager) currentAttachLag(now time.Time) (lag time.Duration, source string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sink == nil || m.lagRebase || m.lagBaseAt.IsZero() {
		return 0, "", false
	}
	if l := m.lastLag; lagUsable(l) && m.lastLagSeq > m.lagBaseSeq {
		return time.Duration(l.DiffSec * float64(time.Second)), "box", true
	}
	delivered := time.Duration(float64(m.deliveredGran-m.lagBaseGran) / vorbisRate * float64(time.Second))
	return max(0, delivered-now.Sub(m.lagBaseAt)), "wall-clock", true
}

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
	// The raw inputs, not just the difference: the number is only readable
	// against them (a box whose RelTime did not restart on a re-attach produces
	// a nonsense difference, and the two sides are what show that).
	// The number is the point of the exercise, but a line per track boundary
	// on every box in the field is a NAND append every three minutes. The
	// first reading of a session is logged at INFO, and so is the first one
	// that is an actual lag (lagUsable): the first is nearly always the attach
	// zero check, which says nothing about the lag, and a bundle with only
	// that line showed no lag at all (#1797). The rest go to Debug; the debug
	// section keeps the newest reading either way. Decided under mu: two
	// readings can finish at the same moment.
	usable := lagUsable(lag)
	info := !m.loggedBoxLag || (usable && !m.loggedUsableBoxLag)
	m.loggedBoxLag = true
	m.loggedUsableBoxLag = m.loggedUsableBoxLag || usable
	m.mu.Unlock()
	if !info {
		m.logger.Debug("spotify: buffer lag measured (audio delivered vs audio played)",
			"at", reason, "deliveredSec", lag.DeliveredSec, "boxRelSec", lag.BoxRelSec, "diffSec", lag.DiffSec)
		return
	}
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
