package webui

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/presets"
)

// The field bundle behind queuebadurl.go (2026-10-26): a folder key still
// pointed at the paths of a USB stick that had been swapped on a router's
// media server. The server answered, every old path was 404, and the queue
// sat out 25 s per track before giving up two minutes later without a word.

// goneMediaServer is a media server that answers, but has none of the files:
// every path is 404. hits counts the requests.
func goneMediaServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func badURLTracks(base string, n int) []queueItem {
	out := make([]queueItem, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, queueItem{
			URL:   base + "/AUDIO/DLNA-1-0/Track" + string(rune('0'+i)) + ".mp3",
			Title: "Track " + string(rune('0'+i)), Mime: "audio/mpeg", Duration: 3 * time.Minute,
		})
	}
	return out
}

// badURLWatchServer is a play-test server whose box never starts a track
// (now_playing answers nothing usable), running the watcher at the fast
// clock.
func badURLWatchServer(t *testing.T, logs *syncBuf) (*Server, *soapRecorder) {
	t.Helper()
	s, rec := newPlayTestServer(t)
	s.queueTiming = fastQueueTiming()
	s.logger = slog.New(slog.NewTextHandler(logs, nil))
	s.nowPlayingFn = func() (string, time.Duration, time.Duration, bool) { return "", 0, 0, false }
	s.staleKeyNoticeFn = func(int) {}
	return s, rec
}

