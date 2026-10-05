// #1065: a MinimServer library track was cut after about 33 s, six runs in a
// row, by STR's own wall-clock net while the speaker was still playing it. The
// server's length was wrong, and the box's climbing position said so the whole
// time. These pin that the position wins over a wrong length, without giving
// back the #219/#923 behaviour of advancing promptly when the box freezes at
// the end of a file instead of reporting STOP.

package webui

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTrackEndNet(t *testing.T) {
	const (
		sec    = time.Second
		moved  = 10 * time.Millisecond // the position climbed on this poll
		oneTik = queuePollInterval     // the position did not move on this poll
	)
	cases := []struct {
		name string
		in   trackEndInput
		want endNet
	}{
		{
			name: "#1065: length 30 s, box position still climbing past 40 s, box reports no total",
			in: trackEndInput{sawPlay: true, playing: true, elapsed: 44 * sec, dur: 30 * sec,
				lastPos: 42 * sec, sinceLastPos: moved},
			want: endNetNone,
		},
		{
			name: "#1065: length 30 s, box echoes the same short total, position climbing past it",
			in: trackEndInput{sawPlay: true, playing: true, elapsed: 44 * sec, dur: 30 * sec,
				obsTotal: 30 * sec, lastPos: 42 * sec, sinceLastPos: moved},
			want: endNetNone,
		},
		{
			name: "#1065: the tick the cut used to happen on, position climbing, not at the box's end",
			in: trackEndInput{sawPlay: true, playing: true, elapsed: 33 * sec, dur: 30 * sec,
				lastPos: 31 * sec, sinceLastPos: moved},
			want: endNetNone,
		},
		{
			name: "#1065: wrong length, the box finally freezes at the real end",
			in: trackEndInput{sawPlay: true, playing: true, elapsed: 250 * sec, dur: 30 * sec,
				lastPos: 232 * sec, sinceLastPos: queueFrozenTimeout + sec},
			want: endNetFrozen,
		},
		{
			name: "wrong length, position paused on one poll mid-track is not an end yet",
			in: trackEndInput{sawPlay: true, playing: true, elapsed: 120 * sec, dur: 30 * sec,
				lastPos: 118 * sec, sinceLastPos: oneTik},
			want: endNetNone,
		},
		{
			name: "#219/#923: known length, box frozen on PLAY_STATE at EOF",
			in: trackEndInput{sawPlay: true, playing: true, elapsed: 151 * sec, dur: 150 * sec,
				obsTotal: 150 * sec, lastPos: 150 * sec, sinceLastPos: oneTik},
			want: endNetWallClock,
		},
		{
			name: "#923: box reaches its own total on this poll, no extra wait",
			in: trackEndInput{sawPlay: true, playing: true, elapsed: 151 * sec, dur: 150 * sec,
				obsTotal: 150 * sec, lastPos: 150 * sec, sinceLastPos: moved},
			want: endNetWallClock,
		},
		{
			name: "known length, no box total, position frozen at EOF",
			in: trackEndInput{sawPlay: true, playing: true, elapsed: 152 * sec, dur: 150 * sec,
				lastPos: 150 * sec, sinceLastPos: oneTik},
			want: endNetWallClock,
		},
		{
			name: "known length, frozen long past the end (wall-clock held earlier)",
			in: trackEndInput{sawPlay: true, playing: true, elapsed: 170 * sec, dur: 150 * sec,
				lastPos: 150 * sec, sinceLastPos: queueFrozenTimeout + sec},
			want: endNetWallClock,
		},
		{
			name: "no position reports at all: wall clock as today, after the margin",
			in:   trackEndInput{sawPlay: true, playing: true, elapsed: 30*sec + queueTimerMargin, dur: 30 * sec},
			want: endNetWallClock,
		},
		{
			name: "no position reports at all: not before the margin",
			in:   trackEndInput{sawPlay: true, playing: true, elapsed: 30*sec + queueTimerMargin - sec, dur: 30 * sec},
			want: endNetNone,
		},
		{
			name: "#380/#381: unknown length, position frozen",
			in: trackEndInput{sawPlay: true, playing: true, elapsed: 200 * sec,
				lastPos: 180 * sec, sinceLastPos: queueFrozenTimeout},
			want: endNetFrozen,
		},
		{
			name: "unknown length, frozen while buffering is a stall, not an end",
			in: trackEndInput{sawPlay: true, playing: false, elapsed: 200 * sec,
				lastPos: 180 * sec, sinceLastPos: queueFrozenTimeout},
			want: endNetNone,
		},
		{
			name: "known length, frozen mid-track before the length elapsed",
			in: trackEndInput{sawPlay: true, playing: true, elapsed: 100 * sec, dur: 150 * sec,
				lastPos: 80 * sec, sinceLastPos: queueFrozenTimeout + sec},
			want: endNetNone,
		},
		{
			name: "never saw playback",
			in:   trackEndInput{elapsed: 500 * sec, dur: 30 * sec},
			want: endNetNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := trackEndNet(tc.in); got != tc.want {
				t.Fatalf("trackEndNet = %s, want %s", got, tc.want)
			}
		})
	}
}

