// Tests for the half of the buffer-lag measurement that lives in the drain:
// what publishes the delivered timeline, and what triggers a reading. The
// measurement's own arithmetic is in boxlag_test.go.

package spotify

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// lagDrainHarness is a manager with a box attached, the real drain step, and a
// counting position reader standing in for the speaker's UPnP clock.
func lagDrainHarness(t *testing.T, reads *atomic.Int32) (*Manager, *oggDrain) {
	t.Helper()
	chain := newTestChain(t, 1<<40, nil)
	holdSeams(chain)
	m, d, _ := newDrainHarness(t, chain, 1<<20)
	m.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	m.boxPositionFn = func(context.Context) (time.Duration, bool) {
		reads.Add(1)
		return 10 * time.Second, true
	}
	return m, d
}

// waitForReads waits for the measurement goroutines the drain fires to land.
func waitForReads(reads *atomic.Int32, want int32, d time.Duration) int32 {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if reads.Load() >= want {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return reads.Load()
}

// waitForLag waits until a measurement with the given trigger has been stored.
// The read and the store happen in the background goroutine, so counting reads
// is not enough to assert on the stored number.
func waitForLag(m *Manager, reason string, d time.Duration) boxLagMeasurement {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if got := m.LastBoxLag(); got.Reason == reason {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	return m.LastBoxLag()
}

// The drain is what actually triggers a reading, and nothing pinned that
// wiring. The contract is one query per event and nothing in between: this runs
// on every speaker in the field, so an event that fired twice, or a page that
// queried at all, would be a standing poll by another name.
//
// Two tracks after an attach make exactly two queries: the first track's
// boundary has no baseline yet and is skipped without touching the box, the
// first audio page takes the baseline and reads once, the second track's
// boundary reads once.
func TestDrainMeasuresOncePerEventAndNeverPerPage(t *testing.T) {
	var reads atomic.Int32
	m, d := lagDrainHarness(t, &reads)
	m.noteStreamAttached()

	feed(d, synthVorbisTrack(1, 60, false))
	feed(d, synthVorbisTrack(2, 60, false))

	waitForReads(&reads, 2, 3*time.Second)
	// Give a third (wrong) query time to arrive before declaring the count.
	time.Sleep(150 * time.Millisecond)
	if got := reads.Load(); got != 2 {
		t.Fatalf("an attach and two track boundaries must produce exactly two box queries, got %d", got)
	}
	if lag := waitForLag(m, "track-boundary", time.Second); lag.Reason != "track-boundary" {
		t.Fatalf("the boundary reading must name its trigger, got %+v", lag)
	}
}

// The baseline belongs to the first audio the attachment RECEIVES, not to the
// counter's value where the box attached: the engine keeps producing while no
// box is attached (engineHot), so basing on that value folded the whole
// detached window into every reading of the new attachment.
func TestAttachBaselineIgnoresAudioFromBeforeTheAttach(t *testing.T) {
	var reads atomic.Int32
	m, d := lagDrainHarness(t, &reads)
	m.noteStreamAttached()
	feed(d, synthVorbisTrack(1, 1000, false)) // several seconds on this attachment
	waitForLag(m, boxLagReasonAttach, 3*time.Second)
	// Forget that first attach reading, so the one asserted on below is
	// unambiguously the second attachment's.
	m.mu.Lock()
	m.lastLag = boxLagMeasurement{}
	m.mu.Unlock()

	// The box drops the stream and re-fetches it: a new attachment on the same
	// running engine.
	m.noteStreamAttached()
	feed(d, synthVorbisTrack(2, 1000, false))

	lag := waitForLag(m, boxLagReasonAttach, 3*time.Second)
	if lag.Reason != boxLagReasonAttach {
		t.Fatalf("the first audio page of a new attachment must take the attach reading, got %+v", lag)
	}
	if lag.DeliveredSec > 1 {
		t.Fatalf("the attach baseline counted %.2f s of audio from before the attach: %+v", lag.DeliveredSec, lag)
	}
}

// go-librespot restarts (crash-restart loop, the volume-config restart, the OTA
// sidecar swap) build a new drain while the sink stays open. The delivered
// timeline must continue where it was: a per-run counter starting at zero made
// every later reading negative until the box happened to re-attach.
func TestDeliveredTimelineSurvivesAnEngineRestart(t *testing.T) {
	var reads atomic.Int32
	m, d := lagDrainHarness(t, &reads)
	m.noteStreamAttached()
	feed(d, synthVorbisTrack(1, 300, false))
	m.mu.Lock()
	afterFirstRun := m.deliveredGran
	m.mu.Unlock()
	if afterFirstRun == 0 {
		t.Fatal("the first run delivered no audio, the test proves nothing")
	}

	// The engine restarted: a new drain and a new chain, the same Manager, the
	// box still attached.
	chain := newTestChain(t, 1<<40, nil)
	holdSeams(chain)
	d2 := m.newOggDrain(1<<20, 0, chain)
	d2.paused = true
	feed(d2, synthVorbisTrack(2, 300, false))

	m.mu.Lock()
	afterRestart, base := m.deliveredGran, m.lagBaseGran
	m.mu.Unlock()
	if afterRestart < afterFirstRun {
		t.Fatalf("the delivered timeline went backwards across an engine restart: %d -> %d granules",
			afterFirstRun, afterRestart)
	}
	// And the boundary that opened the new run still measured: a timeline that
	// had restarted at zero would have fallen below its baseline, and the
	// measurement would have been skipped as rebuilt instead.
	lag := waitForLag(m, "track-boundary", 3*time.Second)
	if want := float64(afterFirstRun-base) / vorbisRate; lag.DeliveredSec < want-0.01 {
		t.Fatalf("the reading after the restart saw %.2f s delivered, want the first run's %.2f s carried over: %+v",
			lag.DeliveredSec, want, lag)
	}
}

// skipCutHarness is the drain with a fake sink and a fake box clock, plus the
// flush size the test needs: 1 writes every page straight through (so the
// delivered timeline and the sink's bytes must match exactly at any moment), a
// large one keeps everything in the batch.
func skipCutHarness(t *testing.T, flushBytes int) (*Manager, *oggDrain, *fakeSink) {
	t.Helper()
	chain := newTestChain(t, 1<<40, nil)
	holdSeams(chain)
	m, d, sink := newDrainHarness(t, chain, flushBytes)
	m.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	m.boxPositionFn = func(context.Context) (time.Duration, bool) { return 10 * time.Second, true }
	m.noteStreamAttached()
	return m, d, sink
}

// Audio a skip cut throws away never reaches the box, so the box's own clock
// cannot contain it and the delivered side must not either. Counting it made
// every reading for the rest of the attachment too large by the dropped amount,
// which is bounded by the lead cap, i.e. by the very quantity being measured.
func TestSkipCutDoesNotCountTheStaleAudioItDrops(t *testing.T) {
	m, d, sink := skipCutHarness(t, 1) // every page goes straight to the box

	track := synthVorbisTrack(1, 400, false)
	feed(d, track[:len(track)/2])
	m.NoteSkip() // the user skips mid-track
	feed(d, track[len(track)/2:])

	m.mu.Lock()
	delivered := m.deliveredGran
	m.mu.Unlock()
	got := sinkGranules(t, sink.buf.Bytes())
	if delivered != got {
		t.Fatalf("delivered timeline %d granules, the box was handed %d (%.2f s counted but dropped)",
			delivered, got, float64(delivered-got)/vorbisRate)
	}
	// The cut has to have thrown something away, or the equality above is met by
	// a run in which nothing was dropped.
	if full := sinkGranules(t, bytes.Join(track, nil)); delivered >= full {
		t.Fatalf("the skip cut dropped nothing (%d of %d granules delivered), the test proves nothing", delivered, full)
	}
}

// The other half of the same cut: the batch that was waiting for the box when
// the boundary arrived is dropped too, and it was counted when it was batched.
func TestSkipCutDoesNotCountTheBatchItThrowsAway(t *testing.T) {
	m, d, sink := skipCutHarness(t, 1<<20) // one big batch, nothing reaches the box yet

	feed(d, synthVorbisTrack(1, 400, false))
	m.mu.Lock()
	batched := m.deliveredGran
	m.mu.Unlock()
	if batched == 0 {
		t.Fatal("nothing was batched, the test proves nothing")
	}
	m.NoteSkip()
	feed(d, synthVorbisTrack(2, 10, false)[:1]) // the boundary the cut waits for

	m.mu.Lock()
	delivered := m.deliveredGran
	m.mu.Unlock()
	if got := sinkGranules(t, sink.buf.Bytes()); delivered != got {
		t.Fatalf("the dropped batch stayed on the delivered timeline: %d granules delivered, %d handed to the box",
			delivered, got)
	}
}

// sinkGranules is the audio actually written to the box: the sum of the granule
// advances over the pages in the byte stream, which is the same quantity the
// drain publishes as the delivered timeline.
func sinkGranules(t *testing.T, b []byte) int64 {
	t.Helper()
	var total, trackMax int64
	for off := 0; off+27 <= len(b); {
		if string(b[off:off+4]) != "OggS" {
			t.Fatalf("the sink holds something that is not a page stream, at offset %d", off)
		}
		nsegs := int(b[off+26])
		if off+27+nsegs > len(b) {
			t.Fatalf("truncated page header at offset %d", off)
		}
		body := 0
		for _, l := range b[off+27 : off+27+nsegs] {
			body += int(l)
		}
		if b[off+5]&0x02 != 0 { // BOS: the next track's granules start again
			trackMax = 0
		}
		if gran := oggGranule(b[off:]); gran > trackMax {
			total, trackMax = total+gran-trackMax, gran
		}
		off += 27 + nsegs + body
	}
	return total
}

// The delivered timeline is measured from the attach, so it may only advance
// while a box is attached. A counter that moved for pages nobody received would
// report audio the speaker never got and inflate every later difference.
func TestDeliveredTimelineStaysPutWithNoBoxAttached(t *testing.T) {
	var reads atomic.Int32
	m, d := lagDrainHarness(t, &reads)
	m.mu.Lock()
	m.sink = nil
	m.mu.Unlock()

	feed(d, synthVorbisTrack(1, 60, false))

	m.mu.Lock()
	delivered := m.deliveredGran
	m.mu.Unlock()
	if delivered != 0 {
		t.Fatalf("the delivered timeline advanced to %d granules with nothing attached", delivered)
	}
	if got := reads.Load(); got != 0 {
		t.Fatalf("the box was queried %d times with nothing attached", got)
	}
}
