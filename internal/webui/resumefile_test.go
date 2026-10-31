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
	t.Parallel()
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
		// Resumed: PLAYING, the position does not move. Three reads on the
		// paused second (7 s) before it counts as the freeze.
		{obsAt(t0, 16, "PLAY_STATE", 15*time.Second), fileResumeStep{hold: true}},
		{obsAt(t0, 20, "PLAY_STATE", 15*time.Second), fileResumeStep{hold: true}},
		{obsAt(t0, 24, "PLAY_STATE", 15*time.Second), fileResumeStep{repair: true, at: 15 * time.Second}},
		// Still frozen: the one repair this resume gets has been used.
		{obsAt(t0, 28, "PLAY_STATE", 15*time.Second), fileResumeStep{}},
		{obsAt(t0, 32, "PLAY_STATE", 15*time.Second), fileResumeStep{}},
	}
	for i, st := range steps {
		if got := w.observe(st.obs); got != st.want {
			t.Fatalf("step %d (%s pos %v): got %+v, want %+v", i, st.obs.status, st.obs.pos, got, st.want)
		}
	}
}

func TestAResumeThatContinuesIsLeftAlone(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	if got := w.observe(o); !got.hold {
		t.Fatalf("got %+v, want still a hold after 4 s", got)
	}
	o = obsAt(t0, 16, "PLAY_STATE", 42*time.Second)
	o.pauseHint = true
	if got := w.observe(o); !got.repair || got.at != 42*time.Second {
		t.Fatalf("got %+v, want a repair at the frozen position", got)
	}
}

