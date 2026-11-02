package webui

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// The remote's Back on a Spotify playlist goes through RemotePrev (#1678), the
// app's Back keeps the engine's plain prev, and Next stays a plain skip on both.
func TestRemoteBackTakesRemotePrevAndTheAppDoesNot(t *testing.T) {
	type call struct {
		remotePrev bool
		forward    bool
	}
	calls := make(chan call, 8)
	s := &Server{
		queue:            newPlayQueue(),
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		spotifyStreaming: func() bool { return true },
		spotifySkip: func(_ context.Context, forward bool) error {
			calls <- call{forward: forward}
			return nil
		},
		spotifyRemotePrev: func(context.Context) (bool, error) {
			calls <- call{remotePrev: true}
			return true, nil
		},
	}
	next := func() call {
		t.Helper()
		select {
		case c := <-calls:
			return c
		case <-time.After(3 * time.Second):
			t.Fatal("the skip never reached the engine")
		}
		return call{}
	}
	// The remote (no Spotify key slot: app-driven playback) presses Back.
	if _, err := s.HardwareSkip(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if c := next(); !c.remotePrev {
		t.Fatalf("the remote's Back took the plain prev: %+v", c)
	}
	// The app presses Back.
	if _, err := s.transportSkip(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if c := next(); c.remotePrev || c.forward {
		t.Fatalf("the app's Back took %+v, want the plain prev", c)
	}
	// The remote presses Next.
	if _, err := s.HardwareSkip(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if c := next(); c.remotePrev || !c.forward {
		t.Fatalf("the remote's Next took %+v, want the plain next", c)
	}
}
