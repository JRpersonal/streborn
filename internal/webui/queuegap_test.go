// #923: six to seven seconds of silence between folder tracks.
//
// Timed against the same Synology folder played twice on one ST20, once driven
// by STR and once by the box itself: 157 s against 150 s, and 236 s against
// 230 s. The box's own advance is 0.8 s, which is why the Bose app sounds
// gapless and STR does not.
//
// The chassis finishes a file and freezes in PLAY_STATE at EOF instead of
// emitting STOP, so the STOP branch never runs and every track ends on the
// wall-clock net at end + timerMargin.
//
// Those six seconds are a grace for a box that reports NO position: with nothing
// to corroborate the length against, cutting a buffering track short is the
// worse failure. A box that reports a position AND reports it sitting at the end
// has already said the track is over.

package webui

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTheGraceIsSkippedWhenTheBoxConfirmsTheEnd(t *testing.T) {
	t.Parallel()
	s := quietServer("192.0.2.10")
	const end = 3 * time.Minute

	// The #923 shape: the box reports a position, and it is at the end.
	if m := s.advanceMargin(end, end); m != 0 {
		t.Fatalf("margin %s with the position at the end; that is the gap users hear", m)
	}
	// Just inside the epsilon still counts as the end.
	if m := s.advanceMargin(end-productionQueueTiming.endEpsilon+time.Second, end); m != 0 {
		t.Fatalf("margin %s for a position inside the end epsilon", m)
	}
}

func TestTheGraceStaysWhereThereIsNothingToCorroborateWith(t *testing.T) {
	t.Parallel()
	s := quietServer("192.0.2.10")
	const end = 3 * time.Minute

	margin := productionQueueTiming.timerMargin
	// No position at all: the FRITZ!Box-class case the margin was written for.
	if m := s.advanceMargin(0, end); m != margin {
		t.Fatalf("margin %s with no position reported, want the full %s", m, margin)
	}
	// A position that is nowhere near the end: the track is still running, and
	// cutting it off is the failure the grace exists to prevent.
	if m := s.advanceMargin(30*time.Second, end); m != margin {
		t.Fatalf("margin %s mid-track, want the full %s", m, margin)
	}
}

// The floor is the poll interval, and it stays where it is: a faster poll would
// run on every speaker all day for this one case.
func TestThePollIntervalIsNotTightened(t *testing.T) {
	t.Parallel()
	if poll := productionQueueTiming.pollInterval; poll < 4*time.Second {
		t.Fatalf("the poll interval dropped to %s; that runs on every speaker all day", poll)
	}
}

// A track that carries a length ends on the poll that sees it end, and that
// poll used to land anywhere up to a whole interval after the end. The watcher
// now moves the one poll before the end so it lands just past it. Run at a
// quarter of the production clock: one "unit" is a production second.
func TestTheEndOfATrackIsSeenOnARetimedPoll(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("real-time watcher test")
	}
	const scale = 4
	unit := time.Second / scale
	s, _ := newPlayTestServer(t)
	s.queueTiming = scaleQueueTiming(scale)
	s.queueInZoneFn = func() bool { return false }
	// A 9 s track: the regular polls land at 4, 8 and 12 s, so without the
	// re-timing the end is seen 3 s late.
	const trackUnits = 9
	var (
		polls atomic.Int32
		t0    time.Time
	)
	s.nowPlayingFn = func() (string, time.Duration, time.Duration, bool) {
		polls.Add(1)
		p := time.Since(t0).Truncate(unit)
		if p > trackUnits*unit {
			p = trackUnits * unit // frozen at EOF, the ST10/ST20 way
		}
		return "PLAY_STATE", p, trackUnits * unit, false
	}
	s.queue.load([]queueItem{
		{URL: "http://192.0.2.10:50002/1.mp3", Title: "01 First", Mime: "audio/mpeg", Duration: trackUnits * unit},
		{URL: "http://192.0.2.10:50002/2.mp3", Title: "02 Second", Mime: "audio/mpeg", Duration: 5 * time.Minute},
	}, 0, false, repeatOff)
	t0 = time.Now()
	s.setQueueTiming(trackUnits * unit)
	s.ensureWatcher()
	t.Cleanup(s.cancelWatcher)

	deadline := time.Now().Add(10 * time.Second)
	for s.queue.snapshot().Pos == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	took := time.Since(t0)
	if s.queue.snapshot().Pos != 1 {
		t.Fatal("the queue never advanced")
	}
	if took < trackUnits*unit {
		t.Fatalf("advanced after %v, before the track's %v were over", took, trackUnits*unit)
	}
	if took > (trackUnits+2)*unit {
		t.Fatalf("advanced after %v; the poll was not moved to the end of the %v track", took, trackUnits*unit)
	}
	// Back on the normal interval after the advance: no faster polling.
	before := polls.Load()
	time.Sleep(8*unit + unit/2)
	if n := polls.Load() - before; n > 3 {
		t.Fatalf("%d polls in two intervals after the advance; the watcher kept a faster pace", n)
	}
}

