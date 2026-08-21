package webui

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/upnp"
)

// reattachAfterRecall drops the box's buffer after a preset recall ONLY when
// stale pre-boundary audio actually reached the fresh attachment: with the
// recall cut doing its job the common case must be a no-op (a needless push
// flaps a stream that is already playing the right track). These tests pin
// that gating; the debounce stamp (lastSkipRepushAt) is the observable "a
// push was decided", exactly as in the soft-skip re-attach tests.

func TestReattachAfterRecall(t *testing.T) {
	disc := slog.New(slog.NewTextHandler(io.Discard, nil))

	cases := []struct {
		name string
		// boundaryLands: the engine stamped a boundary after armedAt. False
		// simulates a load that never delivered (the wait must time out).
		boundaryLands bool
		nilBoundary   bool // no boundary signal wired at all
		staleKB       int64
		userStop      bool // deliberate stop after the recall started
		loginErr      bool // box answered 1036 (re-login in flight)
		supersede     bool // a newer play bumped the recall generation
		priorPush     bool // a re-push happened 2s ago (shared burst debounce)
		wantPush      bool
	}{
		{name: "stale audio reached the box: one push", boundaryLands: true, staleKB: 300, wantPush: true},
		{name: "clean boundary handover: no push", boundaryLands: true, staleKB: 12, wantPush: false},
		{name: "no boundary within the wait: keep the buffered stream", boundaryLands: false, staleKB: 300, wantPush: false},
		{name: "user stop stands the push down", boundaryLands: true, staleKB: 300, userStop: true, wantPush: false},
		{name: "not-logged-in (1036) stands the push down", boundaryLands: true, staleKB: 300, loginErr: true, wantPush: false},
		{name: "newer play supersedes", boundaryLands: true, staleKB: 300, supersede: true, wantPush: false},
		{name: "shared burst debounce: no second push", boundaryLands: true, staleKB: 300, priorPush: true, wantPush: false},
		{name: "no boundary signal wired: no push", nilBoundary: true, staleKB: 300, wantPush: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{logger: disc, renderer: &upnp.Renderer{}}
			s.recallReattachWait = 300 * time.Millisecond // test seam: shrink the 12s budget
			started := time.Now()
			armedAt := started
			if !tc.nilBoundary {
				boundary := armedAt.Add(-time.Hour) // never crossed
				if tc.boundaryLands {
					boundary = armedAt.Add(time.Millisecond)
				}
				s.spotifySkipBoundary = func() time.Time { return boundary }
			}
			s.spotifyBoundaryStaleKB = func() int64 { return tc.staleKB }
			// A non-spotify lastPlay keeps repushSpotifyStream itself a no-op,
			// so the assertions see the pure push decision (like the soft-skip
			// tests, which never exercise the UPnP call either).
			gen := s.setLastPlay("http://127.0.0.1:8888/stream/4", "A", "", "")
			if tc.supersede {
				s.setLastPlay("http://127.0.0.1:8888/stream/5", "B", "", "")
			}
			if tc.userStop {
				s.lastUserStopMu.Lock()
				s.lastUserStop = time.Now()
				s.lastUserStopMu.Unlock()
			}
			if tc.loginErr {
				s.loginErr.mu.Lock()
				s.loginErr.last = time.Now()
				s.loginErr.mu.Unlock()
			}
			var prior time.Time
			if tc.priorPush {
				prior = time.Now().Add(-2 * time.Second)
				s.skipRepushMu.Lock()
				s.lastSkipRepushAt = prior
				s.skipRepushMu.Unlock()
			}

			s.reattachAfterRecall(gen, started, 4, armedAt)

			stamped := s.skipRepushArmedAt()
			switch {
			case tc.wantPush && (stamped.IsZero() || stamped.Equal(prior)):
				t.Fatal("expected a re-push (stale audio reached the box), none was armed")
			case !tc.wantPush && tc.priorPush && !stamped.Equal(prior):
				t.Fatal("the shared burst debounce must keep a second push from arming")
			case !tc.wantPush && !tc.priorPush && !stamped.IsZero():
				t.Fatal("expected NO re-push, but one was armed")
			}
		})
	}
}

// A recall re-push with no renderer (tests, degraded startup) must be a no-op,
// not a panic - mirroring the soft-skip guard.
func TestReattachAfterRecallNoRendererNoPanic(t *testing.T) {
	s := &Server{
		logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		spotifySkipBoundary: func() time.Time { return time.Now() },
	}
	s.reattachAfterRecall(1, time.Now(), 4, time.Now())
	if !s.skipRepushArmedAt().IsZero() {
		t.Fatal("without a renderer there is nothing to push")
	}
}
