package webui

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/upnp"
)

// After an app (soft) skip the box keeps ~10 s of the old track in its own
// buffer (the realtime-pacing lead), so the new track only became audible once
// that had played out; the press read as dead (live ST30 2026-08-19). The skip
// worker now re-pushes the same stream so the box drops that buffer. These
// tests pin the gating: exactly one re-push per press burst, none while
// paused, none on top of the hardware-skip flow's own re-attach.

func newReattachServer(streaming bool) *Server {
	return &Server{
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		renderer:         &upnp.Renderer{},
		spotifyStreaming: func() bool { return streaming },
	}
}

func (s *Server) skipRepushArmedAt() time.Time {
	s.skipRepushMu.Lock()
	defer s.skipRepushMu.Unlock()
	return s.lastSkipRepushAt
}

func TestSoftSkipReattachArmsOncePerBurst(t *testing.T) {
	s := newReattachServer(true)
	s.reattachAfterSoftSkip()
	first := s.skipRepushArmedAt()
	if first.IsZero() {
		t.Fatal("a soft skip on a streaming box must arm the re-push")
	}
	s.reattachAfterSoftSkip() // second drain moments later: debounced
	if got := s.skipRepushArmedAt(); !got.Equal(first) {
		t.Fatal("a second drain within the debounce window must not re-push again")
	}
}

func TestSoftSkipReattachSkipsPausedPlayback(t *testing.T) {
	s := newReattachServer(false) // paused/detached: the Ogg sink is gone
	s.reattachAfterSoftSkip()
	if !s.skipRepushArmedAt().IsZero() {
		t.Fatal("a skip on a paused playlist must not re-push (the push would force Play)")
	}
}

func TestSoftSkipReattachStandsDownForHardwareSkip(t *testing.T) {
	s := newReattachServer(true)
	s.skipRepushMu.Lock()
	s.hwSkipAt = time.Now() // the hardware flow re-attaches the box itself
	s.skipRepushMu.Unlock()
	s.reattachAfterSoftSkip()
	if !s.skipRepushArmedAt().IsZero() {
		t.Fatal("the soft re-push must stand down while the hardware-skip flow re-attaches")
	}
}

func TestSoftSkipReattachNoRendererNoPanic(t *testing.T) {
	s := &Server{
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		spotifyStreaming: func() bool { return true },
	}
	s.reattachAfterSoftSkip() // renderer nil (tests, degraded startup): no-op
	if !s.skipRepushArmedAt().IsZero() {
		t.Fatal("without a renderer there is nothing to push")
	}
}
