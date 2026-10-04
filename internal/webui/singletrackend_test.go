package webui

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9/28082.flac", title: "Photographs And Memories", mime: "audio/x-flac", dur: 5 * time.Second}, gen)
	time.Sleep(6 * time.Second)
	np.set("PLAY_STATE", 5*time.Second, 5*time.Second) // frozen at the end

	waitForTrackEnd(t, "the box to be stopped at the end of the track", func() bool { return rec.has("Stop") })
}

func TestANewerPlayCallsOffTheEndWatch(t *testing.T) {
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	gen := s.setLastPlay("http://192.0.2.9/a.flac", "First", "", "audio/x-flac")
	np.set("PLAY_STATE", 1*time.Second, 3*time.Second)
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9/a.flac", title: "First", mime: "audio/x-flac", dur: 3 * time.Second}, gen)

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
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9/a.flac", title: "First", mime: "audio/x-flac", dur: 3 * time.Second}, gen)

	time.Sleep(14 * time.Second)
	if rec.has("Stop") {
		t.Fatal("STR sent a command to a sleeping speaker")
	}
	_ = context.Background()
}

// #1065, Issue 5: "repeat one" chosen on the Library screen, then a single track
// clicked. The track played once, the SoundTouch light went amber, and nothing
// played it again: the end watch stopped the box without asking the sticky
// repeat mode.

func withPlayMode(t *testing.T, s *Server, body string) {
	t.Helper()
	s.playModePath = filepath.Join(t.TempDir(), "play-mode")
	if err := os.WriteFile(s.playModePath, []byte(body), 0o644); err != nil {
		t.Fatalf("write play-mode: %v", err)
	}
}

func (rec *soapRecorder) countOf(action string) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	n := 0
	for _, a := range rec.actions {
		if a == action {
			n++
		}
	}
	return n
}

func TestRepeatOnePlaysALoneTrackAgainInsteadOfStopping(t *testing.T) {
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	withPlayMode(t, s, "shuffle=0 repeat=one\n")
	gen := s.setLastPlay("http://192.0.2.9/what-i-deserve.m4a", "What I Deserve", "", "audio/mp4")

	np.set("PLAY_STATE", 2*time.Second, 5*time.Second)
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9/what-i-deserve.m4a", title: "What I Deserve", mime: "audio/mp4", dur: 5 * time.Second}, gen)
	time.Sleep(6 * time.Second)
	np.set("PLAY_STATE", 5*time.Second, 5*time.Second) // frozen at the end

	waitForTrackEnd(t, "the track to be played again", func() bool { return rec.countOf("SetAVTransportURI") >= 1 })
	if rec.has("Stop") {
		t.Fatalf("repeat one stopped the box instead of playing the track again: %v", rec.list())
	}
	if s.RecallGeneration() == gen {
		t.Fatal("the replay did not register as a new play, so a newer play could not supersede its watch")
	}
}

func TestRepeatOneAlsoCoversABoxThatStoppedByItself(t *testing.T) {
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	withPlayMode(t, s, "shuffle=0 repeat=one\n")
	gen := s.setLastPlay("http://192.0.2.9/a.flac", "First", "", "audio/x-flac")

	np.set("PLAY_STATE", 1*time.Second, 3*time.Second)
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9/a.flac", title: "First", mime: "audio/x-flac", dur: 3 * time.Second}, gen)
	time.Sleep(4 * time.Second)
	np.set("STOP_STATE", 3*time.Second, 3*time.Second)

	waitForTrackEnd(t, "the track to be played again after the box stopped", func() bool { return rec.countOf("SetAVTransportURI") >= 1 })
	if s.userStoppedRecently() {
		t.Fatal("the replay left the user-stop latch set, which suppresses the auto re-push of the new play")
	}
}

func TestRepeatAllLeavesALoneTrackAlone(t *testing.T) {
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	withPlayMode(t, s, "shuffle=0 repeat=all\n")
	gen := s.setLastPlay("http://192.0.2.9/28082.flac", "Photographs And Memories", "", "audio/x-flac")

	np.set("PLAY_STATE", 2*time.Second, 5*time.Second)
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9/28082.flac", title: "Photographs And Memories", mime: "audio/x-flac", dur: 5 * time.Second}, gen)
	time.Sleep(6 * time.Second)
	np.set("PLAY_STATE", 5*time.Second, 5*time.Second)

	waitForTrackEnd(t, "the box to be stopped", func() bool { return rec.has("Stop") })
	if rec.countOf("SetAVTransportURI") != 0 {
		t.Fatal("repeat all looped a single click; it is the folder mode and must not")
	}
}
