package webui

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// #844: a 2:07 track was still shown playing six and a half minutes later. The
// box finishes a finite file and freezes in PLAY_STATE instead of reporting
// STOP, and a lone track, unlike a folder, had nothing watching for that.

type fakeNowPlaying struct {
	mu      sync.Mutex
	status  string
	pos     time.Duration
	total   time.Duration
	standby bool
	polls   int
}

func (f *fakeNowPlaying) set(status string, pos, total time.Duration) {
	f.mu.Lock()
	f.status, f.pos, f.total = status, pos, total
	f.mu.Unlock()
}

func singleTrackServer(t *testing.T, np *fakeNowPlaying) (*Server, *soapRecorder) {
	t.Helper()
	s, rec := newPlayTestServer(t)
	s.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	s.nowPlayingFn = func() (string, time.Duration, time.Duration, bool) {
		np.mu.Lock()
		defer np.mu.Unlock()
		np.polls++
		return np.status, np.pos, np.total, np.standby
	}
	return s, rec
}

func waitForTrackEnd(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

func TestALoneTrackFrozenAtItsEndIsStopped(t *testing.T) {
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	gen := s.setLastPlay("http://192.0.2.9/28082.flac", "Photographs And Memories", "", "audio/x-flac")

	// A short track that the box plays and then freezes on, exactly the shape
	// in the reporter's bundle.
	np.set("PLAY_STATE", 2*time.Second, 5*time.Second)
	s.armSingleTrackEnd(5*time.Second, gen, "Photographs And Memories")
	time.Sleep(6 * time.Second)
	np.set("PLAY_STATE", 5*time.Second, 5*time.Second) // frozen at the end

	waitForTrackEnd(t, "the box to be stopped at the end of the track", func() bool { return rec.has("Stop") })
}

func TestANewerPlayCallsOffTheEndWatch(t *testing.T) {
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	gen := s.setLastPlay("http://192.0.2.9/a.flac", "First", "", "audio/x-flac")
	np.set("PLAY_STATE", 1*time.Second, 3*time.Second)
	s.armSingleTrackEnd(3*time.Second, gen, "First")

	// The user starts something else before the first track would have ended.
	s.setLastPlay("http://192.0.2.9/b.flac", "Second", "", "audio/x-flac")

	time.Sleep(14 * time.Second)
	if rec.has("Stop") {
		t.Fatal("the stale watch stopped the box, which would cut off the track the user just started")
	}
}

func TestAPoweredOffBoxIsLeftAlone(t *testing.T) {
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	gen := s.setLastPlay("http://192.0.2.9/a.flac", "First", "", "audio/x-flac")
	np.set("PLAY_STATE", 1*time.Second, 3*time.Second)
	np.mu.Lock()
	np.standby = true
	np.mu.Unlock()
	s.armSingleTrackEnd(3*time.Second, gen, "First")

	time.Sleep(14 * time.Second)
	if rec.has("Stop") {
		t.Fatal("STR sent a command to a sleeping speaker")
	}
	_ = context.Background()
}
