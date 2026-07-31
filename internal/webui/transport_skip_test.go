package webui

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// The phone remote's Previous/Next controls call transportSkip, which must skip
// Spotify when it is the live source (the box cannot skip a UPnP source itself)
// and otherwise advance the STR play queue.

func TestTransportSkipRoutesToSpotifyWhenStreaming(t *testing.T) {
	got := make(chan bool, 4)
	s := &Server{
		queue:            newPlayQueue(),
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		spotifyStreaming: func() bool { return true },
		spotifySkip: func(_ context.Context, forward bool) error {
			got <- forward
			return nil
		},
	}
	src, err := s.transportSkip(context.Background(), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src != "spotify" {
		t.Fatalf("source = %q, want spotify", src)
	}
	// The press is acknowledged immediately and executed by the async worker.
	select {
	case forward := <-got:
		if !forward {
			t.Fatalf("spotify skip called with forward=false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("spotify skip never executed by the worker")
	}
}

// Rapid presses must be acknowledged instantly even when go-librespot holds its
// reply while the next track loads: the old synchronous path blocked ~5 s per
// press and stacked presses into a serial wait (live Portable, 2026-07-31).
func TestTransportSkipDoesNotBlockOnSlowEngine(t *testing.T) {
	calls := make(chan bool, 8)
	s := &Server{
		queue:            newPlayQueue(),
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		spotifyStreaming: func() bool { return true },
		spotifySkip: func(ctx context.Context, forward bool) error {
			calls <- forward
			<-ctx.Done() // engine "holds the reply" until the caller's deadline
			return ctx.Err()
		},
	}
	start := time.Now()
	for i := 0; i < 4; i++ {
		if _, err := s.transportSkip(context.Background(), true); err != nil {
			t.Fatalf("press %d: unexpected error: %v", i+1, err)
		}
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("four presses took %v, want immediate acknowledgement", elapsed)
	}
	// The worker drains the queue: the first press executes, and the capped
	// queue holds the rest. At least two skips must reach the engine.
	deadline := time.After(8 * time.Second)
	for n := 0; n < 2; n++ {
		select {
		case <-calls:
		case <-deadline:
			t.Fatalf("only %d skip(s) reached the engine, want at least 2", n)
		}
	}
}

func TestTransportSkipFallsBackToQueueWhenSpotifyIdle(t *testing.T) {
	spotifyCalled := false
	s := &Server{
		queue:            newPlayQueue(), // inactive -> queueSkip is a graceful no-op
		spotifyStreaming: func() bool { return false },
		spotifySkip: func(_ context.Context, _ bool) error {
			spotifyCalled = true
			return nil
		},
	}
	src, err := s.transportSkip(context.Background(), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src != "queue" {
		t.Fatalf("source = %q, want queue", src)
	}
	if spotifyCalled {
		t.Fatalf("spotify skip must not be called when Spotify is not streaming")
	}
}

func TestTransportSkipQueueWhenSpotifyUnconfigured(t *testing.T) {
	s := &Server{queue: newPlayQueue()} // no Spotify hooks wired at all
	src, err := s.transportSkip(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src != "queue" {
		t.Fatalf("source = %q, want queue", src)
	}
}