func TestAlignToEnd(t *testing.T) {
	t.Parallel()
	qt := productionQueueTiming
	sec := time.Second
	if d, ok := qt.alignToEnd(178*sec, 180*sec, qt.pollInterval); !ok || d != 2*sec+qt.alignEpsilon {
		t.Fatalf("2 s before the end: %v %v, want one poll 2 s plus epsilon away", d, ok)
	}
	for _, c := range []struct {
		name     string
		pos, end time.Duration
	}{
		{"end beyond the coming interval", 170 * sec, 180 * sec},
		{"already at the end", 180 * sec, 180 * sec},
		{"past the end", 185 * sec, 180 * sec},
		{"no length", 170 * sec, 0},
		{"no position", 0, 3 * sec},
	} {
		if d, ok := qt.alignToEnd(c.pos, c.end, qt.pollInterval); ok {
			t.Errorf("%s: re-timed to %v", c.name, d)
		}
	}
}

// The FRITZ!Box MP3 shape on a queue: the box reports 120 s at the first poll
// and freezes there. Without the guard the frozen-position net skipped to the
// next track 15 s later, cutting a track that was still playing.
func TestAQueueTrackWhosePositionRunsAheadIsNotSkipped(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("real-time watcher test")
	}
	box := &scriptedQueueBox{tick: qsec}
	logs := &syncBuf{}
	box.set("PLAY_STATE", 120*qsec, 0, false)
	s := queueWatchServer(t, box, 0, logs)

	time.Sleep(40 * qsec) // well past the freeze timeout
	if pos := s.queue.snapshot().Pos; pos != 0 {
		t.Fatalf("the queue moved to track %d on a position the box cannot have played", pos)
	}
	if n := strings.Count(logs.String(), "position runs ahead of real time"); n != 1 {
		t.Fatalf("the ahead-of-real-time line was logged %d times, want once per track; log:\n%s", n, logs.String())
	}
}

// A track without a length that the box plays in real time and freezes on at
// its end still advances on the frozen-position net.
func TestANormalEOFFreezeStillEndsAQueueTrack(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("real-time watcher test")
	}
	box := &scriptedQueueBox{tick: qsec}
	logs := &syncBuf{}
	box.set("PLAY_STATE", 0, 0, true)
	s := queueWatchServer(t, box, 0, logs)

	time.Sleep(10 * qsec)
	_, pos, _, _ := box.poll()
	box.set("PLAY_STATE", pos, 0, false) // EOF: frozen in PLAY_STATE

	deadline := time.Now().Add(10 * time.Second)
	for s.queue.snapshot().Pos == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.queue.snapshot().Pos != 1 {
		t.Fatalf("a frozen track without a length never advanced; log:\n%s", logs.String())
	}
	out := logs.String()
	if !strings.Contains(out, "net=frozen-position") {
		t.Fatalf("advance not decided by the frozen-position net; log:\n%s", out)
	}
	if strings.Contains(out, "position runs ahead of real time") {
		t.Fatalf("a real-time position was flagged as running ahead; log:\n%s", out)
	}
}
