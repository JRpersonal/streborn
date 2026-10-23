// #1190: a music-library folder started on the master of a stereo pair never
// got past track 1. A zone master drops itself to STANDBY about a second after
// the end of the file, inside one 4 s watcher poll, and the watcher then read a
// power-off and ended the queue. These pin the one-second poll for the last
// stretch of a track on a grouped speaker, and that nothing changes for a
// single speaker.

package webui

import (
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestQueueInEndStretch(t *testing.T) {
	t.Parallel()
	const sec = time.Second
	cases := []struct {
		name              string
		pos, end, elapsed time.Duration
		want              bool
	}{
		{"mid-track", 120 * sec, 256 * sec, 121 * sec, false},
		{"just outside the window", 250 * sec, 256 * sec, 251 * sec, false},
		{"field: pos 254 of 256", 254 * sec, 256 * sec, 255 * sec, true},
		{"position on the end", 256 * sec, 256 * sec, 257 * sec, true},
		{"unknown length", 254 * sec, 0, 255 * sec, false},
		{"no position, wall clock near the end", 0, 256 * sec, 253 * sec, true},
		{"no position, wall clock early", 0, 256 * sec, 100 * sec, false},
		{"position ran far past a wrong length (#1065)", 60 * sec, 30 * sec, 62 * sec, false},
		{"frozen on the end long after it", 256 * sec, 256 * sec, 256*sec + 4*productionQueueTiming.zoneEndWindow + sec, false},
	}
	for _, tc := range cases {
		if got := productionQueueTiming.inEndStretch(tc.pos, tc.end, tc.elapsed); got != tc.want {
			t.Errorf("%s: inEndStretch(%v, %v, %v) = %v, want %v",
				tc.name, tc.pos, tc.end, tc.elapsed, got, tc.want)
		}
	}
}

func TestQueueWatchInterval(t *testing.T) {
	t.Parallel()
	qt := &productionQueueTiming
	if got := qt.watchInterval(false, false); got != qt.pollInterval {
		t.Errorf("single speaker mid-track polls every %v, want %v", got, qt.pollInterval)
	}
	if got := qt.watchInterval(false, true); got != qt.pollInterval {
		t.Errorf("single speaker near the end polls every %v, want the normal %v", got, qt.pollInterval)
	}
	if got := qt.watchInterval(true, false); got != qt.pollInterval {
		t.Errorf("grouped speaker mid-track polls every %v, want the normal %v", got, qt.pollInterval)
	}
	got := qt.watchInterval(true, true)
	if got != qt.zoneEndPoll {
		t.Errorf("grouped speaker near the end polls every %v, want %v", got, qt.zoneEndPoll)
	}
	// The poll must be faster than the firmware's end-of-file standby (~1 s
	// after the end) plus a margin, and the window must cover one normal poll,
	// or the fast poll could start too late.
	if got > time.Second {
		t.Errorf("end-of-track poll %v is slower than the zone master's standby", got)
	}
	if qt.zoneEndWindow < qt.pollInterval {
		t.Errorf("window %v is shorter than one normal poll %v", qt.zoneEndWindow, qt.pollInterval)
	}
}

// zoneMasterBox plays one track like a zone master: the position climbs one
// box second (tick) per box second from start, freezes at total, and one box
// second after the end of the file the box reports STANDBY (firmware
// EVT_STANDBY).
type zoneMasterBox struct {
	mu    sync.Mutex
	start time.Time
	total time.Duration
	tick  time.Duration
}

func (b *zoneMasterBox) poll() (string, time.Duration, time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	since := time.Since(b.start)
	if since < 0 {
		return "BUFFERING_STATE", 0, b.total, false
	}
	if since >= b.total+b.tick {
		return "", 0, 0, true
	}
	pos := since.Truncate(b.tick)
	if pos > b.total {
		pos = b.total
	}
	return "PLAY_STATE", pos, b.total, false
}

func TestAZoneMasterAdvancesBeforeItsEndOfFileStandby(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("real-time watcher test")
	}
	// A gentler clock than fastQueueTiming's: this test is a race between the
	// one-second end poll and the standby a second after the file, and at 20x
	// its slack would be a few tens of milliseconds.
	const scale = 5
	const sec = time.Second / scale
	const trackLen = 10 * sec
	logs := &syncBuf{}
	// Audio starts half a second after the push, as on a real box. The file ends
	// at about 10.5 s and the standby lands at about 11.5 s: with the plain 4 s
	// poll the watcher sees pos 7 at 8 s and then the standby at 12 s.
	box := &zoneMasterBox{start: time.Now().Add(sec / 2), total: trackLen, tick: sec}
	s, _ := newPlayTestServer(t)
	s.queueTiming = scaleQueueTiming(scale)
	s.queueInZoneFn = func() bool { return true }
	s.nowPlayingFn = box.poll
	s.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s.queue.load([]queueItem{
		{URL: "http://192.0.2.10:50002/1.m4a", Title: "01 First", Mime: "audio/mp4", Duration: trackLen},
		{URL: "http://192.0.2.10:50002/2.m4a", Title: "02 Second", Mime: "audio/mp4", Duration: 5 * time.Minute},
	}, 0, false, repeatOff)
	s.setQueueTiming(trackLen)
	s.ensureWatcher()
	t.Cleanup(s.cancelWatcher)

	deadline := time.Now().Add(16 * sec)
	for time.Now().Before(deadline) {
		if s.queue.snapshot().Pos == 1 {
			break
		}
		if !s.queue.isActive() {
			t.Fatalf("the queue ended before it advanced; log:\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pos := s.queue.snapshot().Pos; pos != 1 {
		t.Fatalf("queue pos = %d, want 1; log:\n%s", pos, logs.String())
	}
	out := logs.String()
	if !strings.Contains(out, "polling every second until it ends") {
		t.Errorf("no line saying the end-of-track poll started; log:\n%s", out)
	}
	if strings.Contains(out, "box entered standby") {
		t.Errorf("the standby reading won the race; log:\n%s", out)
	}
}
