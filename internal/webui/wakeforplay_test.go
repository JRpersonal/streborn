package webui

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// A speaker that is already up has no power-on resume to hide: nothing may be
// muted, nothing written, and the settling window stays closed, or the forced
// preset pass and the power-on resume would stand down for nothing.
func TestWakeSilentlyForPlayLeavesAnAwakeSpeakerAlone(t *testing.T) {
	prev := quietWakeNowPlaying
	t.Cleanup(func() { quietWakeNowPlaying = prev })
	reads := 0
	quietWakeNowPlaying = func(context.Context, string) nowPlayingSnapshot {
		reads++
		return nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "PLAY_STATE"}
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.10"}
	start := time.Now()
	restore := s.WakeSilentlyForPlay(context.Background())
	restore()
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("took %s: it drove an awake speaker", el)
	}
	if reads != 1 {
		t.Fatalf("read the state %d times, want the single gate read", reads)
	}
	if s.QuietWakeEpisodeActive() {
		t.Fatal("stamped the settling window for a speaker it never woke")
	}
}

// Without a speaker configured there is nothing to wake.
func TestWakeSilentlyForPlayWithoutABox(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.WakeSilentlyForPlay(context.Background())()
	if s.QuietWakeEpisodeActive() {
		t.Fatal("stamped the settling window without a speaker")
	}
}