// boxRefusesEveryPush plays the speaker that answers every queue push with
// AUDIO_ERROR_BAD_URL, the way the field box did: a pair of 3101 frames a
// moment after the push. refuse=false for a push leaves that track alone.
func boxRefusesEveryPush(t *testing.T, s *Server, refuse func(url string) bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		var seen time.Time
		for ctx.Err() == nil {
			s.badURL.mu.Lock()
			at, url := s.badURL.pushAt, s.badURL.pushURL
			s.badURL.mu.Unlock()
			if !at.IsZero() && at != seen {
				seen = at
				if refuse(url) {
					// The field box answered 300 ms or more after the push;
					// past the burst window either way.
					time.Sleep(boxBadURLBurst + 50*time.Millisecond)
					s.NoteBoxError("3101", "AUDIO_ERROR_BAD_URL", "AudioPath error2, reason 1")
					s.NoteBoxError("3101", "AUDIO_ERROR_BAD_URL", "")
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
}

func waitQueueInactive(t *testing.T, s *Server, within time.Duration, logs *syncBuf) time.Duration {
	t.Helper()
	start := time.Now()
	for s.queue.isActive() && time.Since(start) < within {
		time.Sleep(5 * time.Millisecond)
	}
	if s.queue.isActive() {
		t.Fatalf("the queue still runs after %v; log:\n%s", within, logs.String())
	}
	return time.Since(start)
}

// Part 1: the box's BAD_URL for the track just pushed ends it at once. The
// stall timeout is never waited out, and the queue moves to the next track.
func TestBoxBadURLEndsTheQueueTrackAtOnce(t *testing.T) {
	t.Parallel()
	media, _ := goneMediaServer(t)
	logs := &syncBuf{}
	s, _ := badURLWatchServer(t, logs)
	items := badURLTracks(media.URL, 5)
	boxRefusesEveryPush(t, s, func(url string) bool { return url == items[0].URL })
	if err := s.startQueue(context.Background(), items, 0, false, repeatOff, recentCardCtx{}); err != nil {
		t.Fatalf("startQueue: %v", err)
	}
	stall := s.queueTiming.stallTimeout
	deadline := time.Now().Add(stall)
	for time.Now().Before(deadline) {
		if it, ok := s.queue.current(); ok && it.URL == items[1].URL {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if it, _ := s.queue.current(); it.URL != items[1].URL {
		t.Fatalf("the refused track was not skipped before the stall timeout (%v); log:\n%s", stall, logs.String())
	}
	out := logs.String()
	if !strings.Contains(out, "the media server could not deliver the track, skipping to the next") {
		t.Errorf("no line naming the skip; log:\n%s", out)
	}
	if !strings.Contains(out, "status=404") {
		t.Errorf("the skip does not carry what the server answered; log:\n%s", out)
	}
	if strings.Contains(out, "never started the track, waking it and handing it over once more") {
		t.Errorf("a track the server does not have was handed over a second time; log:\n%s", out)
	}
}

// Part 1, the proxy's half: a 404 the stream proxy got for the track just
// pushed ends it without the box's word, once the settle window is over.
func TestProxy404EndsTheQueueTrackWithoutTheBox(t *testing.T) {
	t.Parallel()
	logs := &syncBuf{}
	s, _ := badURLWatchServer(t, logs)
	var probes atomic.Int32
	s.trackProbeFn = func(context.Context, string) (int, error) { probes.Add(1); return 0, nil }
	items := badURLTracks("http://192.0.2.10:49200", 4)
	if err := s.startQueue(context.Background(), items, 0, false, repeatOff, recentCardCtx{}); err != nil {
		t.Fatalf("startQueue: %v", err)
	}
	s.NoteStreamUpstreamFailure(items[0].URL, http.StatusNotFound)
	deadline := time.Now().Add(s.queueTiming.stallTimeout)
	for time.Now().Before(deadline) {
		if it, _ := s.queue.current(); it.URL == items[1].URL {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if it, _ := s.queue.current(); it.URL != items[1].URL {
		t.Fatalf("the proxy's 404 did not end the track before the stall timeout; log:\n%s", logs.String())
	}
	if probes.Load() != 0 {
		t.Error("the media server was probed although the proxy already said what it answered")
	}
}

// A 404 for a URL that is not the track pushed last (an earlier track, an
// unrelated station) must not end the current track.
func TestA404ForAnotherAddressLeavesTheTrackAlone(t *testing.T) {
	t.Parallel()
	s := &Server{queue: newPlayQueue()}
	s.queue.load(badURLTracks("http://192.0.2.10:49200", 2), 0, false, repeatOff)
	s.noteQueuePush("http://192.0.2.10:49200/AUDIO/DLNA-1-0/Track1.mp3")
	s.NoteStreamUpstreamFailure("http://radio.example/stream", http.StatusNotFound)
	if _, ok := s.takeQueueBadURL(0); ok {
		t.Fatal("a 404 for another address counted against the queue track")
	}
	s.NoteStreamUpstreamFailure("http://192.0.2.10:49200/AUDIO/DLNA-1-0/Track1.mp3", http.StatusServiceUnavailable)
	if _, ok := s.takeQueueBadURL(0); ok {
		t.Fatal("a 503 (a busy server) counted as a file that is gone")
	}
}

// The box sends its 3101 in pairs. The second of a pair must not count
// against the track pushed right after the first, and a report with no queue
// running is nobody's.
func TestBoxBadURLBurstCountsOnce(t *testing.T) {
	t.Parallel()
	s := &Server{queue: newPlayQueue()}
	s.NoteBoxError("3101", "AUDIO_ERROR_BAD_URL", "")
	s.badURL.mu.Lock()
	idle := s.badURL.boxAt.IsZero() && s.badURL.lastBox.IsZero()
	s.badURL.mu.Unlock()
	if !idle {
		t.Fatal("a BAD_URL with no queue running was recorded")
	}
	s.queue.load(badURLTracks("http://192.0.2.10:49200", 3), 0, false, repeatOff)
	s.noteQueuePush("http://192.0.2.10:49200/AUDIO/DLNA-1-0/Track1.mp3")
	s.NoteBoxError("3101", "AUDIO_ERROR_BAD_URL", "AudioPath error2, reason 1")
	if _, ok := s.takeQueueBadURL(time.Hour); !ok {
		t.Fatal("the box's BAD_URL did not count at once")
	}
	s.noteQueuePush("http://192.0.2.10:49200/AUDIO/DLNA-1-0/Track2.mp3")
	s.NoteBoxError("3101", "AUDIO_ERROR_BAD_URL", "") // the pair's second frame
	if _, ok := s.takeQueueBadURL(time.Hour); ok {
		t.Fatal("the second frame of one burst counted against the next track")
	}
	s.NoteBoxError("1036", "UNABLE_TO_PROCESS_NOT_LOGGED_IN", "")
	if _, ok := s.takeQueueBadURL(time.Hour); ok {
		t.Fatal("an error other than BAD_URL ended a track")
	}
}

// The proxy's 404 alone waits for the box's own verdict for the settle
// window, so the box's late 3101 cannot land on the next track.
func TestProxy404AloneWaitsTheSettleWindow(t *testing.T) {
	t.Parallel()
	s := &Server{queue: newPlayQueue()}
	s.queue.load(badURLTracks("http://192.0.2.10:49200", 3), 0, false, repeatOff)
	u := "http://192.0.2.10:49200/AUDIO/DLNA-1-0/Track1.mp3"
	s.noteQueuePush(u)
	s.NoteStreamUpstreamFailure(u, http.StatusGone)
	if _, ok := s.takeQueueBadURL(time.Hour); ok {
		t.Fatal("the proxy's 404 acted inside the settle window")
	}
	ev, ok := s.takeQueueBadURL(0)
	if !ok || ev.code != http.StatusGone || ev.box || ev.url != u {
		t.Fatalf("after the settle window: %+v ok=%v, want the proxy's 410 for %s", ev, ok, u)
	}
	if _, ok := s.takeQueueBadURL(0); ok {
		t.Fatal("the same evidence was handed out twice")
	}
}

// The dead-track limit keeps its meaning: a queue that is not a folder key
// stops after queueDeadTrackLimit tracks in a row the server could not
// deliver, now in a few seconds instead of minutes, and never calls a key
// stale.
func TestFailFastKeepsTheDeadTrackLimit(t *testing.T) {
	t.Parallel()
	media, _ := goneMediaServer(t)
	logs := &syncBuf{}
	s, _ := badURLWatchServer(t, logs)
	var notices atomic.Int32
	s.staleKeyNoticeFn = func(int) { notices.Add(1) }
	items := badURLTracks(media.URL, 6)
	boxRefusesEveryPush(t, s, func(string) bool { return true })
	if err := s.startQueue(context.Background(), items, 0, false, repeatOff,
		recentCardCtx{key: "queue:uuid:00000000-0000-0000-0000-000000000000:7", name: "Library folder"}); err != nil {
		t.Fatalf("startQueue: %v", err)
	}
	took := waitQueueInactive(t, s, 3*s.queueTiming.stallTimeout, logs)
	if took >= s.queueTiming.stallTimeout {
		t.Errorf("three refused tracks took %v, the stall timeout alone is %v", took, s.queueTiming.stallTimeout)
	}
	out := logs.String()
	if !strings.Contains(out, "the media server could not deliver several tracks in a row") {
		t.Errorf("the queue did not stop on the dead-track limit; log:\n%s", out)
	}
	if n := strings.Count(out, "could not deliver the track, skipping to the next"); n != queueDeadTrackLimit-1 {
		t.Errorf("%d skips before the stop, want %d; log:\n%s", n, queueDeadTrackLimit-1, out)
	}
	if notices.Load() != 0 || strings.Contains(out, "the folder has to be saved on the key again") {
		t.Error("a Library folder that is on no key was reported as a stale key")
	}
	if _, _, ok := s.StaleFolderKey(); ok {
		t.Error("StaleFolderKey names a key for a queue that came from no key")
	}
}

// seedFolderKey stores a folder key holding tracks on slot.
func seedFolderKey(t *testing.T, s *Server, slot int, name string, tracks []queueItem) presets.Preset {
	t.Helper()
	p := presets.Preset{Slot: slot, Name: name, Type: "queue", Source: "Media server"}
	for _, it := range tracks {
		p.Items = append(p.Items, presets.PresetItem{URL: it.URL, Title: it.Title, Mime: it.Mime, DurationSec: int(it.Duration / time.Second)})
	}
	if err := s.presets.SetSlot(p); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}
	return p
}

// Part 2: the first tracks of a folder key's queue all 404 while the server
// answers. The queue stops at once, the speaker display gets the notice, the
// app and the phone page get the key through /api/agent/version, and the
// debug state records it.
func TestStaleFolderKeyStopsAndTellsTheOwner(t *testing.T) {
	t.Parallel()
	media, hits := goneMediaServer(t)
	logs := &syncBuf{}
	s, _ := badURLWatchServer(t, logs)
	var noticeSlot atomic.Int32
	s.staleKeyNoticeFn = func(slot int) { noticeSlot.Store(int32(slot)) }
	items := badURLTracks(media.URL, 8)
	seedFolderKey(t, s, 1, "All tracks", items)
	boxRefusesEveryPush(t, s, func(string) bool { return true })
	if err := s.startQueue(context.Background(), items, 0, false, repeatOff,
		recentCardCtx{key: "queue:slot:1", name: "All tracks"}); err != nil {
		t.Fatalf("startQueue: %v", err)
	}
	took := waitQueueInactive(t, s, 3*s.queueTiming.stallTimeout, logs)
	if took >= s.queueTiming.stallTimeout {
		t.Errorf("the stale key took %v to be found, the stall timeout alone is %v", took, s.queueTiming.stallTimeout)
	}
	if hits.Load() < staleFolderKeyTracks {
		t.Errorf("the media server was asked %d times, want one probe per refused track", hits.Load())
	}
	if got := noticeSlot.Load(); got != 1 {
		t.Errorf("speaker display notice for key %d, want key 1", got)
	}
	slot, _, ok := s.StaleFolderKey()
	if !ok || slot != 1 {
		t.Fatalf("StaleFolderKey = %d, %v; want key 1", slot, ok)
	}
	out := logs.String()
	if !strings.Contains(out, "the key's files are gone from it") || !strings.Contains(out, "points at files the media server no longer has") {
		t.Errorf("the log does not say the key's files are gone; log:\n%s", out)
	}
	if strings.Contains(out, "several tracks in a row") {
		t.Errorf("a stale key was reported as an unreachable server; log:\n%s", out)
	}

	// The debug state shows it next to the episode it ended.
	raw, err := json.Marshal(s.QueueForensics())
	if err != nil {
		t.Fatal(err)
	}
	var dbg struct {
		Recent []struct {
			EndReason string `json:"endReason"`
			BadURL    int    `json:"badUrlSkips"`
		} `json:"recent"`
		Stale *struct {
			Slot          int   `json:"slot"`
			TracksTried   int   `json:"tracksTried"`
			Codes         []int `json:"codes"`
			ServerAnswers bool  `json:"serverAnswers"`
		} `json:"staleFolderKey"`
	}
	if err := json.Unmarshal(raw, &dbg); err != nil {
		t.Fatal(err)
	}
	if dbg.Stale == nil || dbg.Stale.Slot != 1 || dbg.Stale.TracksTried != staleFolderKeyTracks || !dbg.Stale.ServerAnswers {
		t.Errorf("debug state staleFolderKey = %+v; raw %s", dbg.Stale, raw)
	}
	if len(dbg.Recent) == 0 || !strings.Contains(dbg.Recent[len(dbg.Recent)-1].EndReason, "key 1") ||
		dbg.Recent[len(dbg.Recent)-1].BadURL != staleFolderKeyTracks {
		t.Errorf("the ended episode does not name the key or count the skips; raw %s", raw)
	}

	// /api/agent/version carries it for the app and the phone page.
	w := httptest.NewRecorder()
	s.handleAgentVersion(w, httptest.NewRequest(http.MethodGet, "/api/agent/version", nil))
	var av map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &av); err != nil {
		t.Fatalf("agent version: %v (%s)", err, w.Body.String())
	}
	if av["staleFolderKeySlot"] != "1" {
		t.Errorf("staleFolderKeySlot = %q, want \"1\"", av["staleFolderKeySlot"])
	}

	// Saving the folder on the key again ends the hint.
	seedFolderKey(t, s, 1, "All tracks", badURLTracks("http://192.0.2.10:49200/new", 3))
	if _, _, ok := s.StaleFolderKey(); ok {
		t.Error("the hint stays after the folder was saved on the key again")
	}
}

// A server that does not answer at all is not a moved folder: the key is
// not called stale, the dead-track limit stops the queue as before.
func TestAFolderKeyOnADeadServerIsNotCalledStale(t *testing.T) {
	t.Parallel()
	logs := &syncBuf{}
	s, _ := badURLWatchServer(t, logs)
	var notices atomic.Int32
	s.staleKeyNoticeFn = func(int) { notices.Add(1) }
	s.trackProbeFn = func(context.Context, string) (int, error) { return 0, context.DeadlineExceeded }
	items := badURLTracks("http://192.0.2.10:49200", 6)
	seedFolderKey(t, s, 3, "Evening", items)
	boxRefusesEveryPush(t, s, func(string) bool { return true })
	if err := s.startQueue(context.Background(), items, 0, false, repeatOff,
		recentCardCtx{key: "queue:slot:3", name: "Evening"}); err != nil {
		t.Fatalf("startQueue: %v", err)
	}
	waitQueueInactive(t, s, 3*s.queueTiming.stallTimeout, logs)
	if notices.Load() != 0 {
		t.Error("a server that does not answer was reported as a stale key")
	}
	if !strings.Contains(logs.String(), "the media server could not deliver several tracks in a row") {
		t.Errorf("the dead-track limit did not stop the queue; log:\n%s", logs.String())
	}
}

// One deleted file at the start of a key that otherwise plays is not a moved
// folder: a track heard in between ends the stale verdict for that queue.
func TestAHeardTrackEndsTheStaleVerdict(t *testing.T) {
	t.Parallel()
	s := &Server{queue: newPlayQueue()}
	s.queue.load(badURLTracks("http://192.0.2.10:49200", 6), 0, false, repeatOff)
	s.resetBadURLRun(recentCardCtx{key: "queue:slot:2"})
	if _, stale := s.noteBadURLRunTrack(http.StatusNotFound, true); stale {
		t.Fatal("one gone track called the key stale")
	}
	s.noteQueueHeard()
	for i := 0; i < staleFolderKeyTracks; i++ {
		if _, stale := s.noteBadURLRunTrack(http.StatusNotFound, true); stale {
			t.Fatal("a key whose second track played was called stale")
		}
	}
	// A new queue starts the verdict over.
	s.resetBadURLRun(recentCardCtx{key: "queue:slot:2"})
	var stale bool
	for i := 0; i < staleFolderKeyTracks; i++ {
		_, stale = s.noteBadURLRunTrack(http.StatusNotFound, true)
	}
	if !stale {
		t.Fatalf("%d gone tracks from the start of a key's queue were not called stale", staleFolderKeyTracks)
	}
}

// A folder with fewer tracks than the verdict needs is judged on all of them.
func TestStaleVerdictOnAShortFolder(t *testing.T) {
	t.Parallel()
	s := &Server{queue: newPlayQueue()}
	s.queue.load(badURLTracks("http://192.0.2.10:49200", 2), 0, false, repeatOff)
	s.resetBadURLRun(recentCardCtx{key: "queue:slot:4"})
	if _, stale := s.noteBadURLRunTrack(http.StatusNotFound, true); stale {
		t.Fatal("stale after one of two tracks")
	}
	if _, stale := s.noteBadURLRunTrack(http.StatusGone, true); !stale {
		t.Fatal("a two-track key with both tracks gone was not called stale")
	}
}

// Part 3: the idle re-push never pushes an address the media server just
// answered 404 for, neither the address itself nor one wrapped in the
// stream proxy's /stream/raw.
func TestRePushSkipsAnAddressThatJust404d(t *testing.T) {
	t.Parallel()
	const track = "http://192.0.2.10:49200/AUDIO/DLNA-3-0/Morning%20Sky.m4a"
	for name, boxURL := range map[string]string{
		"direct":  track,
		"proxied": boxurl.RawStream(track),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, rec := newPlayTestServer(t)
			logs := &syncBuf{}
			s.logger = slog.New(slog.NewTextHandler(logs, nil))
			s.playStateFn = func() (bool, bool) { return false, false } // box on, idle
			s.setLastPlay(boxURL, "Morning Sky", "", "audio/mp4")
			s.NoteStreamUpstreamFailure(track, http.StatusNotFound)
			done := make(chan struct{})
			go func() { s.maybeRePush(); close(done) }()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("maybeRePush did not return")
			}
			if rec.has("SetAVTransportURI") {
				t.Fatalf("an address that just answered 404 was pushed again: %v", rec.list())
			}
			if !strings.Contains(logs.String(), "the media server answered this address with an error a moment ago") {
				t.Errorf("no line saying why; log:\n%s", logs.String())
			}
		})
	}
}

func TestUrlRecentlyGoneForgetsAndIsBounded(t *testing.T) {
	t.Parallel()
	s := &Server{}
	s.badURL.mu.Lock()
	for i := 0; i < goneURLKeep+10; i++ {
		s.noteGoneLocked("http://192.0.2.10:49200/t"+string(rune('a'+i)), http.StatusNotFound)
	}
	n := len(s.badURL.gone)
	s.badURL.gone["http://192.0.2.10:49200/old"] = goneURL{code: 404, at: time.Now().Add(-goneURLTTL)}
	s.badURL.mu.Unlock()
	if n > goneURLKeep {
		t.Errorf("%d gone addresses kept, the bound is %d", n, goneURLKeep)
	}
	if _, ok := s.urlRecentlyGone("http://192.0.2.10:49200/old"); ok {
		t.Error("an address that 404'd longer ago than the TTL still blocks the re-push")
	}
	if _, ok := s.urlRecentlyGone(""); ok {
		t.Error("an empty address counted as gone")
	}
}

// The watcher must never read the reports on a timer of its own: the wake
// channel is the only addition to its select (no new periodic timer, SCHONE
// die Box-Hardware).
func TestBadURLWakeAddsNoTimer(t *testing.T) {
	src := readRepoFile(t, "queuebadurl.go")
	for _, timer := range []string{"time.NewTicker", "time.Tick(", "time.AfterFunc", "time.NewTimer"} {
		if strings.Contains(src, timer) {
			t.Errorf("queuebadurl.go uses %s; the reports must wake the watcher, not a timer", timer)
		}
	}
}
