package webui

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

// singleTrackServer runs the end watch at the fast test clock (fastQueueTiming):
// the scenarios below are written in qsec, one box second at that clock.
func singleTrackServer(t *testing.T, np *fakeNowPlaying) (*Server, *soapRecorder) {
	t.Helper()
	s, rec := newPlayTestServer(t)
	s.queueTiming = fastQueueTiming()
	// The end watch runs under the server's base context; cancelling it at the
	// end of the test sends a watch that nothing superseded away instead of
	// leaving it ticking for the rest of the test binary.
	ctx, cancel := context.WithCancel(context.Background())
	s.baseCtx = ctx
	t.Cleanup(cancel)
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
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

func TestALoneTrackFrozenAtItsEndIsStopped(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	gen := s.setLastPlay("http://192.0.2.9/28082.flac", "Photographs And Memories", "", "audio/x-flac")

	// A short track that the box plays and then freezes on, exactly the shape
	// in the reporter's bundle.
	np.set("PLAY_STATE", 2*qsec, 5*qsec)
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9/28082.flac", title: "Photographs And Memories", mime: "audio/x-flac", dur: 5 * qsec}, gen)
	time.Sleep(betweenPolls(1))          // the watch has seen it playing
	np.set("PLAY_STATE", 5*qsec, 5*qsec) // frozen at the end

	waitForTrackEnd(t, "the box to be stopped at the end of the track", func() bool { return rec.has("Stop") })
}

func TestANewerPlayCallsOffTheEndWatch(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	gen := s.setLastPlay("http://192.0.2.9/a.flac", "First", "", "audio/x-flac")
	np.set("PLAY_STATE", 1*qsec, 3*qsec)
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9/a.flac", title: "First", mime: "audio/x-flac", dur: 3 * qsec}, gen)

	// The user starts something else before the first track would have ended.
	s.setLastPlay("http://192.0.2.9/b.flac", "Second", "", "audio/x-flac")

	// Well past the track's length plus the margin: a stale watch would have
	// stopped the box by now.
	time.Sleep(14 * qsec)
	if rec.has("Stop") {
		t.Fatal("the stale watch stopped the box, which would cut off the track the user just started")
	}
}

func TestAPoweredOffBoxIsLeftAlone(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	gen := s.setLastPlay("http://192.0.2.9/a.flac", "First", "", "audio/x-flac")
	np.set("PLAY_STATE", 1*qsec, 3*qsec)
	np.mu.Lock()
	np.standby = true
	np.mu.Unlock()
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9/a.flac", title: "First", mime: "audio/x-flac", dur: 3 * qsec}, gen)

	time.Sleep(14 * qsec)
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
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	withPlayMode(t, s, "shuffle=0 repeat=one\n")
	gen := s.setLastPlay("http://192.0.2.9/what-i-deserve.m4a", "What I Deserve", "", "audio/mp4")

	np.set("PLAY_STATE", 2*qsec, 5*qsec)
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9/what-i-deserve.m4a", title: "What I Deserve", mime: "audio/mp4", dur: 5 * qsec}, gen)
	time.Sleep(betweenPolls(1))
	np.set("PLAY_STATE", 5*qsec, 5*qsec) // frozen at the end

	// The replay is complete once the push went out AND it was registered as a
	// play (the push lands a moment before the registration).
	waitForTrackEnd(t, "the track to be played again", func() bool {
		return rec.countOf("SetAVTransportURI") >= 1 && s.RecallGeneration() != gen
	})
	if rec.has("Stop") {
		t.Fatalf("repeat one stopped the box instead of playing the track again: %v", rec.list())
	}
	if s.RecallGeneration() == gen {
		t.Fatal("the replay did not register as a new play, so a newer play could not supersede its watch")
	}
}

func TestRepeatOneAlsoCoversABoxThatStoppedByItself(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	withPlayMode(t, s, "shuffle=0 repeat=one\n")
	gen := s.setLastPlay("http://192.0.2.9/a.flac", "First", "", "audio/x-flac")

	np.set("PLAY_STATE", 1*qsec, 3*qsec)
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9/a.flac", title: "First", mime: "audio/x-flac", dur: 3 * qsec}, gen)
	// Past the first poll, not on it: a sleep of exactly one poll interval
	// raced the ticker, and a watch whose first poll already saw STOP never saw
	// playback and waited forever.
	time.Sleep(betweenPolls(1))
	np.set("STOP_STATE", 3*qsec, 3*qsec)

	waitForTrackEnd(t, "the track to be played again after the box stopped", func() bool { return rec.countOf("SetAVTransportURI") >= 1 })
	if s.userStoppedRecently() {
		t.Fatal("the replay left the user-stop latch set, which suppresses the auto re-push of the new play")
	}
}

// A user with the toggle on "all" started one track and it stopped at its end
// with the repeat button lit (mail, 2026-10-28). A lone track is a list of one,
// so "all" plays it again too.
func TestRepeatAllAlsoPlaysALoneTrackAgain(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	withPlayMode(t, s, "shuffle=0 repeat=all\n")
	gen := s.setLastPlay("http://192.0.2.9/all-for-love.mp3", "All for love", "", "audio/mpeg")

	np.set("PLAY_STATE", 2*qsec, 5*qsec)
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9/all-for-love.mp3", title: "All for love", mime: "audio/mpeg", dur: 5 * qsec}, gen)
	time.Sleep(betweenPolls(1))
	np.set("PLAY_STATE", 5*qsec, 5*qsec)

	waitForTrackEnd(t, "the track to be played again", func() bool {
		return rec.countOf("SetAVTransportURI") >= 1 && s.RecallGeneration() != gen
	})
	if rec.has("Stop") {
		t.Fatalf("repeat all stopped the box instead of playing the track again: %v", rec.list())
	}
}

func TestRepeatOffStopsALoneTrackAtItsEnd(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	withPlayMode(t, s, "shuffle=0 repeat=off\n")
	gen := s.setLastPlay("http://192.0.2.9/28082.flac", "Photographs And Memories", "", "audio/x-flac")

	np.set("PLAY_STATE", 2*qsec, 5*qsec)
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9/28082.flac", title: "Photographs And Memories", mime: "audio/x-flac", dur: 5 * qsec}, gen)
	time.Sleep(betweenPolls(1))
	np.set("PLAY_STATE", 5*qsec, 5*qsec)

	waitForTrackEnd(t, "the box to be stopped", func() bool { return rec.has("Stop") })
	if rec.countOf("SetAVTransportURI") != 0 {
		t.Fatal("repeat off played the track again")
	}
}

// #1031: a single library track recalled from a preset key played to its end
// and then showed as playing until the auto-off timer. The preset recall path
// never armed the end watch the plain play has had since #844. A preset stores
// no length, so the watch has to take the one the speaker reports.
func TestALibraryTrackFromAPresetKeyIsStoppedAtItsEnd(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	seedLibraryTrackPreset(t, s, 3)

	np.set("PLAY_STATE", 2*qsec, 5*qsec)
	if !s.RecallSlot(context.Background(), 3) {
		t.Fatal("RecallSlot did not claim the library track key")
	}
	time.Sleep(betweenPolls(1))
	np.set("PLAY_STATE", 5*qsec, 5*qsec) // frozen at the end

	waitForTrackEnd(t, "the box to be stopped at the end of the preset's track", func() bool { return rec.has("Stop") })
}

// #1322: a Wave that took STR's wake and push but never started the stream,
// then dropped back to standby. The track is handed over once more, and only
// once: a speaker that refuses twice is left alone.
func TestATrackTheSpeakerNeverStartedIsHandedOverOnceMore(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	gen := s.setLastPlay("http://192.0.2.9/27025.flac", "Layla", "", "audio/x-flac")
	np.set("", 0, 0) // no usable status: the speaker never took the stream
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9/27025.flac", title: "Layla", mime: "audio/x-flac", dur: 4 * time.Minute}, gen)

	waitForTrackEnd(t, "the second start", func() bool { return rec.countOf("SetAVTransportURI") >= 1 })
	// Still silent after the retry: no third push.
	time.Sleep(s.queueTiming.stallTimeout + 2*s.queueTiming.pollInterval)
	if n := rec.countOf("SetAVTransportURI"); n != 1 {
		t.Fatalf("the track was handed over %d more times, want exactly one retry", n)
	}
}

// Mail, 2026-10-26: the following half of a stereo pair took the push and kept
// reporting the pair's other audio in PLAY_STATE, its position frozen at 120 s.
// The watch read that as our track, learned a 120 s length for a file that never
// played, and stopped the speaker. PLAY_STATE for another location is not ours.
func TestForeignAudioIsNeitherOurTrackNorItsLength(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	s.nowPlayingLocationFn = func() string { return "http://192.0.2.99:8085/other.mp3" }
	const mine = "http://192.0.2.20:49000/still.mp3"
	gen := s.setLastPlay(mine, "Still", "", "audio/mpeg")
	np.set("PLAY_STATE", 120*time.Second, 0)
	s.armSingleTrackEnd(singleTrack{boxURL: mine, title: "Still", mime: "audio/mpeg"}, gen)

	// Past the frozen net and past the stall timeout plus its one retry.
	time.Sleep(2*s.queueTiming.stallTimeout + 4*s.queueTiming.frozenTimeout)
	if rec.has("Stop") {
		t.Fatal("the watch stopped the speaker over audio it never started")
	}
	if got := s.trackLens.get(mine); got != 0 {
		t.Fatalf("learned length %v from audio that was not this file", got)
	}
}

// The same file under another host spelling is still ours, and still ended.
func TestOwnFileUnderAnotherHostIsStillWatched(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	s.nowPlayingLocationFn = func() string { return "http://127.0.0.1:49000/a.flac" }
	gen := s.setLastPlay("http://192.0.2.9:49000/a.flac", "First", "", "audio/x-flac")
	np.set("PLAY_STATE", 2*qsec, 5*qsec)
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9:49000/a.flac", title: "First", mime: "audio/x-flac", dur: 5 * qsec}, gen)
	time.Sleep(betweenPolls(1))
	np.set("PLAY_STATE", 5*qsec, 5*qsec)

	waitForTrackEnd(t, "the box to be stopped at the end of the track", func() bool { return rec.has("Stop") })
}

func TestForeignFileLocation(t *testing.T) {
	cases := []struct {
		loc, want string
		foreign   bool
	}{
		{"", "http://192.0.2.9/a.flac", false},
		{"http://192.0.2.9/a.flac", "http://192.0.2.9/a.flac", false},
		{"http://127.0.0.1/a.flac", "http://192.0.2.9/a.flac", false},
		{"http://192.0.2.9/b.flac", "http://192.0.2.9/a.flac", true},
		{"http://192.0.2.9/a.flac?x=2", "http://192.0.2.9/a.flac?x=1", true},
		{"http://127.0.0.1:8085/stream/3", "http://192.0.2.9/a.flac", true},
		// The same query escaped differently, or in another key order, is
		// still our file: a false "foreign" would re-push it mid-play.
		{"http://192.0.2.9/a.flac?t=a%20b&x=1", "http://192.0.2.9/a.flac?x=1&t=a+b", false},
		{"http://192.0.2.9/a%2Eflac?x=1", "http://192.0.2.9/a.flac?x=1", false},
		// Unparsable is unknown, never foreign.
		{"http://[::1", "http://192.0.2.9/a.flac", false},
		{"http://192.0.2.9/a.flac?x=%zz", "http://192.0.2.9/a.flac?x=1", false},
	}
	for _, c := range cases {
		if got := foreignFileLocation(c.loc, c.want); got != c.foreign {
			t.Errorf("foreignFileLocation(%q, %q) = %v, want %v", c.loc, c.want, got, c.foreign)
		}
	}
}

// A key pressed on the speaker after the play began is the listener choosing
// something else, and the retry must not overrule it.
func TestNoStartRetryAfterAKeyPress(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	pressed := time.Now().Add(time.Second)
	s.userActivityFn = func() time.Time { return pressed }
	gen := s.setLastPlay("http://192.0.2.9/27025.flac", "Layla", "", "audio/x-flac")
	np.set("", 0, 0)
	s.armSingleTrackEnd(singleTrack{boxURL: "http://192.0.2.9/27025.flac", title: "Layla", mime: "audio/x-flac", dur: 4 * time.Minute}, gen)

	time.Sleep(s.queueTiming.stallTimeout + 2*s.queueTiming.pollInterval)
	if n := rec.countOf("SetAVTransportURI"); n != 0 {
		t.Fatalf("the track was pushed again %d times after a key press", n)
	}
}

// The FRITZ!Box MP3 in the evidence: the box reported 120 s at most 16 s
// after the push and froze there. The frozen-position net then stopped the box
// while the track was playing, and 120 s was learned as the file's length.
func TestALoneTrackWhosePositionRunsAheadIsNotStoppedOrLearned(t *testing.T) {
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	const url = "http://192.0.2.9:49200/AUDIO/song.mp3"
	gen := s.setLastPlay(url, "Song", "", "audio/mpeg")
	np.set("PLAY_STATE", 120*qsec, 0)
	s.armSingleTrackEnd(singleTrack{boxURL: url, title: "Song", mime: "audio/mpeg"}, gen)

	time.Sleep(40 * qsec) // more than twice the freeze timeout
	if rec.has("Stop") {
		t.Fatal("the box was stopped on a position it cannot have played")
	}
	if got := s.trackLens.get(url); got != 0 {
		t.Fatalf("learned %v from a position that ran ahead of real time", got)
	}
}

func TestALengthIsNotLearnedFromAPositionThatRanAhead(t *testing.T) {
	t.Parallel()
	s, _ := newPlayTestServer(t)
	const url = "http://192.0.2.9:49200/AUDIO/song.mp3"
	s.learnSingleTrackLength(singleTrack{boxURL: url, title: "Song"}, endNetFrozen, 120*time.Second, true)
	if got := s.trackLens.get(url); got != 0 {
		t.Fatalf("learned %v from a position that ran ahead of real time", got)
	}
	s.learnSingleTrackLength(singleTrack{boxURL: url, title: "Song"}, endNetFrozen, 120*time.Second, false)
	if got := s.trackLens.get(url); got != 120*time.Second {
		t.Fatalf("a plausible freeze taught %v, want 120 s", got)
	}
}

// climb raises the scripted position one box second per box second, from
// `from` to `to`, in PLAY_STATE with no total (the FRITZ!Box shape).
func (f *fakeNowPlaying) climb(from, to time.Duration) {
	for p := from; p <= to; p += qsec {
		f.set("PLAY_STATE", p, 0)
		time.Sleep(qsec)
	}
}

// unknownLengthTrackServer starts the end watch on a lone MP3 the way the
// FRITZ!Box media server hands one over: no length from the server, no total
// from the speaker, and the position only starting to count a while after the
// push. Its log goes to the returned buffer.
func unknownLengthTrackServer(t *testing.T) (*Server, *soapRecorder, *fakeNowPlaying, *syncBuffer, string) {
	t.Helper()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	logs := &syncBuffer{}
	s.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	const url = "http://192.0.2.9:49200/AUDIO/DLNA-1-0/Musik/song.mp3"
	gen := s.setLastPlay(url, "Fly", "", "audio/mpeg")
	np.set("BUFFERING_STATE", 0, 0)
	s.armSingleTrackEnd(singleTrack{boxURL: url, title: "Fly", mime: "audio/mpeg"}, gen)
	time.Sleep(20 * qsec) // the start latency: live, the wall clock was well ahead of the position
	return s, rec, np, logs, url
}

// assertNotEndedNorLearned fails when the watch stopped the speaker or ran its
// length learning (which logs "from where it froze" whether or not it keeps
// the length: the test positions are below learnedLengthMin).
func assertNotEndedNorLearned(t *testing.T, s *Server, rec *soapRecorder, logs *syncBuffer, url, when string) {
	t.Helper()
	if rec.has("Stop") {
		t.Fatalf("%s: the speaker was stopped although the track was only paused and resumed\n%s", when, logs.String())
	}
	if got := s.trackLens.get(url); got != 0 {
		t.Fatalf("%s: learned a length of %v from where the track was paused", when, got)
	}
	if strings.Contains(logs.String(), "from where it froze") {
		t.Fatalf("%s: the length learning ran on a pause position\n%s", when, logs.String())
	}
}

// Live, ST10, 2026-11-03: a lone FRITZ!Box MP3 with no known length was paused
// with the speaker's own key at 25 s and resumed five seconds later. The resume
// reports PLAY_STATE with the position still on the paused second for a few
// seconds (the firmware re-opens the URL, FIRMWARE-NOTES.md "Play after
// Pause"). The frozen-position net counted the pause, and the still stretch
// before it, as frozen playback, "learned" a 25 s length and stopped the
// speaker 2 s after the resume.
func TestAShortPauseOfALoneTrackOfUnknownLengthIsNotItsEnd(t *testing.T) {
	t.Parallel()
	s, rec, np, logs, url := unknownLengthTrackServer(t)

	np.climb(1*qsec, 5*qsec)
	np.set("PLAY_STATE", 5*qsec, 0) // the position stood still before the key press, as in the live log
	time.Sleep(8 * qsec)
	np.set("PAUSE_STATE", 5*qsec, 0)
	time.Sleep(5 * qsec)
	np.set("PLAY_STATE", 5*qsec, 0) // resumed, the position not moving yet
	time.Sleep(2*s.queueTiming.pollInterval + 2*qsec)
	np.climb(6*qsec, 10*qsec)

	assertNotEndedNorLearned(t, s, rec, logs, url, "after a short pause")
}

// The same with a pause longer than the freeze timeout: the pause alone used to
// be enough for the frozen-position net on the first poll after the resume. A
// real freeze at the end of the file afterwards still ends the track.
func TestALongPauseOfALoneTrackOfUnknownLengthIsNotItsEnd(t *testing.T) {
	t.Parallel()
	s, rec, np, logs, url := unknownLengthTrackServer(t)

	np.climb(1*qsec, 5*qsec)
	np.set("PLAY_STATE", 5*qsec, 0) // a poll reads the second the pause will keep
	time.Sleep(6 * qsec)
	np.set("PAUSE_STATE", 5*qsec, 0)
	time.Sleep(2 * s.queueTiming.frozenTimeout)
	np.set("PLAY_STATE", 5*qsec, 0) // resumed, the position not moving yet
	time.Sleep(2*s.queueTiming.pollInterval + 2*qsec)
	np.climb(6*qsec, 10*qsec)

	assertNotEndedNorLearned(t, s, rec, logs, url, "after a long pause")

	np.set("PLAY_STATE", 10*qsec, 0) // frozen at the real end of the file
	waitForTrackEnd(t, "the box to be stopped at the real end after the resume", func() bool { return rec.has("Stop") })
}

// A resume that stays on the paused second for a whole freeze timeout is the
// firmware's frozen resume (#830), which a file without a length cannot be
// restarted out of. The speaker is stopped so nothing shows it playing, but it
// is no end of the track: no length is learned and repeat does not start it
// over.
func TestAResumeThatNeverMovesIsStoppedButNotTakenForTheEnd(t *testing.T) {
	t.Parallel()
	s, rec, np, logs, url := unknownLengthTrackServer(t)
	withPlayMode(t, s, "shuffle=0 repeat=one\n")

	np.climb(1*qsec, 5*qsec)
	np.set("PLAY_STATE", 5*qsec, 0)
	time.Sleep(6 * qsec)
	np.set("PAUSE_STATE", 5*qsec, 0)
	time.Sleep(5 * qsec)
	np.set("PLAY_STATE", 5*qsec, 0) // resumed, and it never moves again

	waitForTrackEnd(t, "the frozen resume to be stopped", func() bool { return rec.has("Stop") })
	// The line is written once the speaker took the Stop, a moment after it.
	waitForTrackEnd(t, "the stop to be logged as a frozen resume", func() bool {
		return strings.Contains(logs.String(), "the resume never moved off the paused position")
	})
	if n := rec.countOf("SetAVTransportURI"); n != 0 {
		t.Fatalf("repeat played the track again %d times after a frozen resume", n)
	}
	if got := s.trackLens.get(url); got != 0 {
		t.Fatalf("learned a length of %v from where the track was paused", got)
	}
	if strings.Contains(logs.String(), "from where it froze") {
		t.Fatalf("the length learning ran on a pause position\n%s", logs.String())
	}
}
