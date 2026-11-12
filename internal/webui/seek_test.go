package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/upnp"
)

// Discussion #1207: a click on the progress bar jumps inside a music-library
// file. These tests pin what the agent takes, what it refuses, and that the
// two end watches keep timing the same track after a jump.

// postSeek sends POST /api/seek and returns the status and the decoded answer.
func postSeek(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleSeek(w, httptest.NewRequest(http.MethodPost, "/api/seek", strings.NewReader(body)))
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func seekCommands(box *pausedFileBox) string {
	return strings.Join(box.commands(), ",")
}

func TestSeekJumpsInsideALoneLibraryFile(t *testing.T) {
	t.Parallel()
	s, box := newPausedFileServer(t, "PLAYING")
	armTestFile(s)

	code, out := postSeek(t, s, `{"positionSec":90}`)
	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("seek answered %d %v, want 200 ok", code, out)
	}
	if out["positionSec"] != float64(90) || out["durationSec"] != float64(240) {
		t.Fatalf("answer %v, want position 90 of 240", out)
	}
	if got := seekCommands(box); got != "Seek" {
		t.Fatalf("commands = %s, want one Seek and nothing else (no stop, no new play)", got)
	}
	if len(box.seekTo) != 1 || box.seekTo[0] != "0:01:30.000" {
		t.Fatalf("seek target = %v, want 0:01:30", box.seekTo)
	}
}

// Radio, Spotify and anything else that is not a directly played file: the
// agent holds no active file and no queue, and says so without touching the
// speaker.
func TestSeekIsRefusedForWhatIsNotASeekableFile(t *testing.T) {
	t.Parallel()
	s, box := newPausedFileServer(t, "PLAYING")
	s.setLastPlay("http://127.0.0.1:8888/stream/raw?u=x", "Radio", "", "")

	code, out := postSeek(t, s, `{"positionSec":30}`)
	if code != http.StatusConflict || out["reason"] != "not-seekable" {
		t.Fatalf("seek on radio answered %d %v, want 409 not-seekable", code, out)
	}
	if out["error"] == "" || out["error"] == nil {
		t.Fatal("the refusal carries no sentence saying why")
	}
	if got := seekCommands(box); got != "" {
		t.Fatalf("the speaker was sent %s for a play that cannot be positioned", got)
	}
}

// A play that superseded the file (a station started later) makes it stale.
func TestSeekIsRefusedOnceSomethingNewerPlays(t *testing.T) {
	t.Parallel()
	s, box := newPausedFileServer(t, "PLAYING")
	armTestFile(s)
	s.setLastPlay("http://127.0.0.1:8888/stream/raw?u=x", "Radio", "", "")

	if code, out := postSeek(t, s, `{"positionSec":30}`); code != http.StatusConflict || out["reason"] != "not-seekable" {
		t.Fatalf("answered %d %v, want 409 not-seekable", code, out)
	}
	if got := seekCommands(box); got != "" {
		t.Fatalf("the speaker was sent %s", got)
	}
}

