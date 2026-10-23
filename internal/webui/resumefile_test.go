package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/upnp"
)

// #830, live on an ST10 (FW 27.0.6, 2026-10-22): a directly played MP3 paused
// fine, but Play after the pause left the speaker in PLAYING with the position
// frozen at the pause point and no sound. Stop, Play and a Seek back to the
// paused position is what continued it.

// --- the freeze recogniser, step by step -----------------------------------

func obsAt(t0 time.Time, sec int, status string, pos time.Duration) fileResumeObs {
	return fileResumeObs{status: status, pos: pos, end: 240 * time.Second, now: t0.Add(time.Duration(sec) * time.Second)}
}

func TestAFrozenResumeAfterAPauseIsRepairedOnceAtThePausedPosition(t *testing.T) {
	var w fileResumeWatch
	t0 := time.Now()
	steps := []struct {
		obs  fileResumeObs
		want fileResumeStep
	}{
		{obsAt(t0, 0, "PLAY_STATE", 10*time.Second), fileResumeStep{}},
		{obsAt(t0, 4, "PLAY_STATE", 14*time.Second), fileResumeStep{}},
		{obsAt(t0, 8, "PAUSE_STATE", 15*time.Second), fileResumeStep{}},
		{obsAt(t0, 12, "PAUSE_STATE", 15*time.Second), fileResumeStep{}},
		// Resumed: PLAYING, the position does not move.
		{obsAt(t0, 16, "PLAY_STATE", 15*time.Second), fileResumeStep{hold: true}},
		{obsAt(t0, 20, "PLAY_STATE", 15*time.Second), fileResumeStep{repair: true, at: 15 * time.Second}},
		// Still frozen: the one repair this resume gets has been used.
		{obsAt(t0, 24, "PLAY_STATE", 15*time.Second), fileResumeStep{}},
		{obsAt(t0, 28, "PLAY_STATE", 15*time.Second), fileResumeStep{}},
	}
	for i, st := range steps {
		if got := w.observe(st.obs); got != st.want {
			t.Fatalf("step %d (%s pos %v): got %+v, want %+v", i, st.obs.status, st.obs.pos, got, st.want)
		}
	}
}

func TestAResumeThatContinuesIsLeftAlone(t *testing.T) {
	var w fileResumeWatch
	t0 := time.Now()
	for i, o := range []fileResumeObs{
		obsAt(t0, 0, "PLAY_STATE", 10*time.Second),
		obsAt(t0, 4, "PAUSE_STATE", 12*time.Second),
		obsAt(t0, 8, "BUFFERING_STATE", 12*time.Second),
		obsAt(t0, 12, "PLAY_STATE", 14*time.Second),
		obsAt(t0, 16, "PLAY_STATE", 18*time.Second),
		obsAt(t0, 20, "PLAY_STATE", 22*time.Second),
	} {
		if got := w.observe(o); got.repair || got.hold {
			t.Fatalf("step %d: a resume that plays on got %+v", i, got)
		}
	}
}

// A resume that is still coming up shows one read on the paused second; that
// is held (no end nets), not repaired.
func TestOneReadOnThePausedSecondIsNotYetAFreeze(t *testing.T) {
	var w fileResumeWatch
	t0 := time.Now()
	w.observe(obsAt(t0, 0, "PAUSE_STATE", 30*time.Second))
	if got := w.observe(obsAt(t0, 4, "PLAY_STATE", 30*time.Second)); !got.hold || got.repair {
		t.Fatalf("first frozen read: got %+v, want hold only", got)
	}
	if got := w.observe(obsAt(t0, 8, "PLAY_STATE", 33*time.Second)); got.repair || got.hold {
		t.Fatalf("a position that moved on: got %+v", got)
	}
}

// A pause shorter than the watch's poll is never read as PAUSE_STATE. The key
// press (or the app's pause request) is what ties the freeze to a resume.
func TestAShortPauseTheWatchNeverSawIsRecognisedByTheKeyPress(t *testing.T) {
	var w fileResumeWatch
	t0 := time.Now()
	w.observe(obsAt(t0, 0, "PLAY_STATE", 40*time.Second))
	o := obsAt(t0, 4, "PLAY_STATE", 42*time.Second)
	w.observe(o)
	o = obsAt(t0, 8, "PLAY_STATE", 42*time.Second)
	o.pauseHint = true
	if got := w.observe(o); !got.hold {
		t.Fatalf("got %+v, want hold", got)
	}
	o = obsAt(t0, 12, "PLAY_STATE", 42*time.Second)
	o.pauseHint = true
	if got := w.observe(o); !got.repair || got.at != 42*time.Second {
		t.Fatalf("got %+v, want a repair at the frozen position", got)
	}
}