func TestAFreezeWithoutAPauseOrAtTheEndIsNotAResume(t *testing.T) {
	t.Parallel()
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

// shrinkFileRestart installs a restart clock on s that runs in milliseconds:
// the seek pauses and the quiet window shrink, the freeze judgement keeps its
// production value unless the test is about it. On the server under test,
// never on a package variable, so no cleanup is needed.
func shrinkFileRestart(s *Server) {
	times := productionFileResumeTiming
	times.seekDelay, times.seekRetry, times.restartQuiet = 5*time.Millisecond, 5*time.Millisecond, 50*time.Millisecond
	times.confirmGap = 5 * time.Millisecond
	times.plainPlayWait, times.plainPlayStep = 60*time.Millisecond, 5*time.Millisecond
	s.fileResumeTiming = &times
}

func newPausedFileServer(t *testing.T, state string) (*Server, *pausedFileBox) {
	t.Helper()
	box := &pausedFileBox{state: state, rel: "0:00:15"}
	srv := httptest.NewServer(box)
	t.Cleanup(srv.Close)
	s := &Server{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		queue:    newPlayQueue(),
		renderer: &upnp.Renderer{ControlURL: srv.URL, Client: srv.Client()},
	}
	shrinkFileRestart(s)
	return s, box
}

const testFileURL = "http://192.0.2.20:8000/podcast.mp3"

func armTestFile(s *Server) {
	gen := s.setLastPlay(testFileURL, "Podcast", "", "audio/mpeg")
	s.noteActiveFile(singleTrack{boxURL: testFileURL, title: "Podcast", mime: "audio/mpeg",
		meta: upnp.TrackMeta{Duration: 240 * time.Second, Seekable: true}, dur: 240 * time.Second}, gen)
}

// The plain Play goes first; a position that does not move after it (the fake
// speaker's RelTime stands on 0:00:15, no syslog clock) is the freeze, and the
// file is restarted at the paused position.
func TestAppResumeOfAPausedFileRestartsItAtThePausedPosition(t *testing.T) {
	t.Parallel()
	s, box := newPausedFileServer(t, "PAUSED_PLAYBACK")
	armTestFile(s)

	status, err := s.resumePlayback(context.Background())
	if err != nil || status != "playing" {
		t.Fatalf("resumePlayback = %q, %v", status, err)
	}
	got := strings.Join(box.commands(), ",")
	if got != "Play,Stop,Play,Seek" {
		t.Fatalf("commands = %s, want the plain Play, then Stop,Play,Seek", got)
	}
	if len(box.seekTo) != 1 || box.seekTo[0] != "0:00:15.000" {
		t.Fatalf("seek target = %v, want the paused 0:00:15", box.seekTo)
	}
}

func TestAppResumeOfRadioKeepsThePlainPlay(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	shrinkFileRestart(s)
	// The watch polls at the fast clock, so the freeze is judged at that clock
	// too.
	s.fileResumeTiming.freezeAfter = productionFileResumeTiming.freezeAfter / queueTestScale
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
	t.Parallel()
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

// --- #830 follow-up: the speaker's own resume is never restarted over ----------
//
// Reported on an ST10 (FW 27.0.6, v1.0.23) with a 192 kbit/s MP3 on nginx:
// Pause and Play on the speaker continued correctly (the speaker fetched on
// from its buffer end), and a few seconds later STR restarted the file at the
// paused 31 s anyway, a jump back of about ten seconds. now_playing still showed
// the paused second for two polls, which was all the watch asked for.

// A position that stands still after a pause, but not on the paused second, is
// not the freeze.
func TestAStillPositionAwayFromThePausedSecondIsNotTheFreeze(t *testing.T) {
	t.Parallel()
	var w fileResumeWatch
	t0 := time.Now()
	w.observe(obsAt(t0, 0, "PAUSE_STATE", 31*time.Second))
	for sec := 4; sec <= 24; sec += 4 {
		if got := w.observe(obsAt(t0, sec, "PLAY_STATE", 36*time.Second)); got.repair || got.hold {
			t.Fatalf("t=%ds: got %+v for a position past the pause point", sec, got)
		}
	}
}

// Two reads on the paused second are now_playing trailing a resume, not yet
// the freeze: the third read moves on and nothing is restarted.
func TestAResumeThatNowPlayingShowsLateIsNotRestarted(t *testing.T) {
	t.Parallel()
	var w fileResumeWatch
	t0 := time.Now()
	w.observe(obsAt(t0, 0, "PAUSE_STATE", 31*time.Second))
	for i, st := range []struct {
		obs  fileResumeObs
		want fileResumeStep
	}{
		{obsAt(t0, 4, "PLAY_STATE", 31*time.Second), fileResumeStep{hold: true}},
		{obsAt(t0, 8, "PLAY_STATE", 31*time.Second), fileResumeStep{hold: true}},
		{obsAt(t0, 12, "PLAY_STATE", 39*time.Second), fileResumeStep{}},
	} {
		if got := w.observe(st.obs); got != st.want {
			t.Fatalf("step %d: got %+v, want %+v", i, got, st.want)
		}
	}
}

// scriptedRelPosition is a renderer clock for relPositionFn: it starts at from
// and advances by step on every read (0 = frozen). It counts the reads.
type scriptedRelPosition struct {
	mu    sync.Mutex
	pos   time.Duration
	step  time.Duration
	reads int
}

func (p *scriptedRelPosition) read() (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads++
	cur := p.pos
	p.pos += p.step
	return cur, true
}

func (p *scriptedRelPosition) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reads
}

// pauseAndResumeOnTheKey drives the end watch through the reporter's shape:
// playing, paused on the speaker's key at 31 s, resumed, and now_playing
// standing on 31 s afterwards.
func pauseAndResumeOnTheKey(t *testing.T, rel *scriptedRelPosition) (*Server, *soapRecorder, *fakeNowPlaying) {
	t.Helper()
	np := &fakeNowPlaying{}
	s, rec := singleTrackServer(t, np)
	shrinkFileRestart(s)
	s.fileResumeTiming.freezeAfter = productionFileResumeTiming.freezeAfter / queueTestScale
	s.relPositionFn = rel.read
	url := "http://192.0.2.20:8000/hoerspiel.mp3"
	gen := s.setLastPlay(url, "Episode", "", "audio/mpeg")
	np.set("PLAY_STATE", 29*time.Second, 1800*time.Second)
	s.armSingleTrackEnd(singleTrack{boxURL: url, title: "Episode", mime: "audio/mpeg",
		meta: upnp.TrackMeta{Duration: 1800 * time.Second, Seekable: true}, dur: 1800 * time.Second}, gen)
	time.Sleep(betweenPolls(1))
	np.set("PAUSE_STATE", 31*time.Second, 1800*time.Second)
	time.Sleep(betweenPolls(1))
	np.set("PLAY_STATE", 31*time.Second, 1800*time.Second)
	return s, rec, np
}

func TestTheEndWatchLeavesAResumeTheSpeakerDidItselfAlone(t *testing.T) {
	t.Parallel()
	// The renderer's own clock runs on from the paused 31 s.
	rel := &scriptedRelPosition{pos: 31500 * time.Millisecond, step: time.Second}
	_, rec, np := pauseAndResumeOnTheKey(t, rel)

	waitForTrackEnd(t, "the watch to check the renderer's clock", func() bool { return rel.count() >= 2 })
	// now_playing catches up with the speaker.
	np.set("PLAY_STATE", 40*time.Second, 1800*time.Second)
	time.Sleep(betweenPolls(3))
	for _, a := range []string{"Stop", "Seek", "SetAVTransportURI"} {
		if rec.has(a) {
			t.Fatalf("a resume the speaker did itself was restarted over (%s sent): %v", a, rec.list())
		}
	}
}

func TestTheEndWatchStillRepairsAResumeThatReallyFroze(t *testing.T) {
	t.Parallel()
	rel := &scriptedRelPosition{pos: 31 * time.Second} // frozen on the renderer too
	_, rec, _ := pauseAndResumeOnTheKey(t, rel)

	waitForTrackEnd(t, "the frozen resume to be restarted", func() bool { return rec.has("Seek") })
	if got := strings.Join(rec.list(), ","); !strings.Contains(got, "Stop,Play,Seek") {
		t.Fatalf("actions = %s, want Stop,Play,Seek", got)
	}
}
