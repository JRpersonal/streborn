package webui

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func refusalTestServer() *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func refusalStrikes(s *Server) int {
	s.refusal.mu.Lock()
	defer s.refusal.mu.Unlock()
	return s.refusal.strikes
}

// #1634: an ST10 answered every library track's push with success, never
// fetched it, never logged a playback failure, and the app called each one a
// file in the wrong format. A drop without a failure line is a refusal; a drop
// WITH one is the file, and an unread log decides nothing.
func TestSingleTrackDropCountsAsRefusalOnlyWhenTheSpeakerNeverTriedIt(t *testing.T) {
	start := time.Now().Add(-30 * time.Second)
	cases := []struct {
		name    string
		fn      func(time.Duration) (bool, bool)
		login   bool
		want    string
		strikes int
	}{
		{"log read, no playback failure: refused", func(time.Duration) (bool, bool) { return false, true }, false, "track", 1},
		{"the speaker tried and failed: the file", func(time.Duration) (bool, bool) { return true, true }, false, "", 0},
		{"log not read: undecided", func(time.Duration) (bool, bool) { return false, false }, false, "", 0},
		{"no log reader at all: undecided", nil, false, "", 0},
		{"a 1036 during the play: refused as not signed in", func(time.Duration) (bool, bool) { return true, true }, true, "login", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := refusalTestServer()
			s.boxPlayFailureFn = c.fn
			if c.login {
				s.NoteBoxLoginError()
			}
			if got := s.noteSingleTrackRefused("01 - Track", start); got != c.want {
				t.Fatalf("kind = %q, want %q", got, c.want)
			}
			if got := refusalStrikes(s); got != c.strikes {
				t.Fatalf("strikes = %d, want %d", got, c.strikes)
			}
		})
	}
}

// The look-back covers the whole attempt, from the first push on: a playback
// failure older than that belongs to something else and must not excuse this
// drop.
func TestSingleTrackRefusalLooksBackOverTheWholeAttempt(t *testing.T) {
	s := refusalTestServer()
	var asked time.Duration
	s.boxPlayFailureFn = func(d time.Duration) (bool, bool) { asked = d; return false, true }
	s.noteSingleTrackRefused("01 - Track", time.Now().Add(-40*time.Second))
	if asked < 40*time.Second || asked > 40*time.Second+2*singleTrackRefusalSlack {
		t.Fatalf("looked back %v, want the attempt's 40 s plus the slack", asked)
	}
}

// Two refused tracks in a row raise the restart hint the app shows as its
// banner, the same latch two refused preset recalls raise.
func TestTwoRefusedLibraryTracksRaiseTheRestartHint(t *testing.T) {
	s := refusalTestServer()
	s.boxPlayFailureFn = func(time.Duration) (bool, bool) { return false, true }
	start := time.Now().Add(-30 * time.Second)
	s.noteSingleTrackRefused("01 - First", start)
	if active, _ := s.RecallRefusal(); active {
		t.Fatal("one refused track already raised the hint; a single odd failure must stay quiet")
	}
	s.noteSingleTrackRefused("02 - Second", start)
	if active, _ := s.RecallRefusal(); !active {
		t.Fatal("two refused tracks did not raise the restart hint")
	}
	s.NoteBoxHealthy()
	if active, _ := s.RecallRefusal(); active {
		t.Fatal("a track that played did not clear the hint")
	}
}

// End to end through the watch: a track the speaker never started, handed
// over once more and still not started, ends the watch with a strike when the
// speaker's log shows it never tried the file.
func TestATrackTheSpeakerNeverTriedCountsTowardTheRestartHint(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	s.boxPlayFailureFn = func(time.Duration) (bool, bool) { return false, true }
	const url = "http://192.0.2.9:49200/AUDIO/DLNA-1-0/01%20-%20Track.mp3"
	gen := s.setLastPlay(url, "01 - Track", "", "audio/mpeg")
	np.set("", 0, 0) // the speaker never took the stream
	s.armSingleTrackEnd(singleTrack{boxURL: url, title: "01 - Track", mime: "audio/mpeg", dur: 4 * time.Minute}, gen)

	waitForTrackEnd(t, "the second start", func() bool { return rec.countOf("SetAVTransportURI") >= 1 })
	waitForTrackEnd(t, "the refusal strike", func() bool { return refusalStrikes(s) == 1 })
}

// The same watch on a speaker that tried the file and logged why it failed
// records nothing: that is a file problem, and a restart would not help.
func TestATrackTheSpeakerTriedAndFailedIsNoRefusal(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	s.boxPlayFailureFn = func(time.Duration) (bool, bool) { return true, true }
	const url = "http://192.0.2.9:49200/AUDIO/DLNA-1-0/hires.flac"
	gen := s.setLastPlay(url, "Hi-res", "", "audio/x-flac")
	np.set("", 0, 0)
	s.armSingleTrackEnd(singleTrack{boxURL: url, title: "Hi-res", mime: "audio/x-flac", dur: 4 * time.Minute}, gen)

	waitForTrackEnd(t, "the second start", func() bool { return rec.countOf("SetAVTransportURI") >= 1 })
	time.Sleep(s.queueTiming.stallTimeout + 3*s.queueTiming.pollInterval)
	if n := refusalStrikes(s); n != 0 {
		t.Fatalf("recorded %d refusal strikes for a file the speaker tried and could not play", n)
	}
}
