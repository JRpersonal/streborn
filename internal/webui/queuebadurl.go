// A queue track the media server no longer has fails at once, and a folder
// key whose files have all moved says so.
//
// A folder key stores its tracks as absolute URLs. The heal in
// queuepresetheal.go follows the media server to a new host or port, but the
// PATH is part of the URL too, and some servers build it from where the file
// sits on their disk. A router media server (a FRITZ!Box) serving a USB stick
// does exactly that: the owner swapped the stick, the server came back at the
// same address, and every path the key held answered 404.
//
// Field bundle, 2026-10-26: a 504-track folder key. The stream proxy logged
// "upstream status 404", the box answered with AUDIO_ERROR_BAD_URL (3101) a
// fraction of a second later and dropped to INVALID_SOURCE, and the queue
// watcher still waited its full 25 s stall timeout per track, tried the first
// one a second time, skipped two, and gave up with "several tracks in a row
// never started playing" after almost two minutes of silence. Nothing told
// the owner why, and the idle re-push in between pushed a URL that had 404'd
// seconds before.
//
// So:
//   - a 404/410 from the proxy or a 3101 from the box for the track the queue
//     just pushed ends that track right away (the dead-track limit is
//     unchanged, a track that fails fast counts like one that never started);
//   - when the first tracks of a FOLDER KEY's queue all answer 404/410 while
//     the server itself answers, the queue stops at once and the speaker
//     display, the app and the phone page say the key must be saved again;
//   - an address the server just answered 404/410 for is never re-pushed by
//     the idle re-push.
//
// Everything here is event driven: the proxy and the box report, the watcher
// wakes. No timer, no poll of its own; the one HTTP probe is made per failed
// track, and only when the proxy did not already say what the server answered.

package webui

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/oled"
)

const (
	// boxBadURLBurst folds the box's 3101 frames into one. The firmware sends
	// them in pairs, 1 to 25 ms apart in every field sample; the second of a
	// pair must not count against the track pushed next.
	boxBadURLBurst = 150 * time.Millisecond
	// goneURLTTL is how long an address the server answered 404/410 for stays
	// known as gone to the idle re-push.
	goneURLTTL = 2 * time.Minute
	// goneURLKeep bounds that memory; a queue walks one track at a time.
	goneURLKeep = 32
	// trackProbeTimeout bounds the one probe of a failed track's address. The
	// server is on the LAN; a server that takes longer is not "answering".
	trackProbeTimeout = 3 * time.Second
	// staleFolderKeyTracks is how many tracks in a row, from the start of a
	// folder key's queue, must be gone from a server that still answers before
	// the key is called stale. One gone file is a deleted file; the first few
	// all gone is the folder having moved.
	staleFolderKeyTracks = 3
	// staleFolderKeyWindow is how long the app and the phone page are told
	// about a stale key. Saving the key again ends it sooner.
	staleFolderKeyWindow = time.Hour
)

// queueBadURLState is Server.badURL. mu guards every field.
type queueBadURLState struct {
	mu sync.Mutex
	// pushAt / pushURL: the queue track handed to the box last. A report
	// counts for that track only; a new push clears the evidence.
	pushAt  time.Time
	pushURL string
	// proxyAt / proxyCode: the stream proxy got 404/410 for pushURL.
	proxyAt   time.Time
	proxyCode int
	// boxAt: the box reported AUDIO_ERROR_BAD_URL after the push.
	boxAt time.Time
	// lastBox is the first 3101 of the latest burst (boxBadURLBurst).
	lastBox time.Time
	// wake tells the queue watcher there is evidence to look at.
	wake chan struct{}
	// gone is what a media server answered 404/410 for lately.
	gone map[string]goneURL
	// run is the queue started last, for the stale folder-key verdict.
	run badURLRun
	// stale is the last folder key found stale.
	stale staleFolderKey
}

type goneURL struct {
	code int
	at   time.Time
}

// badURLRun is what the current queue has shown so far.
type badURLRun struct {
	slot  int    // the folder key it was started from, 0 for any other queue
	name  string // that key's name, for the log and the debug state
	gone  int    // tracks in a row from the start that the server answered 404/410 for
	codes []int  // what the server answered, in order
	spoil bool   // a track played, or failed for another reason: no stale verdict
}