func TestSeekOutsideTheTrackIsRefused(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{"positionSec":300}`, `{"positionSec":-5}`, `{}`, `{"positionSec":"x"}`} {
		s, box := newPausedFileServer(t, "PLAYING")
		armTestFile(s)
		code, out := postSeek(t, s, body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s answered %d %v, want 400", body, code, out)
		}
		if got := seekCommands(box); got != "" {
			t.Fatalf("%s: the speaker was sent %s", body, got)
		}
	}
	s, _ := newPausedFileServer(t, "PLAYING")
	armTestFile(s)
	if _, out := postSeek(t, s, `{"positionSec":300}`); out["reason"] != "out-of-range" {
		t.Fatalf("a position past the 240 s track answered %v, want reason out-of-range", out)
	}
}

// A jump onto the very end is pulled back a second: a position on the end is
// what the end watches read as the track being over.
func TestSeekToTheEndStopsShortOfIt(t *testing.T) {
	t.Parallel()
	s, box := newPausedFileServer(t, "PLAYING")
	armTestFile(s)
	if code, out := postSeek(t, s, `{"positionSec":240}`); code != http.StatusOK {
		t.Fatalf("answered %d %v", code, out)
	}
	if len(box.seekTo) != 1 || box.seekTo[0] != "0:03:59.000" {
		t.Fatalf("seek target = %v, want 0:03:59", box.seekTo)
	}
}

func TestSeekIsRefusedWhileTheSpeakerIsPaused(t *testing.T) {
	t.Parallel()
	s, box := newPausedFileServer(t, "PAUSED_PLAYBACK")
	armTestFile(s)
	code, out := postSeek(t, s, `{"positionSec":30}`)
	if code != http.StatusConflict || out["reason"] != "not-playing" {
		t.Fatalf("answered %d %v, want 409 not-playing", code, out)
	}
	if got := seekCommands(box); got != "" {
		t.Fatalf("the paused speaker was sent %s", got)
	}
}

func TestSeekReportsASpeakerThatRefuses(t *testing.T) {
	t.Parallel()
	s, box := newPausedFileServer(t, "PLAYING")
	box.refuseSeek = true
	armTestFile(s)
	code, out := postSeek(t, s, `{"positionSec":30}`)
	if code != http.StatusBadGateway || out["reason"] != "seek-refused" {
		t.Fatalf("answered %d %v, want 502 seek-refused", code, out)
	}
	// A refused jump changes nothing for the end watches.
	if _, ok := s.userSeek.after(0); ok {
		t.Fatal("a jump the speaker refused was recorded for the end watches")
	}
}

func TestSeekIsPostOnly(t *testing.T) {
	t.Parallel()
	s, _ := newPausedFileServer(t, "PLAYING")
	armTestFile(s)
	w := httptest.NewRecorder()
	s.handleSeek(w, httptest.NewRequest(http.MethodGet, "/api/seek", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET answered %d, want 405", w.Code)
	}
}

// A folder track the speaker fetches itself can be positioned; one that goes
// through the stream proxy (no MIME type, or HTTPS) cannot.
func TestSeekInAFolderOnlyForADirectlyPlayedTrack(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		item queueItem
		want int
	}{
		{"direct", queueItem{URL: "http://192.0.2.20:50002/1.mp3", Title: "One", Mime: "audio/mpeg", Duration: 240 * time.Second}, http.StatusOK},
		{"no mime, proxied", queueItem{URL: "http://192.0.2.20:50002/1.mp3", Title: "One", Duration: 240 * time.Second}, http.StatusConflict},
		{"https, proxied", queueItem{URL: "https://192.0.2.20/1.mp3", Title: "One", Mime: "audio/mpeg", Duration: 240 * time.Second}, http.StatusConflict},
	} {
		s, box := newPausedFileServer(t, "PLAYING")
		s.queue.load([]queueItem{c.item, {URL: "http://192.0.2.20:50002/2.mp3", Title: "Two", Mime: "audio/mpeg"}}, 0, false, repeatOff)
		s.setQueueTiming(c.item.Duration)
		code, out := postSeek(t, s, `{"positionSec":60}`)
		if code != c.want {
			t.Fatalf("%s: answered %d %v, want %d", c.name, code, out, c.want)
		}
		wantCmd := ""
		if c.want == http.StatusOK {
			wantCmd = "Seek"
		}
		if got := seekCommands(box); got != wantCmd {
			t.Fatalf("%s: commands = %q, want %q", c.name, got, wantCmd)
		}
		if snap := s.queue.snapshot(); snap.Pos != 0 || !snap.Active {
			t.Fatalf("%s: the jump moved or ended the queue: %+v", c.name, snap)
		}
	}
}

// GET /api/position is where the clients learn whether the bar may be clicked.
func TestPositionSaysWhetherTheBarCanBeClicked(t *testing.T) {
	t.Parallel()
	position := func(s *Server) map[string]any {
		w := httptest.NewRecorder()
		s.handlePosition(w, httptest.NewRequest(http.MethodGet, "/api/position", nil))
		out := map[string]any{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("position answer %q: %v", w.Body.String(), err)
		}
		return out
	}

	s, _ := newPausedFileServer(t, "PLAYING")
	armTestFile(s)
	if out := position(s); out["seekable"] != true || out["durationSec"] != float64(240) {
		t.Fatalf("a playing library file answered %v, want seekable", out)
	}

	// The same file once the speaker is known to be paused.
	s.cacheStatusBody([]byte(`<nowPlaying source="UPNP"><playStatus>PAUSE_STATE</playStatus></nowPlaying>`))
	if out := position(s); out["seekable"] != false {
		t.Fatalf("a paused file answered %v, want not seekable", out)
	}
	s.cacheStatusBody([]byte(`<nowPlaying source="UPNP"><playStatus>PLAY_STATE</playStatus></nowPlaying>`))
	if out := position(s); out["seekable"] != true {
		t.Fatalf("the file playing again answered %v, want seekable", out)
	}

	radio, _ := newPausedFileServer(t, "PLAYING")
	radio.setLastPlay("http://127.0.0.1:8888/stream/raw?u=x", "Radio", "", "")
	if out := position(radio); out["seekable"] != false {
		t.Fatalf("radio answered %v, want not seekable", out)
	}
}

func TestClampSeekTarget(t *testing.T) {
	t.Parallel()
	sec := time.Second
	for _, c := range []struct {
		target, length, want time.Duration
		ok                   bool
	}{
		{0, 240 * sec, 0, true},
		{90 * sec, 240 * sec, 90 * sec, true},
		{240 * sec, 240 * sec, 239 * sec, true},
		{240*sec + 500*time.Millisecond, 240 * sec, 239 * sec, true},
		{242 * sec, 240 * sec, 0, false},
		{-sec, 240 * sec, 0, false},
		{10 * sec, 0, 0, false},
		{500 * time.Millisecond, 500 * time.Millisecond, 0, true},
	} {
		got, ok := clampSeekTarget(c.target, c.length)
		if ok != c.ok || got != c.want {
			t.Fatalf("clampSeekTarget(%v, %v) = %v %v, want %v %v", c.target, c.length, got, ok, c.want, c.ok)
		}
	}
}

// seekScript is a speaker whose now_playing follows a file of the given length
// in real time, and whose position follows a jump: what the end watches poll
// while a test jumps through POST /api/seek.
type seekScript struct {
	mu     sync.Mutex
	length time.Duration
	base   time.Duration // position at since
	since  time.Time
	// stopUntil: the speaker reports STOP_STATE at zero until then, the way
	// a speaker re-opening the file after a jump might.
	stopUntil time.Time
}

func (sc *seekScript) nowPlaying() (string, time.Duration, time.Duration, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if time.Now().Before(sc.stopUntil) {
		return "STOP_STATE", 0, 0, false
	}
	pos := sc.base + time.Since(sc.since)
	if pos > sc.length {
		pos = sc.length // frozen at the end of the file, the ST10/ST20 way
	}
	return "PLAY_STATE", pos.Truncate(50 * time.Millisecond), sc.length, false
}

// jump moves the scripted position, as the speaker does once it took a Seek
// (so a test calls it right after the Seek was answered).
func (sc *seekScript) jump(to, stopFor time.Duration) {
	sc.mu.Lock()
	sc.base, sc.since = to, time.Now()
	sc.stopUntil = time.Now().Add(stopFor)
	sc.mu.Unlock()
}

// seekWatchServer is a server whose renderer answers PLAYING and takes a Seek,
// with the end watches on a clock twenty times the production one (a poll
// every 200 ms) and a 300 ms quiet window after a jump.
func seekWatchServer(t *testing.T, sc *seekScript) (*Server, *pausedFileBox) {
	t.Helper()
	s, _ := newPlayTestServer(t)
	box := &pausedFileBox{state: "PLAYING", rel: "0:00:01"}
	srv := httptest.NewServer(box)
	t.Cleanup(srv.Close)
	s.renderer = &upnp.Renderer{ControlURL: srv.URL, Client: srv.Client()}
	s.queueTiming = scaleQueueTiming(20)
	s.queueInZoneFn = func() bool { return false }
	times := productionFileResumeTiming
	times.seekQuiet = 300 * time.Millisecond
	s.fileResumeTiming = &times
	s.nowPlayingFn = sc.nowPlaying
	return s, box
}

// A jump back inside a folder track. The watcher timed the track from its push
// and followed the highest position it had seen, so the wall clock ran out
// while the track still had the jumped distance to play: the next track cut in
// early. The speaker also reports a stop for a moment after the jump here,
// which the watcher used to read as "stopped in the middle of a track" and
// ended the folder on.
func TestAJumpBackInAFolderTrackIsNotItsEnd(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("real-time watcher test")
	}
	const length = 3 * time.Second
	sc := &seekScript{length: length}
	s, box := seekWatchServer(t, sc)
	s.queue.load([]queueItem{
		{URL: "http://192.0.2.10:50002/1.mp3", Title: "01 First", Mime: "audio/mpeg", Duration: length},
		{URL: "http://192.0.2.10:50002/2.mp3", Title: "02 Second", Mime: "audio/mpeg", Duration: 5 * time.Minute},
	}, 0, false, repeatOff)
	sc.jump(0, 0)
	t0 := time.Now()
	s.setQueueTiming(length)
	s.ensureWatcher()
	t.Cleanup(s.cancelWatcher)

	// Two seconds in, jump back to 0.5 s: the track now ends 4.5 s after its push.
	time.Sleep(2 * time.Second)
	if code, out := postSeek(t, s, `{"positionSec":0.5}`); code != http.StatusOK {
		t.Fatalf("seek answered %d %v", code, out)
	}
	sc.jump(500*time.Millisecond, 250*time.Millisecond)
	if snap := s.queue.snapshot(); snap.Pos != 0 || !snap.Active {
		t.Fatalf("the jump itself moved or ended the queue: %+v", snap)
	}

	// Until the track can have ended, nothing advances and nothing stops.
	for time.Since(t0) < 4200*time.Millisecond {
		snap := s.queue.snapshot()
		if at := time.Since(t0); at < 4400*time.Millisecond && (snap.Pos != 0 || !snap.Active) {
			t.Fatalf("%v after the push the queue is at %+v; the jump back was taken for the end of the track", at, snap)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// And then it does end, on the same watcher.
	deadline := time.Now().Add(10 * time.Second)
	for s.queue.snapshot().Pos == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if snap := s.queue.snapshot(); snap.Pos != 1 || !snap.Active {
		t.Fatalf("the track never ended after the jump: %+v", snap)
	}
	if got := seekCommands(box); !strings.HasPrefix(got, "Seek,SetAVTransportURI") {
		t.Fatalf("commands = %s, want the Seek and then the next track, nothing in between", got)
	}
}

// A jump forward. The position was suddenly further than the wall clock since
// the push allows, which the watcher reads as a position that cannot time the
// end, and the track then ended on the wall clock: the jumped distance was
// silence before the next track.
func TestAJumpForwardInAFolderTrackEndsItOnTime(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("real-time watcher test")
	}
	const length = 4 * time.Second
	sc := &seekScript{length: length}
	s, _ := seekWatchServer(t, sc)
	s.queue.load([]queueItem{
		{URL: "http://192.0.2.10:50002/1.mp3", Title: "01 First", Mime: "audio/mpeg", Duration: length},
		{URL: "http://192.0.2.10:50002/2.mp3", Title: "02 Second", Mime: "audio/mpeg", Duration: 5 * time.Minute},
	}, 0, false, repeatOff)
	sc.jump(0, 0)
	t0 := time.Now()
	s.setQueueTiming(length)
	s.ensureWatcher()
	t.Cleanup(s.cancelWatcher)

	// Half a second in, jump to 3 s: the track ends 1.5 s after its push.
	time.Sleep(500 * time.Millisecond)
	if code, out := postSeek(t, s, `{"positionSec":3}`); code != http.StatusOK {
		t.Fatalf("seek answered %d %v", code, out)
	}
	sc.jump(3*time.Second, 0)
	deadline := time.Now().Add(10 * time.Second)
	for s.queue.snapshot().Pos == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	took := time.Since(t0)
	if s.queue.snapshot().Pos != 1 {
		t.Fatal("the queue never advanced")
	}
	if took < 1500*time.Millisecond {
		t.Fatalf("advanced %v after the push, before the track was over", took)
	}
	// Without the jump the wall clock holds the track for its whole 4 s.
	if took > 3*time.Second {
		t.Fatalf("advanced %v after the push; the track ended 1.5 s in and the rest was silence", took)
	}
}

// The same jump back on a lone file: its end watch stops the speaker at the
// end of the track, and must not do so while the track still plays.
func TestAJumpBackInALoneFileDoesNotStopIt(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("real-time watcher test")
	}
	const length = 3 * time.Second
	sc := &seekScript{length: length}
	s, box := seekWatchServer(t, sc)
	const fileURL = "http://192.0.2.20:8000/song.mp3"
	tr := singleTrack{boxURL: fileURL, title: "Song", mime: "audio/mpeg",
		meta: upnp.TrackMeta{Duration: length, Seekable: true}, dur: length}
	gen := s.setLastPlay(fileURL, "Song", "", "audio/mpeg")
	sc.jump(0, 0)
	t0 := time.Now()
	s.armSingleTrackEnd(tr, gen)

	time.Sleep(2 * time.Second)
	if code, out := postSeek(t, s, `{"positionSec":0.5}`); code != http.StatusOK {
		t.Fatalf("seek answered %d %v", code, out)
	}
	sc.jump(500*time.Millisecond, 250*time.Millisecond)
	for time.Since(t0) < 4200*time.Millisecond {
		if at := time.Since(t0); at < 4400*time.Millisecond && strings.Contains(seekCommands(box), "Stop") {
			t.Fatalf("%v after the push the speaker was stopped; the jump back was taken for the end of the track", at)
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitForTrackEnd(t, "the speaker to be stopped at the real end of the track",
		func() bool { return strings.Contains(seekCommands(box), "Stop") })
	if got := seekCommands(box); got != "Seek,Stop" {
		t.Fatalf("commands = %s, want the Seek and the Stop at the end", got)
	}
}

// The phone page: the bar is a slider only behind the agent's "seekable", and
// every way of jumping asks that gate first, so radio keeps a bar that does
// nothing.
func TestPhoneRemoteProgressBarJumpsOnlyWhenSeekable(t *testing.T) {
	t.Parallel()
	gate := phoneFn(t, "function progSeekable()")
	if !strings.Contains(gate, "tp.seekable === true && tp.dur > 0") {
		t.Errorf("the gate no longer rests on the agent's answer and a known length: %s", gate)
	}
	poll := phoneFn(t, "async function pollProg()")
	if !strings.Contains(poll, "tp.seekable = j.seekable === true;") {
		t.Error("pollProg no longer takes seekable from /api/position")
	}
	paint := phoneFn(t, "function paintProgSeek(bar, shown)")
	for _, want := range []string{
		"var on = progSeekable();",
		"bar.classList.toggle('seek', on);",
		"bar.setAttribute('role', 'slider');",
		"bar.removeAttribute(a)",
	} {
		if !strings.Contains(paint, want) {
			t.Errorf("paintProgSeek lost %q", want)
		}
	}
	seek := phoneFn(t, "async function seekProg(sec)")
	for _, want := range []string{
		"if (sec < 0 || !progSeekable()) return;",
		"apiDetail('/api/seek', 'POST', { positionSec: sec }",
	} {
		if !strings.Contains(seek, want) {
			t.Errorf("seekProg lost %q", want)
		}
	}
	start := strings.Index(indexHTML, "(function wireProgSeek() {")
	end := strings.Index(indexHTML, "async function pollProg()")
	if start < 0 || end < start {
		t.Fatal("the bar's handlers are gone; if they moved, move this test with them")
	}
	if n := strings.Count(indexHTML[start:end], "if (!progSeekable()"); n != 2 {
		t.Errorf("the pointer and key handlers ask the gate %d times, want both of them", n)
	}
	if !strings.Contains(indexHTML, ".trackprog .tbar.seek { position:relative; overflow:visible; cursor:pointer;") {
		t.Error("the pointer cursor no longer hangs on the seek class alone")
	}
	// A new track is not seekable until the speaker says so.
	if !strings.Contains(indexHTML, "tp.at = 0; tp.seekable = false; tp.seekAt = 0;") {
		t.Error("a track change no longer closes the gate")
	}
}

// progSeekTarget is the phone page's click-to-position math, the same rule as
// the desktop app's seekTargetSec (tested there with vitest).
func TestPhoneRemoteSeekMathMatchesTheApp(t *testing.T) {
	t.Parallel()
	fn := phoneFn(t, "function progSeekTarget(clientX, left, width, dur, rtl)")
	for _, want := range []string{
		"var frac = (clientX - left) / width;",
		"if (rtl) frac = 1 - frac;",
		"frac = Math.min(1, Math.max(0, frac));",
		"return Math.round(frac * dur);",
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("progSeekTarget lost %q", want)
		}
	}
}
