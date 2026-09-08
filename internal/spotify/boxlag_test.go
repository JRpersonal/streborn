package spotify

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// lagTestManager is a manager with a box attached and a known delivered
// timeline: 30 s of audio handed out since the box attached.
func lagTestManager(buf *bytes.Buffer, pos func(context.Context) (time.Duration, bool)) *Manager {
	m := &Manager{logger: slog.New(slog.NewTextHandler(buf, nil))}
	m.sink = io.Discard
	m.lagBaseGran = 100 * vorbisRate
	m.deliveredGran = 130 * vorbisRate
	m.boxPositionFn = pos
	return m
}

// The number the field report needs: STR has delivered 30 s since the box
// attached, the box says it has played 20 s of them, so 10 s are still in the
// buffer and the Spotify app's position (and its lyrics) runs that far ahead.
func TestMeasureBoxLagComputesTheDifference(t *testing.T) {
	var buf bytes.Buffer
	m := lagTestManager(&buf, func(context.Context) (time.Duration, bool) {
		return 20 * time.Second, true
	})
	m.measureBoxLag("track-boundary")

	got := m.LastBoxLag()
	if got.DeliveredSec != 30 || got.BoxRelSec != 20 || got.DiffSec != 10 {
		t.Fatalf("measurement = %+v, want delivered 30, box 20, diff 10", got)
	}
	if got.Reason != "track-boundary" || got.At.IsZero() {
		t.Fatalf("the measurement must carry its trigger and time: %+v", got)
	}
	line := buf.String()
	if !strings.Contains(line, "diffSec=10") || !strings.Contains(line, "deliveredSec=30") ||
		!strings.Contains(line, "boxRelSec=20") {
		t.Fatalf("the INFO line must carry both sides and the difference:\n%s", line)
	}
}

// A box that will not answer GetPositionInfo must leave no measurement behind.
// A stored zero would read as "no lag at all", which is the one conclusion this
// must never invite.
func TestMeasureBoxLagSkipsAnUnreadablePosition(t *testing.T) {
	var buf bytes.Buffer
	m := lagTestManager(&buf, func(context.Context) (time.Duration, bool) {
		return 0, false
	})
	m.measureBoxLag("attach")

	if got := m.LastBoxLag(); got != (boxLagMeasurement{}) {
		t.Fatalf("an unreadable position stored a measurement: %+v", got)
	}
	if strings.Contains(buf.String(), "buffer lag measured") {
		t.Fatalf("an unreadable position must not be logged as a measurement:\n%s", buf.String())
	}
}

// The other half of the same trap, and the one a box actually walks into: it
// ANSWERS, but with no usable RelTime, which parses as zero. Mid-track that zero
// is not a position, it is the claim that the box has played none of the 30 s it
// was handed - the largest lag this can express, growing for as long as the
// track lasts, and indistinguishable in a bundle from a real reading.
func TestMeasureBoxLagSkipsAZeroPositionMidTrack(t *testing.T) {
	var buf bytes.Buffer
	m := lagTestManager(&buf, func(context.Context) (time.Duration, bool) {
		return 0, true
	})
	m.measureBoxLag("track-boundary")

	if got := m.LastBoxLag(); got != (boxLagMeasurement{}) {
		t.Fatalf("a zero box clock stored a full-track lag: %+v", got)
	}
	if strings.Contains(buf.String(), "buffer lag measured") {
		t.Fatalf("a zero box clock must not be logged as a measurement:\n%s", buf.String())
	}
}

// A delivered timeline below its own baseline means the timeline was rebuilt
// under the attached box (a go-librespot restart with the sink still open). The
// difference would be negative, which is not a lag: re-baseline and report
// nothing.
func TestMeasureBoxLagRebaselinesARebuiltTimeline(t *testing.T) {
	var buf bytes.Buffer
	m := lagTestManager(&buf, func(context.Context) (time.Duration, bool) {
		return 60 * time.Second, true
	})
	m.deliveredGran = 10 * vorbisRate // a fresh run's timeline, below the base
	m.measureBoxLag("track-boundary")

	if got := m.LastBoxLag(); got != (boxLagMeasurement{}) {
		t.Fatalf("a rebuilt timeline stored a negative measurement: %+v", got)
	}
	if m.lagBaseGran != 10*vorbisRate {
		t.Fatalf("the baseline must follow the rebuilt timeline, got %d", m.lagBaseGran)
	}
}