// staleFolderKey is the record of a folder key found pointing at files the
// media server no longer has. Exported fields are the debug-state shape.
type staleFolderKey struct {
	Slot          int    `json:"slot"`
	Name          string `json:"name,omitempty"`
	At            string `json:"at"`
	TracksTried   int    `json:"tracksTried"`
	Codes         []int  `json:"codes"`
	ServerAnswers bool   `json:"serverAnswers"`

	at       time.Time
	firstURL string // the key's first track when it was found stale
}

// badURLEvidence is one verdict taken from the state for the current track.
type badURLEvidence struct {
	code int    // 404/410 from the proxy, 0 when only the box spoke
	box  bool   // the box reported AUDIO_ERROR_BAD_URL
	url  string // the track it is about
}

// queueBadURLWake is the channel the queue watcher waits on besides its
// ticker. Created once, buffered by one: a report never blocks.
func (s *Server) queueBadURLWake() chan struct{} {
	s.badURL.mu.Lock()
	defer s.badURL.mu.Unlock()
	if s.badURL.wake == nil {
		s.badURL.wake = make(chan struct{}, 1)
	}
	return s.badURL.wake
}

func (s *Server) wakeQueueWatcher() {
	ch := s.queueBadURLWake()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// noteQueuePush records the queue track that is being handed to the box now
// and clears the evidence the previous one left. Called by pushStream right
// before the renderer call, after any native-station stop.
func (s *Server) noteQueuePush(url string) {
	s.badURL.mu.Lock()
	s.badURL.pushAt = time.Now()
	s.badURL.pushURL = url
	s.badURL.proxyAt, s.badURL.proxyCode, s.badURL.boxAt = time.Time{}, 0, time.Time{}
	s.badURL.mu.Unlock()
}

// resetBadURLRun starts the stale verdict over for a queue that starts now.
func (s *Server) resetBadURLRun(card recentCardCtx) {
	slot, _ := queueSlotCardNum(card.key)
	s.badURL.mu.Lock()
	s.badURL.run = badURLRun{slot: slot, name: card.name}
	s.badURL.mu.Unlock()
}

// noteQueueHeard records that a track of the current queue played audibly:
// the server delivers, so whatever fails later is not a moved folder.
func (s *Server) noteQueueHeard() {
	s.badURL.mu.Lock()
	s.badURL.run.spoil = true
	s.badURL.mu.Unlock()
}

func goneStatus(code int) bool {
	return code == http.StatusNotFound || code == http.StatusGone
}

// NoteStreamUpstreamFailure is the stream proxy's report of an upstream that
// failed (wired in cmd/agent). code is the HTTP status, 0 for a network-level
// failure. A 404/410 is remembered for the idle re-push, and when it is the
// queue track pushed last, the queue watcher is told.
func (s *Server) NoteStreamUpstreamFailure(url string, code int) {
	if url == "" || !goneStatus(code) {
		return
	}
	s.badURL.mu.Lock()
	s.noteGoneLocked(url, code)
	mine := !s.badURL.pushAt.IsZero() && s.badURL.pushURL == url
	if mine {
		s.badURL.proxyAt, s.badURL.proxyCode = time.Now(), code
	}
	s.badURL.mu.Unlock()
	if mine && s.queue != nil && s.queue.isActive() {
		s.wakeQueueWatcher()
	}
}

// NoteBoxError is the box's <errorUpdate> (wired in cmd/agent). Only 3101
// AUDIO_ERROR_BAD_URL matters here: the box could not fetch what it was
// handed. It counts for the queue track pushed last, once per burst.
func (s *Server) NoteBoxError(value, name, detail string) {
	if strings.TrimSpace(value) != "3101" || s.queue == nil || !s.queue.isActive() {
		return
	}
	now := time.Now()
	s.badURL.mu.Lock()
	if !s.badURL.lastBox.IsZero() && now.Sub(s.badURL.lastBox) < boxBadURLBurst {
		s.badURL.mu.Unlock()
		return
	}
	s.badURL.lastBox = now
	mine := !s.badURL.pushAt.IsZero()
	if mine {
		s.badURL.boxAt = now
	}
	s.badURL.mu.Unlock()
	if mine {
		s.wakeQueueWatcher()
	}
}

// takeQueueBadURL hands the watcher the evidence against the current track
// and clears it. The box's verdict counts at once. The proxy's 404 alone
// waits settle first: the box answers it with a 3101 within a second, and
// acting before that would leave the 3101 to land on the track pushed next.
func (s *Server) takeQueueBadURL(settle time.Duration) (badURLEvidence, bool) {
	s.badURL.mu.Lock()
	defer s.badURL.mu.Unlock()
	b := &s.badURL
	ev := badURLEvidence{url: b.pushURL}
	switch {
	case !b.boxAt.IsZero():
		ev.box = true
		if !b.proxyAt.IsZero() {
			ev.code = b.proxyCode
		}
	case !b.proxyAt.IsZero() && time.Since(b.proxyAt) >= settle:
		ev.code = b.proxyCode
	default:
		return badURLEvidence{}, false
	}
	b.boxAt, b.proxyAt, b.proxyCode = time.Time{}, time.Time{}, 0
	return ev, true
}

// noteGoneLocked remembers that url answered 404/410. Caller holds badURL.mu.
func (s *Server) noteGoneLocked(url string, code int) {
	b := &s.badURL
	if b.gone == nil {
		b.gone = map[string]goneURL{}
	}
	now := time.Now()
	for u, g := range b.gone {
		if now.Sub(g.at) >= goneURLTTL {
			delete(b.gone, u)
		}
	}
	b.gone[url] = goneURL{code: code, at: now}
	if len(b.gone) > goneURLKeep {
		urls := make([]string, 0, len(b.gone))
		for u := range b.gone {
			urls = append(urls, u)
		}
		sort.Slice(urls, func(i, j int) bool { return b.gone[urls[i]].at.Before(b.gone[urls[j]].at) })
		for _, u := range urls[:len(urls)-goneURLKeep] {
			delete(b.gone, u)
		}
	}
}

// urlRecentlyGone reports whether a media server answered 404/410 for the
// address a box URL plays (the URL itself, or the origin a /stream/raw
// wrapper carries) within goneURLTTL.
func (s *Server) urlRecentlyGone(boxURL string) (code int, ok bool) {
	if boxURL == "" {
		return 0, false
	}
	s.badURL.mu.Lock()
	defer s.badURL.mu.Unlock()
	for _, u := range []string{boxURL, unwrapRawStreamProxy(boxURL)} {
		if g, have := s.badURL.gone[u]; have && time.Since(g.at) < goneURLTTL {
			return g.code, true
		}
	}
	return 0, false
}

// probeTrackStatus asks the media server once what it answers for a track's
// address: the HTTP status, or an error when it does not answer at all. A
// one-byte range so a server that is there sends next to nothing.
func (s *Server) probeTrackStatus(ctx context.Context, rawURL string) (int, error) {
	if s.trackProbeFn != nil {
		return s.trackProbeFn(ctx, rawURL)
	}
	ctx, cancel := context.WithTimeout(ctx, trackProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// queueFailFast ends the current track on the evidence that it cannot be
// fetched. It reports whether the queue was stopped (the watcher returns).
func (s *Server) queueFailFast(gen int, start time.Time, dur time.Duration, ev badURLEvidence, deadInARow *int) (stopped bool) {
	it, ok := s.queue.current()
	if !ok || (ev.url != "" && it.URL != ev.url) {
		return false
	}
	code, answers := ev.code, ev.code != 0
	if code == 0 && isHTTPURL(it.URL) {
		if c, err := s.probeTrackStatus(s.queueCtx(), it.URL); err == nil {
			code, answers = c, true
		}
	}
	if goneStatus(code) {
		s.badURL.mu.Lock()
		s.noteGoneLocked(it.URL, code)
		s.badURL.mu.Unlock()
	}
	s.noteQueueBadURLSkip()
	if rec, stale := s.noteBadURLRunTrack(code, answers); stale {
		s.reportStaleFolderKey(rec, it.URL)
		s.stopQueue("key " + strconv.Itoa(rec.Slot) + " points at files the media server no longer has")
		return true
	}
	if queueDeadTrackStops(deadInARow) {
		s.logger.Info("queue watcher: the media server could not deliver several tracks in a row, stopping the queue",
			"deadInARow", *deadInARow, "status", code)
		s.stopQueue("the media server could not deliver several tracks in a row")
		return true
	}
	s.logger.Info("queue watcher: the media server could not deliver the track, skipping to the next",
		"title", it.Title, "status", code, "boxBadURL", ev.box, "serverAnswers", answers,
		"deadInARow", *deadInARow, "waitedMs", time.Since(start).Milliseconds())
	s.advanceAndPlayEnded(true, gen, "the media server could not deliver the track",
		trackEndReport{net: "bad-url", length: dur, elapsed: time.Since(start)})
	return false
}

// noteBadURLRunTrack adds one failed track to the run's stale verdict and
// reports a stale folder key when the run has reached it.
func (s *Server) noteBadURLRunTrack(code int, answers bool) (staleFolderKey, bool) {
	need := staleFolderKeyTracks
	if s.queue != nil {
		if items, _, _ := s.queue.contents(); len(items) > 0 && len(items) < need {
			need = len(items)
		}
	}
	s.badURL.mu.Lock()
	defer s.badURL.mu.Unlock()
	r := &s.badURL.run
	if r.slot <= 0 || r.spoil {
		return staleFolderKey{}, false
	}
	if !answers || !goneStatus(code) {
		// A server that did not answer, or answered something else, is not
		// a folder that moved: the dead-track limit handles it.
		r.spoil = true
		return staleFolderKey{}, false
	}
	r.gone++
	r.codes = append(r.codes, code)
	if r.gone < need {
		return staleFolderKey{}, false
	}
	now := time.Now()
	rec := staleFolderKey{
		Slot: r.slot, Name: r.name, At: now.Format(time.RFC3339),
		TracksTried: r.gone, Codes: append([]int(nil), r.codes...), ServerAnswers: true,
		at: now,
	}
	r.spoil = true // one verdict per run
	return rec, true
}

// reportStaleFolderKey records a stale folder key for the debug state, the
// app and the phone page, and shows it on the speaker display.
func (s *Server) reportStaleFolderKey(rec staleFolderKey, failedURL string) {
	if s.presets != nil {
		if p, ok := s.presets.Get(rec.Slot); ok && p.Type == "queue" && len(p.Items) > 0 {
			rec.firstURL = p.Items[0].URL
		}
	}
	s.badURL.mu.Lock()
	s.badURL.stale = rec
	s.badURL.mu.Unlock()
	s.logger.Warn("folder key: the media server answers, but the key's files are gone from it (new file addresses, for example after a USB stick swap); the folder has to be saved on the key again",
		"slot", rec.Slot, "name", rec.Name, "tracksTried", rec.TracksTried, "codes", rec.Codes, "lastURL", failedURL)
	if s.staleKeyNoticeFn != nil {
		s.staleKeyNoticeFn(rec.Slot)
		return
	}
	oled.Show(oled.FolderMoved(rec.Slot))
}

// StaleFolderKey is the folder key the app and the phone page should tell the
// owner to save again: found stale within staleFolderKeyWindow and not saved
// again since (the key still holds the tracks it held then).
func (s *Server) StaleFolderKey() (slot int, age time.Duration, ok bool) {
	s.badURL.mu.Lock()
	rec := s.badURL.stale
	s.badURL.mu.Unlock()
	if rec.Slot <= 0 || rec.at.IsZero() {
		return 0, 0, false
	}
	age = time.Since(rec.at)
	if age >= staleFolderKeyWindow {
		return 0, 0, false
	}
	if s.presets != nil {
		p, have := s.presets.Get(rec.Slot)
		if !have || p.Type != "queue" || len(p.Items) == 0 || (rec.firstURL != "" && p.Items[0].URL != rec.firstURL) {
			return 0, 0, false // saved again, or given something else
		}
	}
	return rec.Slot, age, true
}

// staleFolderKeyForensics is the last stale folder key for the debug state,
// nil when none was found since the agent started.
func (s *Server) staleFolderKeyForensics() any {
	s.badURL.mu.Lock()
	defer s.badURL.mu.Unlock()
	if s.badURL.stale.Slot <= 0 {
		return nil
	}
	rec := s.badURL.stale
	rec.Codes = append([]int(nil), rec.Codes...)
	return rec
}