func TestAFreezeWithoutAPauseOrAtTheEndIsNotAResume(t *testing.T) {
	t0 := time.Now()
	t.Run("no pause", func(t *testing.T) {
		var w fileResumeWatch
		w.observe(obsAt(t0, 0, "PLAY_STATE", 40*time.Second))
		for sec := 4; sec <= 20; sec += 4 {
			if got := w.observe(obsAt(t0, sec, "PLAY_STATE", 40*time.Second)); got.repair || got.hold {
				t.Fatalf("t=%ds: got %+v; a freeze nobody paused belongs to the end nets", sec, got)
			}
		}
	})
	t.Run("at the end", func(t *testing.T) {
		var w fileResumeWatch
		w.observe(obsAt(t0, 0, "PAUSE_STATE", 235*time.Second))
		for sec := 4; sec <= 20; sec += 4 {
			if got := w.observe(obsAt(t0, sec, "PLAY_STATE", 235*time.Second)); got.repair || got.hold {
				t.Fatalf("t=%ds: got %+v; frozen at the end is the track ending", sec, got)
			}
		}
	})
	t.Run("unknown length", func(t *testing.T) {
		var w fileResumeWatch
		o := obsAt(t0, 0, "PAUSE_STATE", 20*time.Second)
		o.end = 0
		w.observe(o)
		for sec := 4; sec <= 20; sec += 4 {
			o := obsAt(t0, sec, "PLAY_STATE", 20*time.Second)
			o.end = 0
			if got := w.observe(o); got.repair || got.hold {
				t.Fatalf("t=%ds: got %+v; nothing to seek in without a length", sec, got)
			}
		}
	})
}

// --- the app's resume --------------------------------------------------------

// pausedFileBox is a fake speaker paused on a file: GetTransportInfo answers
// PAUSED_PLAYBACK and GetPositionInfo the paused position. It records every
// action and the Seek target.
type pausedFileBox struct {
	mu      sync.Mutex
	state   string
	rel     string
	actions []string
	seekTo  []string
}

func (b *pausedFileBox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	action := r.Header.Get("SOAPACTION")
	if i := strings.Index(action, "#"); i >= 0 {
		action = strings.Trim(action[i+1:], `"`)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.actions = append(b.actions, action)
	switch action {
	case "GetTransportInfo":
		_, _ = w.Write([]byte("<CurrentTransportState>" + b.state + "</CurrentTransportState>"))
	case "GetPositionInfo":
		_, _ = w.Write([]byte("<RelTime>" + b.rel + "</RelTime><TrackDuration>0:04:00</TrackDuration>"))
	case "Seek":
		s := string(body)
		if i := strings.Index(s, "<Target>"); i >= 0 {
			s = s[i+len("<Target>"):]
			if j := strings.Index(s, "<"); j >= 0 {
				b.seekTo = append(b.seekTo, s[:j])
			}
		}
	}
}

func (b *pausedFileBox) commands() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, a := range b.actions {
		if a == "GetTransportInfo" || a == "GetPositionInfo" {
			continue
		}
		out = append(out, a)
	}
	return out
}

func shrinkFileRestart(t *testing.T) {
	t.Helper()
	pd, pr, pq := fileRestartSeekDelay, fileRestartSeekRetry, fileRestartQuiet
	fileRestartSeekDelay, fileRestartSeekRetry, fileRestartQuiet = 5*time.Millisecond, 5*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { fileRestartSeekDelay, fileRestartSeekRetry, fileRestartQuiet = pd, pr, pq })
}

func newPausedFileServer(t *testing.T, state string) (*Server, *pausedFileBox) {
	t.Helper()
	shrinkFileRestart(t)
	box := &pausedFileBox{state: state, rel: "0:00:15"}
	srv := httptest.NewServer(box)
	t.Cleanup(srv.Close)
	s := &Server{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		queue:    newPlayQueue(),
		renderer: &upnp.Renderer{ControlURL: srv.URL, Client: srv.Client()},
	}
	return s, box
}

const testFileURL = "http://192.0.2.20:8000/podcast.mp3"

func armTestFile(s *Server) {
	gen := s.setLastPlay(testFileURL, "Podcast", "", "audio/mpeg")
	s.noteActiveFile(singleTrack{boxURL: testFileURL, title: "Podcast", mime: "audio/mpeg",
		meta: upnp.TrackMeta{Duration: 240 * time.Second, Seekable: true}, dur: 240 * time.Second}, gen)
}