// scriptedQueueBox scripts now_playing for the queue watcher. With climbing set, the
// position advances one second per real second from posAt, like a box playing.
type scriptedQueueBox struct {
	mu       sync.Mutex
	status   string
	pos      time.Duration
	total    time.Duration
	climbing bool
	posAt    time.Time
}

func (f *scriptedQueueBox) set(status string, pos, total time.Duration, climbing bool) {
	f.mu.Lock()
	f.status, f.pos, f.total, f.climbing, f.posAt = status, pos, total, climbing, time.Now()
	f.mu.Unlock()
}

func (f *scriptedQueueBox) poll() (string, time.Duration, time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pos := f.pos
	if f.climbing {
		pos += time.Since(f.posAt).Truncate(time.Second)
	}
	return f.status, pos, f.total, false
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncBuf) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuf) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func queueWatchServer(t *testing.T, box *scriptedQueueBox, firstDur time.Duration, logTo io.Writer) *Server {
	t.Helper()
	s, _ := newPlayTestServer(t)
	s.logger = slog.New(slog.NewTextHandler(logTo, nil))
	s.nowPlayingFn = box.poll
	s.queue.load([]queueItem{
		{URL: "http://192.0.2.10:50002/1.m4a", Title: "01 First", Mime: "audio/mp4", Duration: firstDur},
		{URL: "http://192.0.2.10:50002/2.m4a", Title: "02 Second", Mime: "audio/mp4", Duration: 5 * time.Minute},
		{URL: "http://192.0.2.10:50002/3.m4a", Title: "03 Third", Mime: "audio/mp4", Duration: 5 * time.Minute},
	}, 0, false, repeatOff)
	s.setQueueTiming(firstDur)
	s.ensureWatcher()
	t.Cleanup(s.cancelWatcher)
	return s
}

func TestAPlayingTrackIsNotCutAtAWrongServerLength(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time watcher test")
	}
	box := &scriptedQueueBox{}
	logs := &syncBuf{}
	// The server claims 2 s; the box plays on and its position keeps climbing.
	box.set("PLAY_STATE", time.Second, 0, true)
	s := queueWatchServer(t, box, 2*time.Second, logs)

	time.Sleep(13 * time.Second) // three polls, all well past 2 s + the margin
	if pos := s.queue.snapshot().Pos; pos != 0 {
		t.Fatalf("the queue moved to track %d while the box was still playing track 1", pos)
	}
	if !strings.Contains(logs.String(), "the server's track length is shorter than what the speaker plays") {
		t.Errorf("no warning that the server's length was short; log:\n%s", logs.String())
	}
	if n := strings.Count(logs.String(), "shorter than what the speaker plays"); n != 1 {
		t.Errorf("the length warning was logged %d times, want once per track", n)
	}
}

func TestANaturalEndAdvancesOnceAndLogsTheEndingTrack(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time watcher test")
	}
	box := &scriptedQueueBox{}
	logs := &syncBuf{}
	// Track 1 is 3 s long and the box sits frozen at its end (#219/#923 shape).
	box.set("PLAY_STATE", 3*time.Second, 3*time.Second, false)
	s := queueWatchServer(t, box, 3*time.Second, logs)

	deadline := time.Now().Add(15 * time.Second)
	for s.queue.snapshot().Pos == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if pos := s.queue.snapshot().Pos; pos != 1 {
		t.Fatalf("queue pos = %d, want 1 after the first track ended", pos)
	}
	// Track 2 starts and plays normally: it must not be advanced past.
	box.set("PLAY_STATE", time.Second, 5*time.Minute, true)
	time.Sleep(9 * time.Second)
	if pos := s.queue.snapshot().Pos; pos != 1 {
		t.Fatalf("queue pos = %d, want it to stay on track 2", pos)
	}
	out := logs.String()
	if n := strings.Count(out, `msg="queue advance"`); n != 1 {
		t.Fatalf("%d advances logged, want exactly one; log:\n%s", n, out)
	}
	for _, want := range []string{`endedTitle="01 First"`, "lengthSec=3", "lastPosSec=3",
		"boxTotalSec=3", "elapsedSec=", "net=wall-clock", `nextTitle="02 Second"`} {
		if !strings.Contains(out, want) {
			t.Errorf("advance log line lacks %s; log:\n%s", want, out)
		}
	}
}