// Nothing attached means there is no playback to measure: no SOAP query goes
// out (the box is not disturbed) and nothing is recorded.
func TestMeasureBoxLagDoesNothingWithNoBoxAttached(t *testing.T) {
	var buf bytes.Buffer
	asked := 0
	m := lagTestManager(&buf, func(context.Context) (time.Duration, bool) {
		asked++
		return 5 * time.Second, true
	})
	m.sink = nil

	m.measureBoxLag("track-boundary")

	if asked != 0 {
		t.Fatalf("the box was queried %d times with nothing attached", asked)
	}
	if got := m.LastBoxLag(); got != (boxLagMeasurement{}) {
		t.Fatalf("a measurement was recorded with nothing attached: %+v", got)
	}
}

// With no position reader wired (the agent that never got one, and every test
// that does not want the traffic) the measurement is a no-op.
func TestMeasureBoxLagDoesNothingWithoutAPositionReader(t *testing.T) {
	var buf bytes.Buffer
	m := lagTestManager(&buf, nil)
	m.measureBoxLag("attach")
	if got := m.LastBoxLag(); got != (boxLagMeasurement{}) {
		t.Fatalf("a measurement appeared without a position reader: %+v", got)
	}
}

// An attach only ARMS the baseline; the drain takes it on the first audio page
// the box actually receives (see boxlag_drain_test.go). Until then there is no
// shared base, so a reading arriving in between would be measured against the
// previous attachment's timeline: it must be skipped, not reported.
func TestMeasureBoxLagWaitsForTheBaselineAfterAnAttach(t *testing.T) {
	var buf bytes.Buffer
	asked := 0
	m := lagTestManager(&buf, func(context.Context) (time.Duration, bool) {
		asked++
		return 20 * time.Second, true
	})
	m.noteStreamAttached()
	m.measureBoxLag("attach")

	if got := m.LastBoxLag(); got != (boxLagMeasurement{}) {
		t.Fatalf("a measurement was taken before the attachment had a baseline: %+v", got)
	}
	if asked != 0 {
		t.Fatalf("the box was queried %d times with no baseline to compare against", asked)
	}
}

// Units. Granules are samples at the stream's own rate and RelTime is a clock
// in seconds, so the divisor is the one place the two sides can silently drift
// apart: at 48000 (the other rate an Ogg stream can carry) the delivered side
// would read 9 % short and nothing else in the code would notice. A fractional
// second is used deliberately - a whole-second fixture passes under a divisor
// that is merely off by a factor the fixture happens to absorb.
func TestMeasureBoxLagConvertsGranulesAtTheStreamSampleRate(t *testing.T) {
	var buf bytes.Buffer
	m := lagTestManager(&buf, func(context.Context) (time.Duration, bool) {
		return 1500 * time.Millisecond, true
	})
	m.lagBaseGran = 0
	m.deliveredGran = 11*vorbisRate + vorbisRate/2 // 11.5 s of audio handed out

	m.measureBoxLag("track-boundary")

	got := m.LastBoxLag()
	if got.DeliveredSec != 11.5 || got.BoxRelSec != 1.5 || got.DiffSec != 10 {
		t.Fatalf("measurement = %+v, want delivered 11.5, box 1.5, diff 10", got)
	}
}

// The box query is bounded. The drain fires this per track boundary and never
// waits on it, so a speaker that accepts the SOAP connection and then says
// nothing must not hold the goroutine: on a box whose :8091 has wedged that is
// one stuck goroutine per track, for the rest of the session.
func TestMeasureBoxLagBoundsTheBoxQuery(t *testing.T) {
	var buf bytes.Buffer
	var deadline time.Time
	var haveDeadline bool
	m := lagTestManager(&buf, func(ctx context.Context) (time.Duration, bool) {
		deadline, haveDeadline = ctx.Deadline()
		return 20 * time.Second, true
	})
	m.measureBoxLag("track-boundary")

	if !haveDeadline {
		t.Fatal("the box query ran with no deadline; a silent speaker would hold it forever")
	}
	if left := time.Until(deadline); left > boxLagReadTimeout {
		t.Fatalf("the query deadline is %v away, longer than boxLagReadTimeout %v", left, boxLagReadTimeout)
	}
}

// The measurement rides in the Spotify debug section, so a diagnostic bundle
// carries the number even after the NAND log ring has rolled.
func TestChainSnapshotCarriesTheLastLagMeasurement(t *testing.T) {
	var buf bytes.Buffer
	m := lagTestManager(&buf, func(context.Context) (time.Duration, bool) {
		return 20 * time.Second, true
	})
	m.measureBoxLag("track-boundary")

	snap := m.ChainSnapshot()
	if snap["lastLagDiffSec"] != 10.0 || snap["lastLagDeliveredSec"] != 30.0 ||
		snap["lastLagBoxRelSec"] != 20.0 || snap["lastLagReason"] != "track-boundary" {
		t.Fatalf("the debug section must carry the measurement, got %v %v %v %v",
			snap["lastLagDeliveredSec"], snap["lastLagBoxRelSec"], snap["lastLagDiffSec"], snap["lastLagReason"])
	}
}