func TestAppResumeOfAPausedFileRestartsItAtThePausedPosition(t *testing.T) {
	s, box := newPausedFileServer(t, "PAUSED_PLAYBACK")
	armTestFile(s)

	status, err := s.resumePlayback(context.Background())
	if err != nil || status != "playing" {
		t.Fatalf("resumePlayback = %q, %v", status, err)
	}
	got := strings.Join(box.commands(), ",")
	if got != "Stop,Play,Seek" {
		t.Fatalf("commands = %s, want Stop,Play,Seek", got)
	}
	if len(box.seekTo) != 1 || box.seekTo[0] != "0:00:15.000" {
		t.Fatalf("seek target = %v, want the paused 0:00:15", box.seekTo)
	}
}

func TestAppResumeOfRadioKeepsThePlainPlay(t *testing.T) {
	s, box := newPausedFileServer(t, "PAUSED_PLAYBACK")
	s.setLastPlay("http://192.0.2.20:8000/live", "Station", "", "")

	if _, err := s.resumePlayback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(box.commands(), ","); got != "Play" {
		t.Fatalf("commands = %s, want a plain Play", got)
	}
}

func TestAppResumeOfAFileThatIsNoLongerCurrentKeepsThePlainPlay(t *testing.T) {
	s, box := newPausedFileServer(t, "PAUSED_PLAYBACK")
	armTestFile(s)
	// Something newer was played after the file.
	s.setLastPlay("http://192.0.2.20:8000/live", "Station", "", "")

	if _, err := s.resumePlayback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(box.commands(), ","); got != "Play" {
		t.Fatalf("commands = %s, want a plain Play", got)
	}
}

func TestAppResumeOfAFileThatIsNotPausedKeepsThePlainPlay(t *testing.T) {
	s, box := newPausedFileServer(t, "STOPPED")
	armTestFile(s)

	if _, err := s.resumePlayback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(box.commands(), ","); got != "Play" {
		t.Fatalf("commands = %s, want a plain Play", got)
	}
}

// --- the speaker's own key, through the lone-file end watch -------------------

func TestTheEndWatchRepairsAFrozenResumeFromTheSpeakersKey(t *testing.T) {
	shrinkFileRestart(t)
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	// The watch polls at the fast clock, so the freeze is judged at that clock
	// too (the production 3 s is under one poll of the production 4 s).
	prevFreeze := fileResumeFreezeAfter
	fileResumeFreezeAfter = prevFreeze / queueTestScale
	t.Cleanup(func() { fileResumeFreezeAfter = prevFreeze })
	gen := s.setLastPlay(testFileURL, "Podcast", "", "audio/mpeg")
	np.set("PLAY_STATE", 14*time.Second, 240*time.Second)
	s.armSingleTrackEnd(singleTrack{boxURL: testFileURL, title: "Podcast", mime: "audio/mpeg",
		meta: upnp.TrackMeta{Duration: 240 * time.Second, Seekable: true}, dur: 240 * time.Second}, gen)

	time.Sleep(betweenPolls(1))                            // the first poll saw the position moving
	np.set("PAUSE_STATE", 15*time.Second, 240*time.Second) // key pause
	time.Sleep(betweenPolls(1))                            // one poll saw the pause
	np.set("PLAY_STATE", 15*time.Second, 240*time.Second)  // key resume, frozen

	waitForTrackEnd(t, "the frozen resume to be restarted", func() bool { return rec.has("Seek") })
	got := strings.Join(rec.list(), ",")
	if !strings.Contains(got, "Stop,Play,Seek") {
		t.Fatalf("actions = %s, want Stop,Play,Seek", got)
	}
	if n := rec.countOf("Stop"); n != 1 {
		t.Fatalf("Stop sent %d times, want exactly one restart", n)
	}
}

// An app pause records a user stop (it must hold off the auto re-push), and
// that used to end the lone-file watch: the resume after it then had nobody
// watching it, nor the end of the track. A pause keeps the watch; a stop
// still ends it.
func TestAnAppPauseKeepsTheEndWatchButAStopEndsIt(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.NoteUserStop()
	s.pausePosMu.Lock()
	s.pauseReqAt = time.Now()
	s.pausePosMu.Unlock()
	if !s.pausedNotStopped() {
		t.Fatal("a pause was read as a stop")
	}
	time.Sleep(2 * time.Millisecond)
	s.NoteUserStop()
	if s.pausedNotStopped() {
		t.Fatal("a stop after the pause was still read as a pause")
	}
}
