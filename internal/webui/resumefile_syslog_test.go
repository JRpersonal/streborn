package webui

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/upnp"
)

// #830, second field report: RelTime frozen through a healthy resume.
//
// An ST10 (FW 27.0.6, 2026-10-31) resumed a directly played file correctly by
// itself, but GetPositionInfo RelTime and now_playing kept the paused second.
// STR restarted the healthy play: the file's start for two seconds, then a seek
// back to the stale second. The speaker's syslog "Audio path offset N" counted
// on with the music; the real freeze logs "for stale UrlID" instead.

// scriptedOffset is the syslog play clock for audioPathOffsetFn: it starts at
// sec and advances by step on every read (0 = frozen), stamped with the read's
// own time so it is always fresh.
type scriptedOffset struct {
	mu   sync.Mutex
	sec  int
	step int
}

func (o *scriptedOffset) read() (int, time.Time, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	cur := o.sec
	o.sec += o.step
	return cur, time.Now(), true
}

// staleAfter is a StaleURLIDSince for a line logged at the given time; zero
// means never.
func staleAfter(at time.Time) func(time.Time) bool {
	return func(t time.Time) bool { return !at.IsZero() && !at.Before(t) }
}

func newLoggedPausedFileServer(t *testing.T) (*Server, *pausedFileBox, *syncBuffer) {
	t.Helper()
	s, box := newPausedFileServer(t, "PAUSED_PLAYBACK")
	buf := &syncBuffer{}
	s.logger = slog.New(slog.NewTextHandler(buf, nil))
	return s, box, buf
}

func testPodcast() singleTrack {
	return singleTrack{boxURL: testFileURL, title: "Podcast", mime: "audio/mpeg",
		meta: upnp.TrackMeta{Duration: 240 * time.Second, Seekable: true}, dur: 240 * time.Second}
}

func TestTheRepairLeavesAResumeAloneWhenTheSyslogClockMoves(t *testing.T) {
	t.Parallel()
	s, box, logs := newLoggedPausedFileServer(t)
	armTestFile(s)
	s.relPositionFn = (&scriptedRelPosition{pos: 15 * time.Second}).read // frozen on the paused second
	s.SetBoxLogPlayClockFns((&scriptedOffset{sec: 27, step: 1}).read, staleAfter(time.Time{}))

	s.repairFrozenFileResume(testPodcast(), s.RecallGeneration(), 15*time.Second, time.Now().Add(-time.Minute))

	if got := box.commands(); len(got) != 0 {
		t.Fatalf("a resume the speaker did by itself was restarted over: %v", got)
	}
	if out := logs.String(); !strings.Contains(out, "continued by itself") || !strings.Contains(out, "audio path offset moved") {
		t.Fatalf("log does not name the syslog clock as the evidence:\n%s", out)
	}
}

func TestTheRepairStillRestartsWhenTheSyslogClockStandsOrTheURLWentStale(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		step  int // -1: no syslog clock wired at all
		stale bool
	}{
		{"offset frozen", 0, false},
		// Whether the offset counts during the real freeze is not measured:
		// the stale-URL line wins over it.
		{"stale URL id", 1, true},
		{"no syslog clock", -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, box, logs := newLoggedPausedFileServer(t)
			armTestFile(s)
			s.relPositionFn = (&scriptedRelPosition{pos: 15 * time.Second}).read
			if tc.step >= 0 {
				var staleAt time.Time
				if tc.stale {
					staleAt = time.Now()
				}
				s.SetBoxLogPlayClockFns((&scriptedOffset{sec: 15, step: tc.step}).read, staleAfter(staleAt))
			}
			s.repairFrozenFileResume(testPodcast(), s.RecallGeneration(), 15*time.Second, time.Now().Add(-time.Minute))
			if got := strings.Join(box.commands(), ","); got != "Stop,Play,Seek" {
				t.Fatalf("commands = %s, want the restart Stop,Play,Seek", got)
			}
			if tc.stale && !strings.Contains(logs.String(), "stale URL id") {
				t.Fatalf("the restart does not log the stale-URL evidence:\n%s", logs.String())
			}
		})
	}
}

// The end watch takes the paused position off the syslog clock at the
// PAUSE_STATE poll, and the repair seeks there rather than to the RelTime
// second now_playing stands on, which can name an older pause.
func TestTheEndWatchRepairSeeksToTheSyslogPausePosition(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	logs := &syncBuffer{}
	s.logger = slog.New(slog.NewTextHandler(logs, nil))
	shrinkFileRestart(s)
	s.fileResumeTiming.freezeAfter = productionFileResumeTiming.freezeAfter / queueTestScale
	s.relPositionFn = (&scriptedRelPosition{pos: 31 * time.Second}).read // frozen
	s.SetBoxLogPlayClockFns((&scriptedOffset{sec: 45}).read, staleAfter(time.Time{}))
	url := "http://192.0.2.20:8000/hoerspiel.mp3"
	gen := s.setLastPlay(url, "Episode", "", "audio/mpeg")
	np.set("PLAY_STATE", 29*time.Second, 1800*time.Second)
	s.armSingleTrackEnd(singleTrack{boxURL: url, title: "Episode", mime: "audio/mpeg",
		meta: upnp.TrackMeta{Duration: 1800 * time.Second, Seekable: true}, dur: 1800 * time.Second}, gen)
	time.Sleep(betweenPolls(1))
	np.set("PAUSE_STATE", 31*time.Second, 1800*time.Second)
	time.Sleep(betweenPolls(1))
	np.set("PLAY_STATE", 31*time.Second, 1800*time.Second)

	waitForTrackEnd(t, "the frozen resume to be restarted", func() bool { return rec.has("Seek") })
	waitForTrackEnd(t, "the restart's log line", func() bool { return strings.Contains(logs.String(), "continued where it was paused") })
	if out := logs.String(); !strings.Contains(out, "at=45s") {
		t.Fatalf("the restart did not seek to the syslog's paused 45 s:\n%s", out)
	}
}

