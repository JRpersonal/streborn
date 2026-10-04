package webui

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// Fleet run 2026-10-04: a SoundTouch 10 needed about ten seconds out of
// standby, the wake and the STOP shared one 10 s context, and the read before
// the STOP failed on the expired deadline. The STOP was never sent and the
// firmware's resumed station became audible on the zone join. Each step after
// the wake now gets a deadline of its own.
func TestQuietWakeStopGetsItsOwnDeadline(t *testing.T) {
	prevNP, prevStop := quietWakeNowPlaying, quietWakeStop
	t.Cleanup(func() { quietWakeNowPlaying, quietWakeStop = prevNP, prevStop })

	quietWakeNowPlaying = func(ctx context.Context, _ string) nowPlayingSnapshot {
		if ctx.Err() != nil {
			return nowPlayingSnapshot{}
		}
		return nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "PLAY_STATE"}
	}
	var stopLeft time.Duration
	stops := 0
	quietWakeStop = func(ctx context.Context, _ string) error {
		stops++
		if dl, ok := ctx.Deadline(); ok {
			stopLeft = time.Until(dl)
		}
		return ctx.Err()
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.10"}
	s.stopQuietWakeResume()
	if stops != 1 {
		t.Fatalf("STOP sent %d times, want once for a speaker that resumed", stops)
	}
	if stopLeft < quietWakeStepBudget/2 {
		t.Fatalf("STOP ran with %s left; it must not inherit a used-up deadline", stopLeft)
	}
}

func TestQuietWakeStopLeavesAnIdleSpeakerAlone(t *testing.T) {
	prevNP, prevStop := quietWakeNowPlaying, quietWakeStop
	t.Cleanup(func() { quietWakeNowPlaying, quietWakeStop = prevNP, prevStop })
	for _, np := range []nowPlayingSnapshot{
		{},
		{Source: "STANDBY"},
		{Source: "INVALID_SOURCE"},
	} {
		quietWakeNowPlaying = func(context.Context, string) nowPlayingSnapshot { return np }
		quietWakeStop = func(context.Context, string) error {
			t.Fatalf("STOP sent for %+v", np)
			return nil
		}
		(&Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.10"}).stopQuietWakeResume()
	}
}

// The whole quiet wake must fit inside the wake handler's deadline, and the
// wake part alone must cover a measured ten-second standby wake.
func TestQuietWakeBudgetsCoverASlowWake(t *testing.T) {
	if quietWakeWakeBudget < 12*time.Second {
		t.Fatalf("wake budget %s does not cover the ~10 s a SoundTouch 10 needs out of standby", quietWakeWakeBudget)
	}
	if quietWakeBudget < quietWakeWakeBudget+2*quietWakeStepBudget {
		t.Fatalf("quietWakeBudget %s leaves no room for the mute and STOP after the wake", quietWakeBudget)
	}
}

// The power-on resume stands down for the whole time STR is waking the speaker
// for a group, not only once the restore is armed at the end of the wake.
func TestPowerOnResumeStandsDownWhileTheGroupWakeRuns(t *testing.T) {
	s := &Server{}
	if s.wokenForGroup() {
		t.Fatal("a speaker nobody woke reports a group wake")
	}
	s.noteQuietWakeEpisode() // stamped before the wake starts
	if s.quietWakeActive() {
		t.Fatal("test premise: nothing is armed yet in the middle of the wake")
	}
	if !s.wokenForGroup() {
		t.Fatal("a power-on frame in the middle of STR's own group wake would resume the last station")
	}
}

// The station starts a moment after the wake reports the speaker awake. A
// speaker that shows its source selected but not yet playing is watched, and
// the STOP goes out once it plays (fleet run 2026-10-04).
func TestQuietWakeStopCatchesALateResume(t *testing.T) {
	prevNP, prevStop := quietWakeNowPlaying, quietWakeStop
	t.Cleanup(func() { quietWakeNowPlaying, quietWakeStop = prevNP, prevStop })
	reads := 0
	quietWakeNowPlaying = func(context.Context, string) nowPlayingSnapshot {
		reads++
		if reads < 3 {
			return nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "STOP_STATE"}
		}
		return nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "PLAY_STATE"}
	}
	stops := 0
	quietWakeStop = func(context.Context, string) error { stops++; return nil }
	(&Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.10"}).stopQuietWakeResume()
	if stops != 1 {
		t.Fatalf("STOP sent %d times after a late resume, want once", stops)
	}
}

// A speaker that stays selected but idle is watched for a bounded time only.
func TestQuietWakeResumeWatchIsBounded(t *testing.T) {
	prevNP, prevStop := quietWakeNowPlaying, quietWakeStop
	t.Cleanup(func() { quietWakeNowPlaying, quietWakeStop = prevNP, prevStop })
	quietWakeNowPlaying = func(context.Context, string) nowPlayingSnapshot {
		return nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "STOP_STATE"}
	}
	quietWakeStop = func(context.Context, string) error { t.Fatal("STOP for an idle speaker"); return nil }
	start := time.Now()
	(&Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.10"}).stopQuietWakeResume()
	if el := time.Since(start); el > quietWakeResumeWatch+2*quietWakeResumePoll {
		t.Fatalf("watched for %s, longer than the bound", el)
	}
}

// A native station the firmware activated by itself is not the user's play: a
// group wake's power-on resume arrives as exactly that frame.
func TestNativeActivationIsNotAUserPlay(t *testing.T) {
	s := quietServer("")
	before := time.Now().Add(-time.Second)
	s.lastPlay = &lastPlayInfo{boxURL: "http://127.0.0.1:8888/stream/2", ts: time.Now(), fromBox: true}
	if s.userPlayedSince(before) {
		t.Fatal("the firmware's own native activation counted as a user play")
	}
	s.lastPlay = &lastPlayInfo{boxURL: "http://127.0.0.1:8888/stream/3", ts: time.Now()}
	if !s.userPlayedSince(before) {
		t.Fatal("a play STR pushed must still count")
	}
}
