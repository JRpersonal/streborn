package webui

import (
	"context"
	"testing"
)

// The phone remote's Previous/Next controls call transportSkip, which must skip
// Spotify when it is the live source (the box cannot skip a UPnP source itself)
// and otherwise advance the STR play queue.

func TestTransportSkipRoutesToSpotifyWhenStreaming(t *testing.T) {
	var gotForward *bool
	s := &Server{
		queue:            newPlayQueue(),
		spotifyStreaming: func() bool { return true },
		spotifySkip: func(_ context.Context, forward bool) error {
			gotForward = &forward
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
	if gotForward == nil || *gotForward != true {
		t.Fatalf("spotify skip not called with forward=true (got %v)", gotForward)
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
