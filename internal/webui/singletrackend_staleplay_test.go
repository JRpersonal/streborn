package webui

import (
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Live, ST10 on FW 27.0.6 (2026-11-04): a 20 minute MP3 with no known length,
// paused at 68 s with the speaker's PLAY_PAUSE key and resumed 2.5 s later.
// The end watch never polled during the pause, the position then stood on 68 s
// while the audio path logged "PLAYING error #0 for stale UrlID 1" once a
// second, and 19 s after the resume the frozen-position net learned a 68 s
// length and stopped a speaker that was playing. While that run is alive the
// freeze is no end; once it stops, the net ends the track as before.
func TestAFrozenPositionIsNoEndWhileTheSpeakerPlaysUnderAStaleURL(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	logs := &syncBuffer{}
	s.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var playing atomic.Bool
	playing.Store(true)
	s.SetBoxLogStalePlayRunFn(func() (time.Time, time.Time, int, bool) {
		now := time.Now()
		if playing.Load() {
			return now.Add(-4 * time.Second), now.Add(-200 * time.Millisecond), 5, false
		}
		return now.Add(-30 * time.Second), now.Add(-10 * time.Second), 20, false
	})
	const url = "http://192.0.2.9:8765/sweep.mp3"
	gen := s.setLastPlay(url, "Sweep", "", "audio/mpeg")
	np.set("BUFFERING_STATE", 0, 0)
	s.armSingleTrackEnd(singleTrack{boxURL: url, title: "Sweep", mime: "audio/mpeg"}, gen)
	time.Sleep(20 * qsec)

	np.climb(1*qsec, 5*qsec)
	// The pause and the resume fell between two polls: from here on the
	// watch only ever sees PLAY_STATE on the paused second.
	np.set("PLAY_STATE", 5*qsec, 0)
	time.Sleep(4 * s.queueTiming.frozenTimeout)

	assertNotEndedNorLearned(t, s, rec, logs, url, "while the speaker plays under a stale URL id")
	if !strings.Contains(logs.String(), "the position is frozen but the speaker still plays it") {
		t.Fatalf("the hold does not log its deciding signal:\n%s", logs.String())
	}

	// The audio stops: the run goes stale, and the freeze is the end again.
	playing.Store(false)
	waitForTrackEnd(t, "the box to be stopped once the stale-id run has stopped", func() bool { return rec.has("Stop") })
}

func TestFrozenButPlayingCountsARunThatBeganJustBeforeThePositionLastMoved(t *testing.T) {
	t.Parallel()
	s := &Server{}
	s.SetBoxLogStalePlayRunFn(staleRun(6*time.Second, 300*time.Millisecond, 6, false))
	// The watch saw the position move 3 s ago, after the run's first line:
	// its poll trailed the resume.
	if sp := s.frozenButPlaying(time.Now().Add(-3 * time.Second)); !sp.ok {
		t.Fatalf("a run that began within staleHoldLead of the last move does not hold the end: %s", sp.why)
	}
	// A run from long before the position last moved belongs to another freeze.
	if sp := s.frozenButPlaying(time.Now().Add(3 * time.Minute)); sp.ok {
		t.Fatalf("a run older than the freeze holds the end: %s", sp.why)
	}
	s.SetBoxLogStalePlayRunFn(staleRun(6*time.Second, 300*time.Millisecond, 6, true))
	if sp := s.frozenButPlaying(time.Time{}); sp.ok {
		t.Fatalf("a run with a buffer stall holds the end: %s", sp.why)
	}
}