// The app's resume takes the syslog pause position recorded at the pause over
// a RelTime behind it, also once the offset lines have stopped coming.
func TestAppResumeSeeksToTheRecordedSyslogPausePosition(t *testing.T) {
	t.Parallel()
	s, box, _ := newLoggedPausedFileServer(t)
	armTestFile(s)
	s.SetBoxLogPlayClockFns(func() (int, time.Time, bool) { return 42, time.Now().Add(-time.Minute), true }, staleAfter(time.Time{}))
	s.notePausedAtFrom(42*time.Second, true)
	if _, err := s.resumePlayback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(box.seekTo) != 1 || box.seekTo[0] != "0:00:42.000" {
		t.Fatalf("seek target = %v, want the recorded syslog 0:00:42 over RelTime 0:00:15", box.seekTo)
	}
}

func TestAppResumeKeepsAPlainPlayThatContinues(t *testing.T) {
	t.Parallel()
	t.Run("syslog clock moves", func(t *testing.T) {
		t.Parallel()
		s, box, logs := newLoggedPausedFileServer(t)
		armTestFile(s)
		s.SetBoxLogPlayClockFns((&scriptedOffset{sec: 15, step: 1}).read, staleAfter(time.Time{}))
		if status, err := s.resumePlayback(context.Background()); err != nil || status != "playing" {
			t.Fatalf("resumePlayback = %q, %v", status, err)
		}
		if got := strings.Join(box.commands(), ","); got != "Play" {
			t.Fatalf("commands = %s, want the plain Play alone", got)
		}
		if !strings.Contains(logs.String(), "the plain play continued it") {
			t.Fatalf("log does not record the plain-play path:\n%s", logs.String())
		}
	})
	t.Run("RelTime moves, no syslog", func(t *testing.T) {
		t.Parallel()
		s, box, _ := newLoggedPausedFileServer(t)
		armTestFile(s)
		s.relPositionFn = (&scriptedRelPosition{pos: 15 * time.Second, step: time.Second}).read
		if _, err := s.resumePlayback(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(box.commands(), ","); got != "Play" {
			t.Fatalf("commands = %s, want the plain Play alone", got)
		}
	})
}

func TestAppResumeRestartsAPlainPlayThatFroze(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		step  int
		stale bool
	}{
		{"syslog clock frozen", 0, false},
		{"stale URL id although the offset moves", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, box, _ := newLoggedPausedFileServer(t)
			armTestFile(s)
			stale := func(time.Time) bool { return tc.stale }
			s.SetBoxLogPlayClockFns((&scriptedOffset{sec: 15, step: tc.step}).read, stale)
			if _, err := s.resumePlayback(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(box.commands(), ","); got != "Play,Stop,Play,Seek" {
				t.Fatalf("commands = %s, want the plain Play, then Stop,Play,Seek", got)
			}
			if len(box.seekTo) != 1 || box.seekTo[0] != "0:00:15.000" {
				t.Fatalf("seek target = %v, want 0:00:15", box.seekTo)
			}
		})
	}
}

// A fresh syslog clock is the paused position, over a RelTime that stood on an
// older pause (the fake speaker answers 0:00:15).
func TestAppResumeSeeksToTheSyslogClockOverAStaleRelTime(t *testing.T) {
	t.Parallel()
	s, box, _ := newLoggedPausedFileServer(t)
	armTestFile(s)
	s.SetBoxLogPlayClockFns((&scriptedOffset{sec: 57}).read, staleAfter(time.Time{}))
	if _, err := s.resumePlayback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(box.seekTo) != 1 || box.seekTo[0] != "0:00:57.000" {
		t.Fatalf("seek target = %v, want the syslog's 0:00:57", box.seekTo)
	}
}

// An app pause records the syslog clock over RelTime while it is current.
func TestAnAppPauseRecordsTheSyslogClock(t *testing.T) {
	t.Parallel()
	s, _, _ := newLoggedPausedFileServer(t)
	s.SetBoxLogPlayClockFns((&scriptedOffset{sec: 33}).read, nil)
	s.notePausePosition(context.Background())
	if p, ok := s.pausePositionFromLog(); !ok || p != 33*time.Second {
		t.Fatalf("pause position = %v from log %v, want 33s from the syslog", p, ok)
	}
	s.SetBoxLogPlayClockFns(nil, nil)
	s.notePausePosition(context.Background())
	if _, ok := s.pausePositionFromLog(); ok {
		t.Fatal("without the syslog the pause position must come from RelTime")
	}
	if got := s.pausePosition(); got != 15*time.Second {
		t.Fatalf("pause position = %v, want RelTime 15s", got)
	}
}
