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

// #830, second field report: STR restarted a healthy resume.
//
// An ST10 (FW 27.0.6, 2026-10-31) resumed a directly played file by itself,
// and STR restarted it anyway: the file's start for two seconds, then a seek
// back. Why the RelTime veto let that through is not measured (a frozen or an
// unreadable RelTime); these tests pin the behaviour with the syslog's "Audio
// path offset N" as a second clock and "for stale UrlID" as the freeze's own
// signature.

// scriptedOffset is the syslog play clock for audioPathOffsetFn: it starts at
// sec and advances by step on every read (0 = frozen), stamped with the read's
// own time so it is always fresh. It does not model the line's real timing
// (one a second, stamped when the reader saw it); the single-line test below
// does.
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

// The offset line comes once a second, and a resume that re-buffers can leave
// a single one inside the plain Play's window. Read against the paused offset
// the syslog recorded, that one line is the evidence; RelTime (frozen on the
// fake speaker's 0:00:15) adds nothing. The line is stamped once, when the
// speaker took the Play, and never again: a real line, not one per read.
func TestAppResumeKeepsAPlainPlayWithASingleOffsetLine(t *testing.T) {
	t.Parallel()
	s, box, logs := newLoggedPausedFileServer(t)
	armTestFile(s)
	var (
		mu     sync.Mutex
		lineAt time.Time
	)
	s.SetBoxLogPlayClockFns(func() (int, time.Time, bool) {
		mu.Lock()
		defer mu.Unlock()
		if lineAt.IsZero() {
			for _, c := range box.commands() {
				if c == "Play" {
					lineAt = time.Now()
				}
			}
		}
		if lineAt.IsZero() {
			// Paused: the last line before the pause, long stopped coming.
			return 42, time.Now().Add(-time.Minute), true
		}
		return 43, lineAt, true
	}, staleAfter(time.Time{}))
	s.notePausedAtFrom(42*time.Second, true)
	if _, err := s.resumePlayback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(box.commands(), ","); got != "Play" {
		t.Fatalf("commands = %s, want the plain Play alone: one offset line past the paused 42 s is a resume that worked", got)
	}
	out := logs.String()
	for _, want := range []string{"the plain play continued it", `offsetBase="42s (paused)"`, `offsetFirst="43s age`, "relTimeBefore=15s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log lacks %q:\n%s", want, out)
		}
	}
}

// Every repair verdict logs both clocks' raw readings, so a field bundle shows
// what RelTime and the offset did through the resume, not only the verdict.
func TestTheRepairLogsBothClocksRaw(t *testing.T) {
	t.Parallel()
	s, _, logs := newLoggedPausedFileServer(t)
	armTestFile(s)
	s.relPositionFn = (&scriptedRelPosition{pos: 15 * time.Second}).read
	s.SetBoxLogPlayClockFns((&scriptedOffset{sec: 15}).read, staleAfter(time.Time{}))

	s.repairFrozenFileResume(testPodcast(), s.RecallGeneration(), 15*time.Second, time.Now().Add(-time.Minute))

	out := logs.String()
	for _, want := range []string{"froze on the speaker", "relTime1=15s", "relTime2=15s", "offset1=\"15s age", "offset2=\"15s age", "evidence=\"no clock moved\""} {
		if !strings.Contains(out, want) {
			t.Fatalf("repair log lacks %s:\n%s", want, out)
		}
	}
}

// A resume that needed no restart must not leave the pause on the end watch's
// clock. Before, every pause on the speakers that freeze ended in a restart,
// which re-anchored the clock; a resume the speaker did by itself (or a plain
// Play that worked) has none, and with now_playing standing on the paused
// second the wall-clock net then cut the file short by the pause's length.
func TestALongPauseWithoutARestartDoesNotCutTheFileShort(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	logs := &syncBuffer{}
	s.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	shrinkFileRestart(s)
	s.fileResumeTiming.freezeAfter = productionFileResumeTiming.freezeAfter / queueTestScale
	// The renderer's clock runs on through the resume: the repair is vetoed.
	rel := &scriptedRelPosition{pos: 6 * time.Second, step: time.Second}
	s.relPositionFn = rel.read
	url := "http://192.0.2.20:8000/song.mp3"
	gen := s.setLastPlay(url, "Song", "", "audio/mpeg")
	np.set("PLAY_STATE", 5*qsec, 48*qsec)
	s.armSingleTrackEnd(singleTrack{boxURL: url, title: "Song", mime: "audio/mpeg",
		meta: upnp.TrackMeta{Duration: 48 * qsec, Seekable: true}, dur: 48 * qsec}, gen)
	time.Sleep(betweenPolls(1))
	// Paused for longer than the whole track.
	np.set("PAUSE_STATE", 6*qsec, 48*qsec)
	time.Sleep(60 * qsec)
	// Resumed; now_playing stands on the paused second from here on.
	np.set("PLAY_STATE", 6*qsec, 48*qsec)

	// About 12 qsec after the resume the watch asks the renderer's clock and
	// leaves the play alone. Two polls later the file has played some 30 of
	// its 48 qsec; the clock with the pause on it says 90.
	waitForTrackEnd(t, "the watch to veto the repair on the renderer's clock", func() bool { return rel.count() >= 1 })
	time.Sleep(betweenPolls(2))
	for _, a := range []string{"Stop", "Seek", "SetAVTransportURI"} {
		if rec.has(a) {
			t.Fatalf("the file was cut short after a long pause (%s sent): %v", a, rec.list())
		}
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
