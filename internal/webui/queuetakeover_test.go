package webui

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// #1366: a Pandora station started while a library folder played did not end
// the queue. The queue watcher went on timing the library track underneath
// Pandora and pushed the next track over it, and the folder key stayed lit.

func activeTestQueue(s *Server) {
	s.queue = newPlayQueue()
	s.queue.load([]queueItem{
		{URL: "http://192.0.2.10:50002/1.mp3", Title: "01 First", Mime: "audio/mpeg", Duration: 5 * time.Minute},
		{URL: "http://192.0.2.10:50002/2.mp3", Title: "02 Second", Mime: "audio/mpeg", Duration: 5 * time.Minute},
	}, 0, false, repeatOff)
}

func TestPandoraPlayEndsTheLibraryQueue(t *testing.T) {
	s, _, box := nativeTestServer(true)
	pandoraLinked(box)
	activeTestQueue(s)
	w := httptest.NewRecorder()
	s.handlePandoraPlay(w, nativeReq(http.MethodPost, "/api/pandora/play", `{"location":"111","name":"Jazz Radio"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusOK {
		t.Fatalf("play: %d %s", w.Code, w.Body.String())
	}
	if s.queue.isActive() {
		t.Fatal("the library queue is still active after a Pandora station was started")
	}
}

func TestRefusedPandoraStationKeepsTheLibraryQueue(t *testing.T) {
	s, _, box := nativeTestServer(true)
	pandoraLinked(box)
	box.selectErr = errors.New("1005 UNKNOWN_SOURCE_ERROR")
	activeTestQueue(s)
	w := httptest.NewRecorder()
	s.handlePandoraPlay(w, nativeReq(http.MethodPost, "/api/pandora/play", `{"location":"111","name":"Jazz Radio"}`, "192.168.1.5:5000", "application/json"))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("play: %d, want 502", w.Code)
	}
	if !s.queue.isActive() {
		t.Fatal("a station the speaker refused ended the library queue that was still playing")
	}
}

func TestStopQueueIfActiveWithoutAQueue(t *testing.T) {
	s := quietServer("")
	s.stopQueueIfActive("no queue at all") // must not panic on a nil queue
	s.queue = newPlayQueue()
	s.stopQueueIfActive("an idle queue")
	if s.queue.isActive() {
		t.Fatal("an idle queue became active")
	}
}

func TestQueueSourceTakenOver(t *testing.T) {
	cases := []struct {
		name  string
		saw   bool
		src   string
		state string
		want  bool
	}{
		{"pandora playing", true, "PANDORA", "PLAY_STATE", true},
		{"bluetooth buffering", true, "BLUETOOTH", "BUFFERING_STATE", true},
		{"aux playing", true, "AUX", "PLAY_STATE", true},
		{"native station playing", true, "LOCAL_INTERNET_RADIO", "PLAY_STATE", true},
		{"lower-case source", true, "pandora", "PLAY_STATE", true},
		{"the queue itself", true, "UPNP", "PLAY_STATE", false},
		{"between tracks: torn down", true, "INVALID_SOURCE", "", false},
		{"standby is its own path", true, "STANDBY", "", false},
		{"poll error", true, "", "", false},
		{"other source paused", true, "PANDORA", "PAUSE_STATE", false},
		{"other source stopped", true, "PANDORA", "STOP_STATE", false},
		{"box never on the queue yet", false, "LOCAL", "PLAY_STATE", false},
	}
	for _, c := range cases {
		if got := queueSourceTakenOver(c.saw, c.src, c.state); got != c.want {
			t.Errorf("%s: queueSourceTakenOver(%v, %q, %q) = %v, want %v", c.name, c.saw, c.src, c.state, got, c.want)
		}
	}
}

// sourceScriptedBox is a now_playing script with a source attribute.
type sourceScriptedBox struct {
	mu     sync.Mutex
	status string
	source string
	pos    time.Duration
	total  time.Duration
}

func (b *sourceScriptedBox) set(source, status string, pos, total time.Duration) {
	b.mu.Lock()
	b.source, b.status, b.pos, b.total = source, status, pos, total
	b.mu.Unlock()
}

func (b *sourceScriptedBox) poll() (string, time.Duration, time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status, b.pos, b.total, false
}

func (b *sourceScriptedBox) src() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.source
}

func takeoverWatchServer(t *testing.T, box *sourceScriptedBox, firstDur time.Duration, logs *syncBuf) *Server {
	t.Helper()
	s, _ := newPlayTestServer(t)
	s.logger = slog.New(slog.NewTextHandler(logs, nil))
	s.nowPlayingFn = box.poll
	s.nowPlayingSourceFn = box.src
	s.queue.load([]queueItem{
		{URL: "http://192.0.2.10:50002/1.m4a", Title: "01 First", Mime: "audio/mp4", Duration: firstDur},
		{URL: "http://192.0.2.10:50002/2.m4a", Title: "02 Second", Mime: "audio/mp4", Duration: 5 * time.Minute},
	}, 0, false, repeatOff)
	s.setQueueTiming(firstDur)
	s.ensureWatcher()
	t.Cleanup(s.cancelWatcher)
	return s
}

// The #1366 field shape: the library track plays, then Pandora takes over and
// keeps playing past the library track's length. The queue must end, not push
// track 2 over Pandora.
func TestQueueWatcherEndsTheQueueWhenAnotherSourceTakesOver(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time watcher test")
	}
	box := &sourceScriptedBox{}
	logs := &syncBuf{}
	box.set("UPNP", "PLAY_STATE", time.Second, 0)
	s := takeoverWatchServer(t, box, 6*time.Second, logs)

	time.Sleep(5 * time.Second) // one poll on the queue's own source
	box.set("PANDORA", "PLAY_STATE", 0, 0)

	deadline := time.Now().Add(20 * time.Second)
	for s.queue.isActive() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if s.queue.isActive() {
		t.Fatalf("the queue is still active while Pandora plays; log:\n%s", logs.String())
	}
	out := logs.String()
	if strings.Contains(out, "queue advance") {
		t.Errorf("the watcher advanced the queue over Pandora; log:\n%s", out)
	}
	if !strings.Contains(out, "another source is playing on the speaker, ending the queue") {
		t.Errorf("no Info line naming the takeover; log:\n%s", out)
	}
}

// A single poll on another source (a change caught in flight) is not a
// takeover: the box returns to the queue and the queue plays on.
func TestQueueWatcherIgnoresOneForeignPoll(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time watcher test")
	}
	box := &sourceScriptedBox{}
	logs := &syncBuf{}
	box.set("UPNP", "PLAY_STATE", time.Second, 0)
	s := takeoverWatchServer(t, box, 5*time.Minute, logs)

	time.Sleep(5 * time.Second)
	box.set("PANDORA", "PLAY_STATE", 0, 0)
	time.Sleep(4 * time.Second) // exactly one poll sees it
	box.set("UPNP", "PLAY_STATE", 10*time.Second, 0)
	time.Sleep(9 * time.Second)
	if !s.queue.isActive() {
		t.Fatalf("one poll on another source ended the queue; log:\n%s", logs.String())
	}
}
