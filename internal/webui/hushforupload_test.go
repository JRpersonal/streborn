package webui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
)

// stopCounter records stop calls so a test can tell "we silenced the speaker"
// from "we left it alone". Installed over the hushStop seam, because the
// renderer field is a concrete type with no interface to fake.
type stopCounter struct {
	mu    sync.Mutex
	stops int
	err   error
}

func (r *stopCounter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stops
}

func withStopSeam(t *testing.T, r *stopCounter) {
	t.Helper()
	prev := hushStop
	hushStop = func(context.Context, *Server) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.stops++
		return r.err
	}
	t.Cleanup(func() { hushStop = prev })
}

// withNowPlaying installs the read seam. The real one reaches :8090 on a fixed
// port, so without this every case would take the "unreadable, leave it alone"
// branch and prove nothing.
func withNowPlaying(t *testing.T, np nowPlayingSnapshot) {
	t.Helper()
	prev := hushReadNowPlaying
	hushReadNowPlaying = func(context.Context, string) nowPlayingSnapshot { return np }
	t.Cleanup(func() { hushReadNowPlaying = prev })
}

func newHushServer() *Server {
	return &Server{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		boxHost: "192.0.2.10",
	}
}

// The reported case: the speaker is playing STR's own stream and a 16 MB engine
// upload is about to start. It must go quiet first.
func TestPlaybackIsStoppedBeforeAnUpload(t *testing.T) {
	withNowPlaying(t, nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PLAY_STATE"})
	r := &stopCounter{}
	withStopSeam(t, r)
	s := newHushServer()

	s.hushForUpload("sidecar")

	if got := r.count(); got != 1 {
		t.Errorf("Stop calls = %d, want 1", got)
	}
	// Without the latch the re-push machinery reads the stop as a firmware drop
	// and puts the stream straight back on, which is the fight being ended here.
	if !s.userStoppedRecently() {
		t.Error("the stop was not latched as deliberate, so the re-push will undo it")
	}
}

// A speaker playing Bluetooth is not ours to silence. The transfer disturbs it
// too, but reaching in is the bigger intrusion.
func TestASourceTheSpeakerOwnsIsLeftAlone(t *testing.T) {
	for _, src := range []string{"BLUETOOTH", "AUX", "SPOTIFY"} {
		t.Run(src, func(t *testing.T) {
			withNowPlaying(t, nowPlayingSnapshot{Source: src, PlayStatus: "PLAY_STATE"})
			r := &stopCounter{}
			withStopSeam(t, r)
			s := newHushServer()

			s.hushForUpload("agent-update")

			if got := r.count(); got != 0 {
				t.Errorf("a %s source was stopped %d time(s)", src, got)
			}
			if s.userStoppedRecently() {
				t.Errorf("a %s source was latched as a user stop", src)
			}
		})
	}
}

// Nothing playing, nothing to do. Sending a stop anyway would be a needless
// write to a speaker that may be counting down to deep standby.
func TestASilentSpeakerIsNotTouched(t *testing.T) {
	cases := []nowPlayingSnapshot{
		{Source: "STANDBY", PlayStatus: "STOP_STATE"},
		{Source: "INVALID_SOURCE"},
		{Source: "UPNP", PlayStatus: "STOP_STATE"},
		{Source: "UPNP", PlayStatus: "PAUSE_STATE"},
	}
	for _, np := range cases {
		withNowPlaying(t, np)
		r := &stopCounter{}
		withStopSeam(t, r)
		s := newHushServer()

		s.hushForUpload("sidecar")

		if got := r.count(); got != 0 {
			t.Errorf("source=%q status=%q was stopped %d time(s)", np.Source, np.PlayStatus, got)
		}
	}
}

// A speaker that will not say what it is playing gets left alone: stopping
// blind could silence a source STR does not own.
func TestAnUnreadableSpeakerIsLeftAlone(t *testing.T) {
	withNowPlaying(t, nowPlayingSnapshot{})
	r := &stopCounter{}
	withStopSeam(t, r)
	s := newHushServer()

	s.hushForUpload("sidecar")

	if got := r.count(); got != 0 {
		t.Errorf("an unreadable speaker was stopped %d time(s)", got)
	}
}

// The update is what the user is waiting for. A speaker that refuses the stop
// must not hold it up, and must not leave the code path in a broken state.
func TestAFailedStopDoesNotBlockTheUpload(t *testing.T) {
	withNowPlaying(t, nowPlayingSnapshot{Source: "UPNP", PlayStatus: "BUFFERING_STATE"})
	r := &stopCounter{err: errors.New("box refused")}
	withStopSeam(t, r)
	s := newHushServer()

	s.hushForUpload("agent-update") // must simply return

	if got := r.count(); got != 1 {
		t.Errorf("Stop attempts = %d, want 1", got)
	}
}

// The seam must not be the only thing being tested. Without it installed the
// real read runs, cannot reach a firmware port from a unit test, and the
// function must fall through to "leave it alone" rather than stopping blind.
func TestWithoutTheSeamsNothingIsStopped(t *testing.T) {
	r := &stopCounter{}
	withStopSeam(t, r) // installed on purpose: it is the WITNESS, not the subject
	s := newHushServer()

	// hushReadNowPlaying is deliberately NOT installed, so the real read runs.
	// It cannot reach a firmware port from a unit test, and the function must
	// then fall through to "leave it alone" rather than stopping blind.
	s.hushForUpload("sidecar")

	if got := r.count(); got != 0 {
		t.Errorf("with no reachable speaker the code stopped playback %d time(s)", got)
	}
}
