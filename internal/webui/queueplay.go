package webui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/upnp"
)

// presetItemsToQueue maps a queue preset's stored tracks to the play-queue item
// shape. Items without a URL are dropped, matching toQueueItems.
func presetItemsToQueue(in []presets.PresetItem) []queueItem {
	out := make([]queueItem, 0, len(in))
	for _, it := range in {
		if it.URL == "" {
			continue
		}
		out = append(out, queueItem{
			URL:      it.URL,
			Title:    it.Title,
			Art:      it.Art,
			Mime:     it.Mime,
			Duration: time.Duration(it.DurationSec) * time.Second,
			Artist:   it.Artist,
			Album:    it.Album,
		})
	}
	return out
}

// queueRecallInFlight bounds how long after a recall was armed the streamproxy
// may hold a box-native /stream/<slot> fetch waiting for the first track. It
// covers the gap between arming and the queue's first push; a queue that plays
// on keeps serving via queue.current() with recalling=false afterwards.
const queueRecallInFlight = 5 * time.Second

// armQueueRecall binds the play queue that is about to start to the preset slot
// it was recalled from, so a racing box-native /stream/<slot> fetch of a queue
// preset (whose stored StreamURL is empty) can hold for the first track instead
// of 404-ing into the previous station. See QueueLiveURL.
func (s *Server) armQueueRecall(slot int) {
	s.queueMu.Lock()
	s.queueRecallSlot = slot
	s.queueRecallAt = time.Now()
	s.queueMu.Unlock()
}

// QueueLiveURL resolves the current track URL for a queue preset the box has
// natively activated as /stream/<slot> (empty stored StreamURL). recalling=true
// tells the streamproxy it may keep holding: a recall for this slot is still in
// its landing window. Returns ("", false) for a slot that is not the active
// queue's, so a fetch for one slot never serves another slot's queue.
func (s *Server) QueueLiveURL(slot int) (url string, recalling bool) {
	s.queueMu.Lock()
	bound := s.queueRecallSlot
	inFlight := bound == slot && !s.queueRecallAt.IsZero() && time.Since(s.queueRecallAt) < queueRecallInFlight
	s.queueMu.Unlock()
	if bound != slot {
		return "", false
	}
	if it, ok := s.queue.current(); ok {
		return it.URL, inFlight
	}
	return "", inFlight
}

// RecallSlot handles a hardware preset-button press for a queue preset: if the
// slot holds a saved DLNA folder it starts the play-queue and returns true. A
// single library track is claimed too and played as the file itself.
// Otherwise it returns false and the caller falls back to the existing
// single-track recall. This keeps the queue logic in webui (it owns the queue)
// without entangling the gabbo handler in cmd/agent.
func (s *Server) RecallSlot(ctx context.Context, slot int) (handled bool) {
	if s.presets == nil {
		return false
	}
	p, ok := s.presets.Get(slot)
	if !ok {
		return false
	}
	if p.Type != "queue" {
		// A single library track is the queue's one-file sibling: the key
		// must play the file directly too (#1065, librarykey.go).
		return s.recallLibraryFileSlot(ctx, slot, p)
	}
	// The saved tracks carry the music server's address. When the router hands
	// that server a different lease, every folder key pointing at it goes dead at
	// once, which is what a static-lease workaround is really for (#977). Correct
	// the key from the media-server store before the queue is built.
	// Pressing the key of the folder that plays right now skips to its next
	// track, the way a listener expects a playlist key to behave (Jens,
	// 2026-10-16). Restarting the folder instead cut the song off and began it
	// again. Past the last track (no repeat) the folder starts over below.
	if s.queuePlaysSlot(slot) {
		if it, ok, err := s.queueSkip(true); err == nil && ok {
			s.logger.Info("preset slot recall (hardware): the key's folder already plays, next track",
				"slot", slot, "title", it.Title)
			return true
		} else if err != nil {
			s.logger.Warn("preset slot recall (hardware): next track failed, starting the folder again",
				"slot", slot, "err", err)
		}
	}
	s.healQueuePresetIfMoved(ctx, slot, &p)
	items := presetItemsToQueue(p.Items)
	if len(items) == 0 {
		return false
	}
	// Bind this slot as the in-flight queue recall BEFORE the queue starts, so a
	// box-native /stream/<slot> fetch that races ahead of the first track load can
	// hold for it (the ST20 source-switch race) instead of 404-ing into silence.
	s.armQueueRecall(slot)
	// Supersede any still-running hardware verify at claim time: startQueue's
	// own setLastPlay only bumps the generation once the first push SUCCEEDS,
	// and until then a stale verify loop from an earlier press would keep
	// re-pushing its old URL over this queue start.
	s.bumpRecallGen()
	s.ensureBoxReady(ctx)
	// A key press carries no mode choice of its own, so the sticky play mode
	// applies when one was ever chosen; the slot's stored shuffle and repeat-off
	// stay the defaults on a box whose owner never touched the toggles.
	shuffle, rep := p.Shuffle, repeatOff
	if ps, pr, ok := s.loadPlayMode(); ok {
		shuffle, rep = ps, pr
	}
	s.logger.Info("preset slot recall (hardware): queue", "slot", slot, "tracks", len(items), "shuffle", shuffle, "repeat", rep.String())
	// Record the saved folder as a Recently-played card (#220), keyed on the slot
	// so repeated recalls of the same preset group together.
	card := recentCardCtx{key: fmt.Sprintf("queue:slot:%d", slot), name: p.Name, art: p.Art, source: p.Source}
	// -1 = the user recalled the whole preset without picking a track, so with
	// shuffle on the queue may start anywhere (#490).
	if err := s.startQueue(ctx, items, -1, shuffle, rep, card); err != nil {
		s.logger.Warn("hardware queue recall failed", "slot", slot, "err", err)
	}
	return true
}

// queuePlaysSlot reports whether the active queue is the folder saved on slot:
// the queue was started from that key, or saved onto it while it played.
//
// The card of a queue a key started names that key for the queue's whole life,
// also after the key was given another folder (the Library's "Save folder as
// preset" while the old one plays). The card alone made a press on the key skip
// a track of the old folder instead of starting the new one (#1525), so it
// counts only while the key still holds the tracks that play. A stale card
// falls back to the keys that do hold them.
func (s *Server) queuePlaysSlot(slot int) bool {
	if !s.queue.isActive() || slot <= 0 {
		return false
	}
	s.queueMu.Lock()
	folderKey := s.queueFolder.key
	s.queueMu.Unlock()
	if n, ok := queueSlotCardNum(folderKey); ok && s.cardSlotHolds(n) {
		return slot == n || s.queueSavedSlotHolds() == slot
	}
	return s.queueKeySlot() == slot
}

// queueSlotCardRe matches the folder card of a queue that a key started.
var queueSlotCardRe = regexp.MustCompile(`^queue:slot:\d+$`)

// queueSlotCardNum returns the key a "queue:slot:<n>" card names.
func queueSlotCardNum(card string) (int, bool) {
	if !queueSlotCardRe.MatchString(card) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(card, "queue:slot:"))
	return n, err == nil
}

// cardSlotHolds reports whether key n still holds the folder that plays: a
// folder key with exactly the queue's tracks (#1525).
func (s *Server) cardSlotHolds(n int) bool {
	if n <= 0 || s.presets == nil || s.queue == nil {
		return false
	}
	cur, ok := s.presets.Get(n)
	if !ok || cur.Type != "queue" {
		return false
	}
	items, _, active := s.queue.contents()
	return active && presetHoldsQueueTracks(cur, items)
}

// NoteQueueSavedSlot records that the running queue was just saved on slot
// (#1217). The queue keeps its folder card; the saved slot is extra.
func (s *Server) NoteQueueSavedSlot(slot int) {
	s.queueMu.Lock()
	s.queueSavedSlot = slot
	s.queueMu.Unlock()
}

// queueSavedSlotHolds returns the key the running queue was saved on, or 0
// when none, or when that key has since been given something else: it counts
// only while the key still holds a folder with exactly the tracks that play.
// Shuffle is not compared, so toggling it during play keeps the key lit.
func (s *Server) queueSavedSlotHolds() int {
	s.queueMu.Lock()
	slot := s.queueSavedSlot
	s.queueMu.Unlock()
	if slot == 0 || s.presets == nil || s.queue == nil {
		return 0
	}
	cur, ok := s.presets.Get(slot)
	if !ok || cur.Type != "queue" {
		return 0
	}
	items, _, active := s.queue.contents()
	if !active || !presetHoldsQueueTracks(cur, items) {
		return 0
	}
	return slot
}

// presetHoldsQueueTracks reports whether a folder key holds exactly the tracks
// of a queue, in order (capped the way the key itself is capped).
//
// Items without a URL are skipped on both sides: presetItemsToQueue drops them
// when the key starts its queue, so a key holding one never matched its own
// queue and stayed dark next to it (#1525).
func presetHoldsQueueTracks(cur presets.Preset, items []queueItem) bool {
	if cur.Type != "queue" {
		return false
	}
	want := make([]string, 0, len(cur.Items))
	for _, it := range cur.Items {
		if it.URL != "" {
			want = append(want, it.URL)
		}
	}
	if len(want) == 0 {
		return false
	}
	n := 0
	for _, it := range items {
		if it.URL == "" {
			continue
		}
		if n >= len(want) || want[n] != it.URL {
			return false
		}
		n++
		if n == presets.MaxQueueItems {
			break
		}
	}
	return n == len(want)
}

// queueKeySlot is the key the running folder belongs to, 0 when none: the key
// it was saved on while it played, else a key that holds exactly these tracks.
//
// The second half is the folder started with "Play folder" that a key ALREADY
// holds. It played the key's own playlist and the key stayed dark, because the
// only link from a queue to a key was a save made while it played (#1366). The
// lowest such key wins when several hold the same folder.
func (s *Server) queueKeySlot() int {
	if slot := s.queueSavedSlotHolds(); slot != 0 {
		return slot
	}
	if s.presets == nil || s.queue == nil {
		return 0
	}
	items, _, active := s.queue.contents()
	if !active {
		return 0
	}
	best := 0
	for _, p := range s.presets.All() {
		if p.Slot <= 0 || (best != 0 && p.Slot >= best) {
			continue
		}
		if presetHoldsQueueTracks(p, items) {
			best = p.Slot
		}
	}
	return best
}

// Auto-advance tuning. The Bose box emits no native "track finished" event: a
// finished file and a deliberate stop both surface only as a now_playing
// STOP_STATE. The watcher therefore decides from progress (the box's reported
// position, or wall-clock elapsed when the box reports none) plus a wall-clock
// timer as a safety net, per the chosen "position + timer" strategy.
const (
	// queueDeadTrackLimit is how many tracks in a row the box may drop without
	// playing before the queue gives up. One is a bad file and worth skipping;
	// three in a row is the server having gone away, and racing through the
	// remaining fifty tracks to prove it helps nobody.
	queueDeadTrackLimit = 3
	// queueTakeoverPolls is how many polls in a row the box must report
	// another source playing before the watcher ends the queue (#1366). One
	// poll could catch a source change in flight; two, eight seconds apart,
	// are the user having moved on.
	queueTakeoverPolls = 2
)

// queueTiming is the watcher's clock: every duration the queue watcher and the
// lone-track end watch run on. It is a value on the server, not a set of
// package variables, so the tests can run the very same state machine at a
// faster clock (fastQueueTiming in queuetiming_test.go divides every field by
// one factor) without touching anything another test's watcher still reads.
// productionQueueTiming is what runs on the speaker.
type queueTiming struct {
	pollInterval time.Duration // how often the watcher reads now_playing
	endEpsilon   time.Duration // progress within this of the end == "ended"
	timerMargin  time.Duration // grace past the track length before the net trips
	stallTimeout time.Duration // a track that never starts is skipped
	// frozenTimeout advances when the box sits in PLAY_STATE with its
	// position frozen and the track length is UNKNOWN. Some DLNA servers (a
	// FRITZ!Box mediaserver) expose no duration AND the box reports no total, so
	// end==0 and the wall-clock net cannot fire; the box also finishes the file
	// but stays PLAY_STATE frozen at EOF instead of emitting STOP, so that path
	// cannot fire either, and a folder hung on its first track forever (#380,
	// #381). A position that has not moved for this long is a finished track.
	frozenTimeout time.Duration
	// overrunSlack is how far the box's position may sit past the track
	// length before STR concludes the length is wrong. The box reports whole
	// seconds and a server rounds its duration, so a second or so either side of
	// the end is noise; more than that is a box still playing audio the length
	// says does not exist (#1065).
	overrunSlack time.Duration
	// ownEndSlack is how close a still-climbing position must be to the
	// box's OWN reported total to count as "the box says this is the end".
	ownEndSlack time.Duration
	// zoneEndWindow and zoneEndPoll are the last stretch of a track on
	// a zone master or stereo pair (#1190). A single speaker freezes in
	// PLAY_STATE at the end of a file and the next 4 s poll advances it. A zone
	// master instead drops itself (and its slave) to STANDBY about 1 s after the
	// end of the file, inside one 4 s poll, and the watcher then reads a
	// power-off and ends the queue (field: pos 254/256 still climbing on the last
	// tick, STANDBY 0.8 s after the file ended, autoAdvances=0). Polling every
	// second for the last few seconds of a track lets the at-its-own-end rule
	// advance before that standby. Bounded to this window on purpose: the box is
	// never polled faster for a whole track.
	zoneEndWindow time.Duration
	zoneEndPoll   time.Duration
	// aheadSlack is how far the box's position may run ahead of the wall clock
	// since the push before STR stops believing it. A box cannot play faster
	// than real time; the slack covers whole-second rounding and the push
	// timestamp being taken a moment before the box starts. Seen on a FRITZ!Box
	// MP3: the box reported 120 s at most 16 s after the push and 180 s at the
	// first poll, froze there, and the frozen-position net stopped a track that
	// was still playing (and learned 120 s as its length).
	aheadSlack time.Duration
	// hintSlack is how close a frozen position must sit to an estimated
	// length (a bare bitrate x size guess, never handed to the box) for that
	// estimate to confirm the end after a single non-climbing poll.
	hintSlack time.Duration
	// alignEpsilon is how far past the computed end of a track the one
	// re-timed poll lands (see alignToEnd).
	alignEpsilon time.Duration
	// badURLSettle is how long a 404 the stream proxy got for the current
	// track waits for the box's own AUDIO_ERROR_BAD_URL before it ends the
	// track by itself (queuebadurl.go). The box's error follows the proxy's
	// 502 within a second; acting sooner would let it land on the next track.
	badURLSettle time.Duration
}

// productionQueueTiming is the clock the watcher runs on on the speaker.
var productionQueueTiming = queueTiming{
	pollInterval:  4 * time.Second,
	endEpsilon:    12 * time.Second,
	timerMargin:   6 * time.Second,
	stallTimeout:  25 * time.Second,
	frozenTimeout: 15 * time.Second,
	overrunSlack:  2 * time.Second,
	ownEndSlack:   1 * time.Second,
	zoneEndWindow: 5 * time.Second,
	zoneEndPoll:   1 * time.Second,
	aheadSlack:    3 * time.Second,
	hintSlack:     3 * time.Second,
	alignEpsilon:  300 * time.Millisecond,
	badURLSettle:  1500 * time.Millisecond,
}

// queueTimes is the watcher's clock: the production values unless a test
// installed a faster one.
func (s *Server) queueTimes() *queueTiming {
	if s.queueTiming != nil {
		return s.queueTiming
	}
	return &productionQueueTiming
}

// inEndStretch reports whether the track is within zoneEndWindow of
// its end, judged by the box's position when it reports one, else by the wall
// clock since the push. A position that ran well past the end (a wrong length,
// #1065) is no longer "near the end", so a misjudged length cannot keep the fast
// poll running for the rest of the track, and a box frozen on its last second
// falls back to the normal poll once the wall clock is well past the end.
func (qt *queueTiming) inEndStretch(pos, end, elapsed time.Duration) bool {
	if end <= 0 || elapsed > end+4*qt.zoneEndWindow {
		return false
	}
	at := pos
	if at <= 0 {
		at = elapsed
	}
	left := end - at
	return left <= qt.zoneEndWindow && left >= -qt.zoneEndWindow
}

// watchInterval is how long the watcher waits before its next poll: the
// one-second end-of-track poll only for a box in a zone or stereo pair inside
// the last stretch of a track, the normal four seconds everywhere else.
func (qt *queueTiming) watchInterval(inZone, inEndStretch bool) time.Duration {
	if inZone && inEndStretch {
		return qt.zoneEndPoll
	}
	return qt.pollInterval
}

// positionRunsAhead reports whether a position the box reported elapsed after
// the push is further into the track than it can have played. Such a position
// cannot time the end of the track and must not be learned as its length.
func (qt *queueTiming) positionRunsAhead(pos, elapsed time.Duration) bool {
	return pos > elapsed+qt.aheadSlack
}

// alignToEnd is the one re-timed poll at the end of a track. The watcher polls
// every pollInterval from the push, so the poll that sees a track's end lands
// anywhere up to pollInterval after it, and that wait is silence between two
// album tracks. When the next regular poll would land after the end, the
// ticker is moved once so the next poll lands just past it instead; after that
// poll the watcher goes back to the normal interval. This moves a poll, it
// does not add a timer or poll the box faster. It reports false when the end
// is not inside the coming interval.
func (qt *queueTiming) alignToEnd(pos, end, interval time.Duration) (time.Duration, bool) {
	remaining := end - pos
	if pos <= 0 || end <= 0 || remaining <= 0 {
		return 0, false
	}
	d := remaining + qt.alignEpsilon
	if d >= interval {
		return 0, false
	}
	return d, true
}

// queueBoxInZone is boxInZone behind the watcher's test seam.
func (s *Server) queueBoxInZone() bool {
	if s.queueInZoneFn != nil {
		return s.queueInZoneFn()
	}
	return s.boxInZone()
}

// pushStream sends one stream to the box, choosing direct play (a plain-HTTP
// library file the box can range-read) vs the loopback proxy (radio / HTTPS)
// exactly like handlePlay, and records it as the last play. The caller must
// hold boxCmdMu.
func (s *Server) pushStream(ctx context.Context, url, title, art, mime, artist string, dur time.Duration) error {
	playDirect := mime != "" && isPlainHTTPURL(url)
	playURL := boxurl.RawStream(url)
	if playDirect {
		playURL = url
	}
	// A folder started while the speaker plays a native station must stop
	// that station first, or the firmware swallows the push (#1065).
	err := s.pushOverNativeStation(ctx, title, func() error {
		// What the box is handed now is what its next 404 or BAD_URL is
		// about (queuebadurl.go); after the native stop, so a failing
		// native station's own error cannot land on this track.
		s.noteQueuePush(url)
		if mime != "" {
			// Tell the speaker what it is being handed. A direct library file has a
			// length and a server that serves byte ranges, and saying so is what
			// gives the box a real total time and a position it can return to; a
			// proxied stream has neither, so it keeps the old stream-shaped metadata.
			return s.renderer.PlayURLTrack(ctx, playURL, title, art, mime, upnp.TrackMeta{
				Duration: dur,
				Seekable: playDirect,
			})
		}
		return s.renderer.PlayURL(ctx, playURL, title, art)
	})
	if err != nil {
		return err
	}
	if playDirect {
		s.trackLens.put(url, dur) // #978: a single-track key of this file can use it
	}
	s.setLastPlay(playURL, title, art, mime)
	// Recently-played (#220): hang this queue track under the active folder card.
	// No-op outside a queue (pushStream is queue-only) or before a card is set.
	s.recentNoteQueueTrack(title, artist)
	s.recentMu.Lock()
	folder := s.recentQueueCard.name
	s.recentMu.Unlock()
	s.panelLibraryTrack(folder, artist, title)
	return nil
}

func (s *Server) queueCtx() context.Context {
	if s.baseCtx != nil {
		return s.baseCtx
	}
	return context.Background()
}

// startQueue replaces the queue with items and starts playing from start. card
// carries the Recently-played folder identity (#220); a zero card skips recording.
func (s *Server) startQueue(ctx context.Context, items []queueItem, start int, shuffle bool, rep repeatMode, card recentCardCtx) error {
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	return s.startQueueLocked(ctx, items, start, shuffle, rep, card)
}

// startQueueLocked is startQueue for callers that already hold boxCmdMu (e.g. a
// preset slot recall, which takes the lock for the whole handler).
func (s *Server) startQueueLocked(ctx context.Context, items []queueItem, start int, shuffle bool, rep repeatMode, card recentCardCtx) error {
	if s.renderer == nil {
		return errors.New("renderer not configured")
	}
	if len(items) == 0 {
		return errors.New("empty queue")
	}
	// A sleeping speaker is kept silent through its own resume until the
	// first track has taken over (#1557, newplaywake.go).
	playSent := s.ensureBoxReadyForNewPlayRead(ctx)
	defer playSent()
	s.queue.load(items, start, shuffle, rep)
	s.queueMu.Lock()
	s.queueFolder = card
	s.queueSavedSlot = 0 // a new folder is on no key until it is saved on one
	s.queueMu.Unlock()
	s.resetBadURLRun(card)
	it, ok := s.queue.current()
	if !ok {
		return errors.New("empty queue")
	}
	// A queue start is an explicit user play: clear the stop latches and anchor
	// the standby-flip discriminator (#419).
	s.NoteUserPlay()
	// Record the folder as a Recently-played card before the first push, so that
	// push (and every auto-advance after it) is hung under it. The replay target
	// and cover fall back to the first track when the caller left them empty.
	if card.key != "" {
		if card.url == "" {
			card.url = it.URL
		}
		if card.art == "" {
			card.art = it.Art
		}
		// The stored length belongs to the replay URL: the first track's own
		// length only when the card falls back to it.
		cardDur := 0
		if card.url == it.URL {
			cardDur = int(it.Duration / time.Second)
		}
		s.recentNoteQueueCard(card.key, card.name, card.art, card.url, it.Mime, cardDur)
	} else {
		s.recentClearQueueCard()
	}
	s.noteQueueStart(len(items), start, shuffle, rep, it.Title, it.Duration)
	if err := s.pushStream(ctx, it.URL, it.Title, it.Art, it.Mime, it.Artist, it.Duration); err != nil {
		s.noteQueueEnd("the first track could not be pushed to the box")
		return err
	}
	s.setQueueTiming(it.Duration)
	s.ensureWatcher()
	s.prearmNextLength()
	// A folder started from the Library names its server and carries the
	// address that server answers at now: put that server's keys on it (#1366).
	s.healQueuePresetsFromLiveQueue(card, items)
	return nil
}

// setQueueTiming records when the current track started and its length, bumping
// the generation so the watcher resets its per-track progress tracking.
func (s *Server) setQueueTiming(dur time.Duration) {
	s.queueMu.Lock()
	s.queueTrackStart = time.Now()
	s.queueTrackDur = dur
	s.queueGen++
	s.queueMu.Unlock()
}

// ensureWatcher starts the auto-advance watcher if it is not already running.
func (s *Server) ensureWatcher() {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if s.queueCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(s.queueCtx())
	s.queueCancel = cancel
	go s.runQueueWatcher(ctx)
}

func (s *Server) cancelWatcher() {
	s.queueMu.Lock()
	if s.queueCancel != nil {
		s.queueCancel()
		s.queueCancel = nil
	}
	s.queueMu.Unlock()
}

// stopQueue deactivates the queue and stops the watcher. Called on a single
// play, a stop, or when the queue runs out.
//
// why names the caller, and it is not decoration: a queue that ended because
// somebody pressed stop, because the box went into standby, because a station
// was started instead and because a hardware key was pressed all left exactly
// the same trace before #960, which was nothing at all.
func (s *Server) stopQueue(why string) {
	s.noteQueueEnd(why)
	s.queue.clear()
	s.cancelWatcher()
	s.recentClearQueueCard()
}

// StopQueueForSpotify ends a running library queue because the Spotify app
// just pointed the speaker at the Spotify stream. A no-op without a queue.
//
// Every other way off a queue (another preset, a single track, stop, standby)
// already ended it; the Spotify app takeover did not, so the queue watcher kept
// timing the library track underneath Spotify and, once that track's length
// had passed, its wall-clock net pushed the next library track over the
// Spotify music. Meanwhile the app kept showing the folder key as playing
// (#1301: "queue advance ... net=wall-clock" right after the Spotify stream
// dropped, with the queue's card still active).
func (s *Server) StopQueueForSpotify() {
	if s.queue == nil || !s.queue.isActive() {
		return
	}
	s.stopQueue("the Spotify app took over the speaker")
}

// stopQueueIfActive ends a running library queue because the user started
// something on the speaker that is not a queue: a Pandora station, the AUX or
// Bluetooth input, a box-native preset key. A no-op without a queue.
//
// The plain play paths (a station, a track, a non-queue preset) always called
// stopQueue. These go straight to the speaker's own /select or key, so the
// queue stayed active underneath the new source: its watcher pushed the next
// library track over it once the old track's length had passed, and the
// queue's folder key stayed highlighted (#1366).
func (s *Server) stopQueueIfActive(why string) {
	if s.queue == nil || !s.queue.isActive() {
		return
	}
	s.stopQueue(why)
}

// advanceAndPlay moves to the next track (natural end vs manual skip differ on
// repeatOne) and plays it, or stops the queue at the end. The caller must NOT
// hold boxCmdMu. gen is the queue generation the advance decision was based
// on: the watcher decides from a now_playing poll BEFORE this lock, and a user
// starting a new queue/track meanwhile holds boxCmdMu through wake+push (up to
// several seconds) and bumps queueGen. Acting on the stale decision would
// advance the NEW queue and cut off the first track the user just chose, so
// the advance re-checks the generation once it holds the lock and aborts when
// superseded.
func (s *Server) advanceAndPlay(natural bool, gen int, why string) {
	s.advanceAndPlayEnded(natural, gen, why, trackEndReport{net: "stop-frame"})
}

// advanceAndPlayEnded is advanceAndPlay with the ending track's numbers, so the
// advance log line says which track ended, where the box was in it, and which
// net decided it (#1065: a track cut at 33 s left no trace of why).
func (s *Server) advanceAndPlayEnded(natural bool, gen int, why string, ended trackEndReport) {
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	s.queueMu.Lock()
	superseded := s.queueGen != gen
	s.queueMu.Unlock()
	if superseded {
		s.logger.Info("queue advance: a new queue/track started while this advance waited, standing down", "why", why)
		return
	}
	endedTitle := s.queueCurrentTitle()
	var (
		it queueItem
		ok bool
	)
	if natural {
		it, ok = s.queue.advanceNatural()
	} else {
		it, ok = s.queue.next()
	}
	if !ok {
		s.logger.Info("queue: the last track ended",
			append([]any{"why", why, "natural", natural}, ended.logAttrs(endedTitle)...)...)
		// Queue exhausted. On a NATURAL end the box is frozen in PLAY_STATE on the
		// last track (it finished the file but never emitted STOP, #380), so the
		// app/remote/display keep showing it "playing" until standby. Stop the box
		// so now_playing goes STOP_STATE and every UI updates at once. Mirror
		// handleStop (NoteUserStop + Stop) so the 6s guard suppresses an auto
		// re-push. A user-driven skip past the end (natural=false) already left the
		// box stopped, so only the natural case needs this.
		if natural {
			s.NoteUserStop()
			if err := s.renderer.Stop(s.queueCtx()); err != nil {
				s.logger.Warn("queue end: stopping the box failed", "err", err)
			}
			s.noteQueueEnd("played to the end of the list")
		} else {
			s.noteQueueEnd("skipped past the end of the list")
		}
		s.cancelWatcher()
		return
	}
	s.ClearUserStop()
	if err := s.pushStream(s.queueCtx(), it.URL, it.Title, it.Art, it.Mime, it.Artist, it.Duration); err != nil {
		s.noteQueuePushFailed()
		s.logger.Warn("queue advance: play failed", "why", why, "title", it.Title, "err", err)
		return
	}
	s.noteQueueAdvance(natural, why, it.Title, it.Duration, endedTitle, ended)
	s.setQueueTiming(it.Duration)
	s.prearmNextLength()
}

// retryQueueTrackStart wakes the speaker and hands it the current track once
// more, for a track it never started. Same checks as an advance: it stands
// down when a newer queue or track took over, and when the user stopped the
// box. It reports whether the track was handed over again.
func (s *Server) retryQueueTrackStart(gen int, trackStart time.Time) bool {
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	s.queueMu.Lock()
	superseded := s.queueGen != gen
	s.queueMu.Unlock()
	if superseded || !s.queue.isActive() || s.userStoppedRecently() {
		return false
	}
	it, ok := s.queue.current()
	if !ok {
		return false
	}
	s.logger.Info("queue watcher: the speaker never started the track, waking it and handing it over once more",
		"title", it.Title, "waitedSec", int(time.Since(trackStart).Seconds()))
	ctx := s.queueCtx()
	s.ensureBoxReady(ctx)
	s.ClearUserStop()
	if err := s.pushStream(ctx, it.URL, it.Title, it.Art, it.Mime, it.Artist, it.Duration); err != nil {
		s.logger.Warn("queue watcher: the second start failed too", "title", it.Title, "err", err)
		return false
	}
	s.setQueueTiming(it.Duration)
	return true
}

// queueSourceTakenOver reports whether one now_playing poll shows the speaker
// playing a source other than the queue's own (#1366). Deliberately narrow,
// because a false positive ends a queue the user is listening to:
//   - only after the box was seen on the queue's UPNP source, so a speaker that
//     has not switched to the queue yet is left to the start retry;
//   - only a named source that is not one of STR's own states between tracks
//     ("", UPNP, STANDBY, INVALID_SOURCE: boxOwnedSource), so a poll error or
//     a torn-down source never counts;
//   - only while that source plays or buffers, so a source the box merely
//     shows while idle never counts.
func queueSourceTakenOver(sawQueueSource bool, src, state string) bool {
	if !sawQueueSource || !boxOwnedSource(strings.ToUpper(strings.TrimSpace(src))) {
		return false
	}
	return state == "PLAY_STATE" || state == "BUFFERING_STATE"
}

// queueDeadTrackStops counts one more track the speaker did not play, either
// dropped (a torn-down source) or never started, and reports whether the run
// has reached queueDeadTrackLimit and the queue should stop.
func queueDeadTrackStops(deadInARow *int) bool {
	*deadInARow++
	return *deadInARow >= queueDeadTrackLimit
}

// queueCurrentTitle is the title of the track the queue is on, empty when none.
func (s *Server) queueCurrentTitle() string {
	if it, ok := s.queue.current(); ok {
		return it.Title
	}
	return ""
}

// queueSkip plays the next (forward) or previous track on demand.
func (s *Server) queueSkip(forward bool) (queueItem, bool, error) {
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	if !s.queue.isActive() {
		return queueItem{}, false, nil
	}
	endedTitle := s.queueCurrentTitle()
	var (
		it queueItem
		ok bool
	)
	if forward {
		it, ok = s.queue.next()
	} else {
		it, ok = s.queue.prev()
	}
	if !ok {
		s.noteQueueEnd("skipped past the end of the list")
		s.cancelWatcher()
		return queueItem{}, false, nil
	}
	s.ClearUserStop()
	if err := s.pushStream(s.queueCtx(), it.URL, it.Title, it.Art, it.Mime, it.Artist, it.Duration); err != nil {
		s.noteQueuePushFailed()
		return queueItem{}, false, err
	}
	s.noteQueueAdvance(false, "next/previous pressed", it.Title, it.Duration, endedTitle, trackEndReport{net: "manual"})
	s.setQueueTiming(it.Duration)
	s.prearmNextLength()
	return it, true, nil
}

// runQueueWatcher polls now_playing while a queue is active and advances when
// the current track ends. It exits when the queue is no longer active or ctx is
// cancelled.
func (s *Server) runQueueWatcher(ctx context.Context) {
	qt := s.queueTimes()
	ticker := time.NewTicker(qt.pollInterval)
	defer ticker.Stop()
	var (
		gen       int
		lastPos   time.Duration
		lastPosAt time.Time     // when lastPos last increased (frozen-position net)
		obsTotal  time.Duration // largest total the box reported for this track
		sawPlay   bool
		// overrunWarned rate-limits the "length is shorter than what the speaker
		// plays" warning to once per track.
		overrunWarned bool
		// deadInARow counts tracks the box dropped without playing them. It
		// deliberately survives the per-track reset below: one dead track is a bad
		// file, several in a row is a server that has gone away, and only the
		// second reading is worth stopping for.
		deadInARow int
		// startRetried: the one start retry of the current run of silent
		// tracks has been used. Audible progress gives the next run its own.
		startRetried bool
		// interval is the ticker's current period. zoneChecked / inZone cache the
		// zone check once per track: boxInZone may read /getZone over HTTP, so it
		// runs only when a track first enters its last stretch, never per poll.
		interval    = qt.pollInterval
		zoneChecked bool
		inZone      bool
		// sawQueueSource: the box has reported the queue's UPNP source at
		// least once. Only after that can a different source be a takeover;
		// before it, the box simply has not switched to the queue yet (a Wave
		// still on its own source, #1322), which the start retry handles.
		sawQueueSource bool
		// takeoverPolls counts polls in a row with another source playing.
		takeoverPolls int
		// ranAhead: this track's position was once further than the box can
		// have played (positionRunsAhead); logged once per track.
		ranAhead bool
		// aligned: the one end-of-track re-timing (alignToEnd) was used for
		// this track.
		aligned bool
		// retimed: the ticker runs on the one-off alignToEnd period. The tick
		// after it puts the normal period back first thing, so a branch that
		// continues before the interval check below cannot keep it.
		retimed bool
		// hint is the current track's estimated length when it has no real
		// one (prearmNextLength), 0 when none.
		hint time.Duration
		// wake carries the stream proxy's and the box's reports that the
		// track just pushed cannot be fetched (queuebadurl.go). woke: this
		// round was such a report, not a tick, so it looks at the evidence
		// and never polls the box.
		wake = s.queueBadURLWake()
		woke bool
	)
	for {
		woke = false
		select {
		case <-ctx.Done():
			s.logger.Debug("queue watcher: stopped")
			return
		case <-ticker.C:
		case <-wake:
			woke = true
		}
		if retimed && !woke {
			retimed = false
			interval = qt.pollInterval
			ticker.Reset(interval)
		}
		if !s.queue.isActive() {
			s.logger.Debug("queue watcher: the queue is no longer active, stopping")
			return
		}
		s.queueMu.Lock()
		curGen := s.queueGen
		start := s.queueTrackStart
		dur := s.queueTrackDur
		s.queueMu.Unlock()
		if curGen != gen {
			gen, lastPos, lastPosAt, obsTotal, sawPlay, overrunWarned = curGen, 0, time.Time{}, 0, false, false
			zoneChecked, inZone = false, false
			ranAhead, aligned, hint = false, false, 0
		}
		// A track the media server no longer has, or the box could not
		// fetch, ends now instead of after the stall timeout. Only before it
		// was heard: a track that played and then failed is the teardown
		// path's, below.
		if lastPos == 0 {
			if ev, ok := s.takeQueueBadURL(qt.badURLSettle); ok {
				if s.queueFailFast(curGen, start, dur, ev, &deadInARow) {
					return
				}
				continue
			}
		}
		if woke {
			continue
		}
		if dur == 0 && hint == 0 {
			if it, ok := s.queue.current(); ok {
				hint = s.lengthHints.get(it.URL)
			}
		}

		ps, pos, total, standby, tornDown, src := s.pollNowPlaying()
		if strings.EqualFold(src, "UPNP") {
			sawQueueSource = true
		}
		// Another source is playing (#1366): a Pandora station, AUX, Bluetooth
		// or a native preset started on the speaker itself or from another app.
		// That is the user moving on, not a track ending. Advancing here pushed
		// the next library track over the new source once the old track's
		// length had passed (the wall-clock net below), and the queue's key
		// stayed lit the whole time.
		if queueSourceTakenOver(sawQueueSource, src, ps) {
			takeoverPolls++
			if takeoverPolls >= queueTakeoverPolls {
				s.logger.Info("queue watcher: another source is playing on the speaker, ending the queue (not advancing)",
					"source", src, "state", ps, "polls", takeoverPolls,
					"elapsedSec", int(time.Since(start).Seconds()))
				s.stopQueue("another source took over the speaker (" + src + ")")
				return
			}
			continue // never time or advance a track while another source plays
		}
		takeoverPolls = 0
		if standby {
			// The box was powered off mid-queue (top switch, remote, or the app's
			// Standby button -> now_playing source=STANDBY). A standby is never a
			// track end: stop the queue so STR does not advance and re-push the next
			// track, which would wake the box back up and resume playing (#219).
			// The last known position goes into the line: a standby right at the end
			// of a track on a zone master is the #1190 signature, not a power press.
			standbyEnd := dur
			if obsTotal > standbyEnd {
				standbyEnd = obsTotal
			}
			s.logger.Info("queue watcher: box entered standby, stopping queue (not advancing)",
				"lastPosSec", int(lastPos.Seconds()), "boxTotalSec", int(obsTotal.Seconds()),
				"trackSec", int(standbyEnd.Seconds()), "elapsedSec", int(time.Since(start).Seconds()),
				"pollSec", interval.Seconds())
			s.stopQueue("the box went into standby")
			return
		}
		// The box dropped the source while we believed it was playing. That is the
		// firmware's answer to a stream that starved or was refused, and it is the
		// opposite of a track ending: nothing was heard. Without this the wall-clock
		// net below sits out the REST of the track's nominal length in silence and
		// then logs the dead track as a natural end (11 m 43 s of silence inside one
		// 33-minute queue, 2026-09-28), and with repeat=all that never stops.
		if tornDown && sawPlay {
			if queueDeadTrackStops(&deadInARow) {
				s.logger.Info("queue watcher: the speaker dropped several tracks in a row without playing them, stopping the queue",
					"deadInARow", deadInARow)
				s.stopQueue("the music server stopped delivering; several tracks in a row did not play")
				return
			}
			s.logger.Info("queue watcher: the speaker dropped this track without playing it, skipping to the next",
				"deadInARow", deadInARow, "trackSec", int(dur.Seconds()))
			s.advanceAndPlay(true, curGen, "the speaker dropped the track without playing it")
			continue
		}
		if total > obsTotal {
			obsTotal = total // remember it even after the box later reports 0
		}
		// Track length: the queue item's duration, or the box's reported total
		// when the item carried none (a DLNA server, e.g. Synology, that did not
		// expose duration in its metadata leaves dur==0). #219
		end := dur
		if obsTotal > end {
			end = obsTotal
		}

		// The poll rate for the next tick (#1190). Only a playing track in its
		// last stretch on a zone master / stereo pair polls every second. A paused
		// or stopped box stays on the normal poll however close to the end it is.
		posNow := pos
		if lastPos > posNow {
			posNow = lastPos
		}
		stretch := ps == "PLAY_STATE" && qt.inEndStretch(posNow, end, time.Since(start))
		if stretch && !zoneChecked {
			zoneChecked = true
			inZone = s.queueBoxInZone()
			if inZone {
				s.logger.Info("queue watcher: speaker is in a group or stereo pair and the track is nearly over, polling every second until it ends",
					"posSec", int(posNow.Seconds()), "trackSec", int(end.Seconds()))
			}
		}
		if want := qt.watchInterval(inZone, stretch); want != interval {
			interval = want
			ticker.Reset(interval)
		}

		climbedNow := false
		switch ps {
		case "PLAY_STATE", "BUFFERING_STATE":
			sawPlay = true
			// Audible progress means the server is answering again, so a previous
			// run of dead tracks is over.
			if pos > 0 {
				deadInARow = 0
				startRetried = false
				s.noteQueueHeard()
			}
			// Track the MAX position and when it last advanced: the box's position
			// climbs each second while playing and then freezes at EOF, so a
			// position that stops moving is the finished-track signal for the
			// unknown-length case below.
			if pos > lastPos {
				lastPos = pos
				lastPosAt = time.Now()
				climbedNow = true
				if !ranAhead && qt.positionRunsAhead(pos, time.Since(start)) {
					ranAhead = true
					s.logger.Info("queue watcher: position runs ahead of real time, not timing the end by it",
						"title", s.queueCurrentTitle(), "posSec", int(pos.Seconds()),
						"elapsedSec", int(time.Since(start).Seconds()), "trackSec", int(end.Seconds()))
				}
			}
			// Do NOT continue: fall through to the wall-clock net. Some renderers
			// (seen on the ST20 with direct-played NAS files) finish a finite file
			// but stay in PLAY_STATE with the position frozen at the end instead of
			// reporting STOP_STATE, so end detection cannot rely on the state alone.
		case "PAUSE_STATE":
			continue // paused: never advance, and freeze the wall-clock timer
		case "STOP_STATE":
			if !sawPlay {
				continue // track has not started yet (gap between tracks)
			}
			prog := lastPos
			if prog == 0 {
				prog = time.Since(start) // box reported no position; use elapsed
			}
			if qt.nearEnd(prog, end) {
				rep := s.trackEndSnapshot(sawPlay, ps, start, dur, obsTotal, lastPos, lastPosAt).report(endNetNone)
				rep.net = "stop-frame"
				s.advanceAndPlayEnded(true, curGen, "the box reported the track stopped at its end", rep)
			} else {
				// Stopped well before the end: a real stop, not an end.
				s.logger.Info("queue watcher: the box stopped well before the end of the track, treating it as a stop",
					"progressSec", int(prog.Seconds()), "trackSec", int(end.Seconds()))
				s.stopQueue("the box stopped in the middle of a track")
				return
			}
			continue
		default:
			// No usable status this tick (poll error or an idle box).
			if !sawPlay && time.Since(start) >= qt.stallTimeout {
				// The first silence of a run gets one more wake and the same
				// track again before anything is skipped (#1322): the Wave
				// that never took the first push started fine on the second.
				if !startRetried && s.retryQueueTrackStart(curGen, start) {
					startRetried = true
					continue
				}
				// A track that never started counts as a dead one too. A speaker
				// that never takes the stream (a Wave left on its own source,
				// #1322) otherwise walked the whole folder in silence and filled
				// Recently played with tracks nobody heard.
				if queueDeadTrackStops(&deadInARow) {
					s.logger.Info("queue watcher: several tracks in a row never started playing, stopping the queue",
						"deadInARow", deadInARow)
					s.stopQueue("the speaker did not start several tracks in a row")
					return
				}
				s.logger.Info("queue watcher: the track never started playing, skipping to the next",
					"deadInARow", deadInARow, "waitedSec", int(time.Since(start).Seconds()))
				s.advanceAndPlayEnded(true, curGen, "the track never started playing",
					trackEndReport{net: "never-started", length: dur, elapsed: time.Since(start)})
				continue
			}
		}

		if !overrunWarned && !ranAhead && dur > 0 && lastPos > dur+qt.overrunSlack {
			overrunWarned = true
			s.logger.Warn("queue watcher: the server's track length is shorter than what the speaker plays",
				"title", s.queueCurrentTitle(), "lengthSec", int(dur.Seconds()),
				"lastPosSec", int(lastPos.Seconds()), "boxTotalSec", int(obsTotal.Seconds()),
				"elapsedSec", int(time.Since(start).Seconds()))
		}

		// The wall-clock and frozen-position nets, reached on PLAY_STATE and on an
		// unknown status. trackEndNet holds the rules.
		in := s.trackEndSnapshot(sawPlay, ps, start, dur, obsTotal, lastPos, lastPosAt)
		in.ranAhead, in.hint = ranAhead, hint
		net := trackEndNet(in)
		if stretch {
			// Forensics for the last ticks of a track only (#1190): what the end
			// rules saw, so a missed advance can be told apart from a late one.
			climbing, atOwnEnd := in.endSignals()
			s.logger.Debug("queue watcher: end of track",
				"state", ps, "posSec", int(lastPos.Seconds()), "boxTotalSec", int(obsTotal.Seconds()),
				"trackSec", int(end.Seconds()), "elapsedMs", in.elapsed.Milliseconds(),
				"climbing", climbing, "atOwnEnd", atOwnEnd, "inZone", inZone,
				"pollSec", interval.Seconds(), "decision", net.String())
		}
		if net != endNetNone {
			s.advanceAndPlayEnded(true, curGen, net.why(), in.report(net))
			continue
		}
		// The end falls inside the coming interval: land the next poll just
		// past it instead of up to a whole interval later. Once per track, only
		// on a position that climbed in real time on this very poll, and not on
		// a zone master, which already polls every second near the end.
		if !aligned && !inZone && climbedNow && !ranAhead && ps == "PLAY_STATE" {
			target := end
			if target == 0 {
				target = hint
			}
			if d, ok := qt.alignToEnd(lastPos, target, interval); ok {
				aligned, retimed = true, true
				interval = d
				ticker.Reset(interval)
				s.logger.Debug("queue watcher: timing the next poll to the end of the track",
					"posSec", int(lastPos.Seconds()), "trackSec", int(target.Seconds()),
					"nextPollMs", d.Milliseconds())
			}
		}
	}
}

// trackEndSnapshot packs what the watcher knows about the current track into a
// trackEndInput.
func (s *Server) trackEndSnapshot(sawPlay bool, ps string, start time.Time, dur, obsTotal, lastPos time.Duration, lastPosAt time.Time) trackEndInput {
	in := trackEndInput{
		sawPlay:  sawPlay,
		playing:  ps == "PLAY_STATE",
		elapsed:  time.Since(start),
		dur:      dur,
		obsTotal: obsTotal,
		lastPos:  lastPos,
		timing:   s.queueTimes(),
	}
	if !lastPosAt.IsZero() {
		in.sinceLastPos = time.Since(lastPosAt)
	}
	return in
}

// endNet names which detector decided a track was over.
type endNet int

const (
	endNetNone endNet = iota
	// endNetWallClock: the track's length (plus a margin) elapsed.
	endNetWallClock
	// endNetFrozen: the box sat on PLAY_STATE with its position not moving.
	endNetFrozen
)

func (n endNet) String() string {
	switch n {
	case endNetWallClock:
		return "wall-clock"
	case endNetFrozen:
		return "frozen-position"
	}
	return "none"
}

func (n endNet) why() string {
	switch n {
	case endNetWallClock:
		return "the track's length elapsed without a stop from the box"
	case endNetFrozen:
		return "the box stayed on play with its position frozen"
	}
	return ""
}

// trackEndInput is what the watcher knows about the current track on one poll.
type trackEndInput struct {
	sawPlay bool
	playing bool          // the box reported PLAY_STATE on this poll
	elapsed time.Duration // wall clock since the track was pushed
	dur     time.Duration // the length the media server claimed (0 = none)
	// obsTotal is the largest total the box reported for this track.
	obsTotal time.Duration
	// lastPos is the highest position the box reported (0 = never reported one).
	lastPos time.Duration
	// sinceLastPos is how long ago lastPos last increased. Only meaningful when
	// lastPos > 0.
	sinceLastPos time.Duration
	// ranAhead: the box reported a position for this track further than it
	// can have played (positionRunsAhead). Such a position proves nothing
	// about the end: it neither confirms the length nor counts as a freeze
	// before the wall clock has caught up with it.
	ranAhead bool
	// hint is an estimated length that was too uncertain to hand to the box
	// (a bitrate x size guess for an MP3 without a frame count), 0 when none.
	// It only lets the frozen-position net confirm an end sooner.
	hint time.Duration
	// timing is the clock the nets judge the readings by: nil means the
	// production clock, so a bare literal (the table tests) needs none.
	timing *queueTiming
}

// times resolves timing, falling back to the production clock.
func (in trackEndInput) times() *queueTiming {
	if in.timing != nil {
		return in.timing
	}
	return &productionQueueTiming
}

// trackEndReport is the ending track's numbers, for the advance log line.
type trackEndReport struct {
	net      string
	length   time.Duration
	lastPos  time.Duration
	boxTotal time.Duration
	elapsed  time.Duration
}

func (in trackEndInput) report(n endNet) trackEndReport {
	return trackEndReport{net: n.String(), length: in.dur, lastPos: in.lastPos,
		boxTotal: in.obsTotal, elapsed: in.elapsed}
}

// logAttrs renders the ending track for a log line.
func (r trackEndReport) logAttrs(title string) []any {
	return []any{"net", r.net, "endedTitle", title,
		"lengthSec", int(r.length.Seconds()), "lastPosSec", int(r.lastPos.Seconds()),
		"boxTotalSec", int(r.boxTotal.Seconds()), "elapsedSec", int(r.elapsed.Seconds())}
}

// trackEndNet decides whether the track is over when the box has not said so
// with a STOP frame. Two nets:
//
// Wall-clock: once playback was seen and the track length is known, advance a
// margin past it. This covers a missed STOP frame and a box that freezes at
// PLAY_STATE on a finite file's EOF (#219, #923), neither of which the STOP
// path can catch.
//
// The length is only a claim, though, and the box's position is evidence.
// #1065: a MinimServer library track was cut at 33 s, six runs in a row, while
// the speaker was still playing it; later tracks advanced at their real
// lengths. So, when the box reports a position:
//   - a position that moved on this very poll and has not reached the box's own
//     total is a box still playing, and the net holds. Once the position stops
//     moving (the EOF freeze), the next poll fires; when the box reports a
//     total and the position sits on it, it fires at once, as before (#923).
//   - a position that ran past the length and past the box's own total means
//     the length is wrong. It is dropped, and the frozen-position net decides.
//
// A box that reports no position at all keeps the plain wall-clock behaviour:
// there is nothing to corroborate the length against.
//
// Frozen-position: the box stays PLAY_STATE but its position has not advanced
// for frozenTimeout. For an unknown length (or one proven wrong) that is
// the only end signal (#380, #381 FRITZ!Box mediaserver); for a known length it
// fires once the length has elapsed, so an EOF freeze still advances even if
// the wall-clock net held. A genuine mid-track stall reports BUFFERING_STATE
// (excluded), so this trips only at a real EOF.
func trackEndNet(in trackEndInput) endNet {
	if !in.sawPlay {
		return endNetNone
	}
	qt := in.times()
	// Track length: the queue item's duration, or the box's reported total when
	// the item carried none (a DLNA server, e.g. Synology, that did not expose
	// duration in its metadata leaves dur==0). #219
	end := in.dur
	if in.obsTotal > end {
		end = in.obsTotal
	}
	posKnown := in.lastPos > 0
	if posKnown && end > 0 && in.lastPos > end+qt.overrunSlack {
		// The box has played further than any length anyone claimed: the length
		// is wrong and cannot time the end. Fall back to the freeze.
		end = 0
	}
	climbing, atOwnEnd := in.endSignals()
	margin := qt.advanceMargin(in.lastPos, end)
	if in.ranAhead {
		// A position that ran ahead of real time cannot say "this is the end".
		atOwnEnd = false
		margin = qt.timerMargin
	}

	if end > 0 && in.elapsed >= end+margin && (!climbing || atOwnEnd) {
		return endNetWallClock
	}
	// A box cannot play faster than real time, so a freeze only counts once
	// the wall clock has caught up with the frozen position plus the freeze
	// timeout. For a box whose position climbed in real time that is always
	// already true when the position has been still for frozenTimeout; for a
	// box that reported 120 s sixteen seconds after the push it holds the end
	// back until the track can really have played that far.
	caughtUp := in.elapsed+qt.aheadSlack >= in.lastPos+qt.frozenTimeout
	if in.playing && posKnown && in.sinceLastPos >= qt.frozenTimeout && caughtUp &&
		(end == 0 || in.elapsed >= end) {
		return endNetFrozen
	}
	// An estimated length confirms a freeze sooner: the position stopped on the
	// poll after it reached the estimate, and the wall clock agrees.
	if end == 0 && in.hint > 0 && in.playing && posKnown && !in.ranAhead && !climbing &&
		in.elapsed >= in.hint && absDur(in.lastPos-in.hint) <= qt.hintSlack {
		return endNetFrozen
	}
	return endNetNone
}

// endSignals are the two position readings the wall-clock net weighs: whether
// the position moved on this very poll (the watcher stamps the position on the
// same iteration it asks this, so a position that just climbed is milliseconds
// old and one that did not is at least a poll interval old), and whether it sits
// on the box's own reported total.
func (in trackEndInput) endSignals() (climbing, atOwnEnd bool) {
	qt := in.times()
	climbing = in.lastPos > 0 && in.sinceLastPos < qt.pollInterval/2
	atOwnEnd = in.obsTotal > 0 && in.lastPos >= in.obsTotal-qt.ownEndSlack
	return climbing, atOwnEnd
}

// advanceMargin is how long past the track length the wall-clock net waits.
//
// The six seconds exist for a box that reports NO position: with nothing to
// corroborate the length against, the timer has to be generous, or a track that
// buffers mid-way gets cut off. A box that DOES report a position and reports it
// sitting at the end of the track has already said the track is over, and making
// it wait another six seconds is the gap users hear.
//
// Measured on an ST20 playing one Synology folder twice, once through STR and
// once by the box itself (#923): 157 s against 150 s, and 236 s against 230 s.
// The box's own advance is 0.8 s. The chassis finishes the file and freezes in
// PLAY_STATE at EOF rather than emitting STOP, so the STOP branch never runs and
// every track ends on this timer.
//
// The remaining floor is the four second poll, which stays where it is: a faster
// poll would run on every speaker all day for one case (see the project rule on
// sparing the box hardware).
func (s *Server) advanceMargin(lastPos, end time.Duration) time.Duration {
	return s.queueTimes().advanceMargin(lastPos, end)
}

func (qt *queueTiming) advanceMargin(lastPos, end time.Duration) time.Duration {
	if lastPos > 0 && qt.nearEnd(lastPos, end) {
		return 0
	}
	return qt.timerMargin
}

// nearEnd reports whether progress is within endEpsilon of the track's end.
func (qt *queueTiming) nearEnd(progress, end time.Duration) bool {
	if end <= 0 {
		return true // unknown length: a STOP after real playback counts as the end
	}
	return progress >= end-qt.endEpsilon
}

// pollNowPlaying reads the box's now_playing once and returns the play status,
// the current/total position, whether the box is in standby or dropped its
// source, and the source attribute ("" when unknown). Zero values on any
// error. The verdicts are boxapi's: standby and a teardown (INVALID_SOURCE,
// the firmware's answer to a starved stream, a document with no <playStatus>
// at all) are read off the root source attribute, so a track literally named
// "STANDBY" cannot be mistaken for the box being off.
func (s *Server) pollNowPlaying() (status string, pos, total time.Duration, standby, tornDown bool, source string) {
	status, pos, total, standby, tornDown, source, _ = s.pollNowPlayingAt()
	return
}

// pollNowPlayingAt is pollNowPlaying plus the ContentItem location the speaker
// reports, for the single-track end watch, which has to tell its own file from
// whatever else the speaker reports playing. location is "" when unknown.
func (s *Server) pollNowPlayingAt() (status string, pos, total time.Duration, standby, tornDown bool, source, location string) {
	// Test seam, the same one boxPlayStateDetail has: the end detection is a
	// state machine over what the box reports, and it is only testable if the
	// reports can be scripted.
	if s.nowPlayingFn != nil {
		// The test seam predates the torn-down flag and scripts four values; a
		// scripted box is never reported as torn down, which keeps every existing
		// end-detection test meaning exactly what it did before. The source is
		// scripted separately and stays "" (unknown) unless a test sets it.
		st, p, t, sb := s.nowPlayingFn()
		if s.nowPlayingSourceFn != nil {
			source = s.nowPlayingSourceFn()
		}
		if s.nowPlayingLocationFn != nil {
			location = s.nowPlayingLocationFn()
		}
		return st, p, t, sb, false, source, location
	}
	np, err := s.readBoxNowWithin(3 * time.Second)
	if err != nil {
		return "", 0, 0, false, false, "", ""
	}
	return np.Status(), np.Position, np.Total, np.IsStandby(), np.SourceTornDown(), np.Source, np.Location()
}

// --- HTTP handlers -------------------------------------------------------

type queueStartItem struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Art         string `json:"art"`
	Mime        string `json:"mime"`
	DurationSec int    `json:"duration_sec"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
}

// queueCard is the optional Recently-played folder identity the desktop app
// sends with a folder play (#220). When absent the queue still plays; it just is
// not recorded as a card.
type queueCard struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Art  string `json:"art"`
	// Source is the media server's name, kept so a key saved while this
	// queue plays carries "from <server>" (#1030). Older apps send none; the
	// registered server list fills it in then (see LiveQueuePreset).
	Source string `json:"source,omitempty"`
}

type queueStartRequest struct {
	Items []queueStartItem `json:"items"`
	Start int              `json:"start"`
	// Pointers so an absent field is distinguishable from an explicit "off":
	// an explicit value wins and becomes the sticky play mode, an absent one
	// inherits it (see playmode.go). Older apps always send both explicitly,
	// which keeps their behaviour exactly as it was.
	Shuffle *bool     `json:"shuffle"`
	Repeat  *string   `json:"repeat"`
	Card    queueCard `json:"card"`
}

func toQueueItems(in []queueStartItem) []queueItem {
	out := make([]queueItem, 0, len(in))
	for _, it := range in {
		if it.URL == "" {
			continue
		}
		out = append(out, queueItem{
			URL:      it.URL,
			Title:    it.Title,
			Art:      it.Art,
			Mime:     it.Mime,
			Duration: time.Duration(it.DurationSec) * time.Second,
			Artist:   it.Artist,
			Album:    it.Album,
		})
	}
	return out
}

// handleQueue is POST (set + start a queue) or GET (current queue snapshot).
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.queueSnapshotWithCard())
	case http.MethodPost:
		if s.renderer == nil {
			http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
			return
		}
		var req queueStartRequest
		if !decodeJSONRequest(w, r, 1<<20, &req) {
			return
		}
		items := toQueueItems(req.Items)
		if len(items) == 0 {
			http.Error(w, "no playable items", http.StatusBadRequest)
			return
		}
		// Half of a broken pair, or the following half of a healthy one: the
		// first track's push could never be heard, so refuse before the wake.
		if ref := s.pairPlayRefusal(); ref != nil {
			writePlayRefusal(w, ref)
			return
		}
		card := recentCardCtx{key: req.Card.Key, name: req.Card.Name, art: req.Card.Art, source: req.Card.Source}
		// Resolve the play mode: explicit request values win and become the new
		// sticky mode; absent ones inherit the persisted choice (playmode.go).
		shuffle, rep, _ := s.loadPlayMode()
		if req.Shuffle != nil {
			shuffle = *req.Shuffle
		}
		if req.Repeat != nil {
			rep = parseRepeat(*req.Repeat)
		}
		if req.Shuffle != nil || req.Repeat != nil {
			s.savePlayMode(shuffle, rep)
		}
		// Detach the queue start from the request context (#252): the standby
		// wake inside startQueue can outlast the app's HTTP timeout, and the
		// first track's push must still reach the box after the app gave up.
		playCtx, playCancel := context.WithTimeout(context.WithoutCancel(r.Context()), playDetachTimeout)
		defer playCancel()
		if err := s.startQueue(playCtx, items, req.Start, shuffle, rep, card); err != nil {
			if isGroupedRejection(err) {
				s.writeGroupedPlayError(w, err)
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, s.queue.snapshot())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleQueueNext(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if _, _, err := s.queueSkip(true); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.queue.snapshot())
}

func (s *Server) handleQueuePrev(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if _, _, err := s.queueSkip(false); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.queue.snapshot())
}

// transportSkip advances playback to the next (forward=true) or previous track
// for the phone remote's Previous/Next controls. It is source-aware: Spotify is
// skipped in go-librespot when it is the live source (the box cannot skip a UPnP
// source itself, it emits QPLAY_SKIP_*_FAILED), otherwise the STR play queue is
// advanced. On a non-skippable source (radio, aux, Bluetooth) the queue skip is
// a graceful no-op, matching a hardware remote's Next/Prev on those sources. It
// returns which source handled the skip, for logging.
func (s *Server) transportSkip(ctx context.Context, forward bool) (source string, err error) {
	// Route to Spotify whenever Spotify is the box's current source, not only
	// while it is actively pulling the Ogg: spotifyStreaming() flaps to false the
	// moment a Spotify playlist is paused (the Ogg sink detaches), and Next/Prev
	// must still skip a paused playlist. boxSourceIsSpotify catches that case by
	// reading the box's now_playing location.
	if s.spotifySkip != nil && (s.spotifyIsStreaming() || s.boxSourceIsSpotify(ctx)) {
		s.enqueueSpotifySkip(forward)
		return "spotify", nil
	}
	if _, _, err := s.queueSkip(forward); err != nil {
		return "queue", err
	}
	return "queue", nil
}

// enqueueSpotifySkip acknowledges a Spotify Next/Prev press immediately and
// hands it to a single worker. The synchronous form waited on go-librespot's
// /player/next reply, which the engine holds while the next track loads: on a
// slow speaker every press blocked its HTTP handler ~5 s and rapid presses
// stacked into a serial half-minute wait the user read as a dead button (live
// Portable, 2026-07-31, four presses). The queue is capped small: a press
// beyond three waiting skips would fire long after the user stopped caring,
// as a surprise track change.
func (s *Server) enqueueSpotifySkip(forward bool) {
	s.spotifySkipOnce.Do(func() {
		s.spotifySkipCh = make(chan bool, 3)
		go s.spotifySkipWorker()
	})
	select {
	case s.spotifySkipCh <- forward:
	default:
		s.logger.Info("spotify skip: queue full, extra press dropped", "forward", forward)
	}
}

// spotifySkipWorker drains queued skips back-to-back. Each go-librespot call
// gets a short deadline: the engine performs the skip on receipt and only the
// REPLY is slow (it arrives with the loaded track), so waiting longer buys
// nothing. An engine that is actually down surfaces as a non-timeout error;
// the post-drain recover then repairs a box left not playing via the proven
// clean slot recall.
func (s *Server) spotifySkipWorker() {
	for forward := range s.spotifySkipCh {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		err := s.spotifySkip(ctx, forward)
		cancel()
		issued := err == nil || isTimeoutErr(err)
		switch {
		case err == nil:
			// Log EVERY executed skip: a fast ack used to leave no trace, so a
			// user's repeated presses were invisible in the bundle.
			s.logger.Info("spotify skip: issued", "forward", forward)
		case isTimeoutErr(err):
			s.logger.Info("spotify skip: go-librespot slow to ack, skip issued", "forward", forward)
		default:
			s.logger.Warn("spotify skip failed", "forward", forward, "err", err)
		}
		if len(s.spotifySkipCh) == 0 {
			if issued {
				s.reattachAfterSoftSkip()
			}
			s.spotifyRecoverAfterSkip()
		}
	}
}

// reattachAfterSoftSkip re-points the box at the SAME Spotify stream right
// after an app (soft) skip. The soft skip keeps the box attached, and the box
// holds the realtime-pacing lead (~10 s of the old track) in its own buffer,
// so the new track only became audible once all of that had played out: the
// press read as dead and the user pressed again (live ST30 2026-08-19, the
// audible change landed ~13 s after the press and the second skip was heard
// as "no effect"). Re-fetching the stream makes the box drop that buffer, so
// the skip is audible right after the engine loads the track (a brief gap,
// like a playlist switch). One re-push per press burst; the hardware path
// re-attaches in its own flow (the box tears its source down itself), so a
// recent hardware skip stands this down.
func (s *Server) reattachAfterSoftSkip() {
	if s.renderer == nil || !s.spotifyIsStreaming() {
		// A paused/detached box is deliberately left alone: a re-push sends
		// Play, and a skip on a paused playlist must not force playback.
		return
	}
	s.skipRepushMu.Lock()
	recentHW := time.Since(s.hwSkipAt) < 15*time.Second
	recentPush := time.Since(s.lastSkipRepushAt) < 8*time.Second
	if !recentHW && !recentPush {
		s.lastSkipRepushAt = time.Now()
	}
	s.skipRepushMu.Unlock()
	if recentHW || recentPush {
		return
	}
	// Hold the competing auto-repoint off across the deliberate detach gap the
	// re-push causes, exactly like the hardware path.
	if s.spotifySuppressActivate != nil {
		s.spotifySuppressActivate(12 * time.Second)
	}
	armedAt := time.Now()
	go func() {
		// Drop the box's buffer only once the NEW track is actually flowing:
		// the skip cut races stale pages to the boundary, and pushing before
		// it arrives throws the buffered safety margin away while the engine
		// is still loading - a slow load (CDN hiccup, another device pulling
		// the account session) then starves the fresh attachment into a
		// detach + recovery recall (live Portable 2026-08-19). The boundary
		// may already have passed while the worker waited out its 1.5 s ack
		// deadline, so "recent" counts too. No boundary within the wait means
		// the engine never delivered the track: keep the buffered stream (the
		// old, slow-but-safe behavior) and let the recovery net decide.
		// The window sits above the slowest observed engine load (11.3 s,
		// live Portable 2026-08-19 on a cold merged engine): waiting costs
		// nothing, the box plays its buffer meanwhile, and a late push still
		// beats the in-band switch by the remaining buffer length.
		deadline := time.Now().Add(12 * time.Second)
		for {
			if s.spotifySkipBoundary != nil {
				if b := s.spotifySkipBoundary(); b.After(armedAt.Add(-3 * time.Second)) {
					break
				}
			} else {
				// No boundary signal wired (tests, degraded startup): the
				// fixed short delay of the first implementation.
				time.Sleep(1500 * time.Millisecond)
				break
			}
			if time.Now().After(deadline) {
				s.logger.Info("skip: no track boundary within the wait, keeping the box's buffered stream")
				// Nothing was pushed: release the burst debounce so the NEXT
				// skip may re-push, instead of silently inheriting this
				// attempt's give-up for another eight seconds.
				s.skipRepushMu.Lock()
				if s.lastSkipRepushAt.Equal(armedAt) || s.lastSkipRepushAt.Before(armedAt) {
					s.lastSkipRepushAt = time.Time{}
				}
				s.skipRepushMu.Unlock()
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		// A deliberate stop while we waited wins: the push sends Play and
		// would force audio back on.
		if s.userStoppedRecently() {
			return
		}
		s.repushSpotifyStream("soft skip", 0)
	}()
}

// reattachAfterRecall re-pushes the slot's Spotify stream after a preset
// recall, and ONLY when stale pre-boundary audio actually reached the box's
// fresh attachment. The recall arms the engine's boundary cut before the box
// re-fetches the stream, so in the common case the attachment stays clean
// (staleKB ~0) and no push happens at all: a needless push would flap a stream
// that is already playing the right track. Only when the cut could not keep
// the buffer clean (late arm, early wrong BOS, cut expiry) is the box's buffer
// dropped, sharing the soft-skip burst debounce so recall and skip re-pushes
// cannot double-tear the stream. Runs in its own goroutine (caller spawns it).
func (s *Server) reattachAfterRecall(gen uint64, started time.Time, slot int, armedAt time.Time) {
	if s.renderer == nil || s.spotifySkipBoundary == nil {
		return
	}
	// Same boundary-wait budget as the soft skip: above the slowest observed
	// engine track load, so a landed boundary is always seen. The recall's
	// boundary always follows armedAt (the cut was armed before the play was
	// issued), so no backwards slack is needed here.
	wait := s.recallReattachWait
	if wait == 0 {
		wait = 12 * time.Second
	}
	deadline := time.Now().Add(wait)
	for s.spotifySkipBoundary().Before(armedAt) {
		if time.Now().After(deadline) {
			// The engine never delivered the new track: keep the box's
			// buffered stream and let the recall verify own the recovery.
			s.logger.Info("recall: no track boundary within the wait, keeping the box's buffered stream")
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	staleKB := int64(0)
	if s.spotifyBoundaryStaleKB != nil {
		staleKB = s.spotifyBoundaryStaleKB()
	}
	if staleKB < 64 { // under ~3s of audio: the cut kept the box clean
		s.logger.Info("recall: clean boundary handover, no re-push needed", "staleKB", staleKB)
		return
	}
	// A deliberate stop wins (the push sends Play), and a box that just
	// rejected the source as not-logged-in (1036) must not be re-pushed into
	// its re-login bounce.
	if s.userStoppedRecently() || s.recentLoginError() {
		return
	}
	// A newer play or a power-off supersedes this recall's re-push.
	if s.recallStandDownReason(gen, started) != "" {
		return
	}
	// Share the soft-skip burst debounce: a skip re-push and a recall re-push
	// within the window would tear the box's stream down twice back to back.
	// Deliberately does NOT consult hwSkipAt: a hardware preset PRESS is not a
	// hardware SKIP, and its own flow ends right here.
	s.skipRepushMu.Lock()
	recent := time.Since(s.lastSkipRepushAt) < 8*time.Second
	if !recent {
		s.lastSkipRepushAt = time.Now()
	}
	s.skipRepushMu.Unlock()
	if recent {
		return
	}
	s.repushSpotifyStream("recall stale buffer", slot)
}

// ReattachAfterSpotifyRecall exposes the recall re-push gate to the hardware
// preset path (cmd/agent), which mirrors the app recall and needs the same
// "only push when stale audio reached the box" decision. Blocking (boundary
// wait); the caller runs it in a goroutine.
func (s *Server) ReattachAfterSpotifyRecall(gen uint64, started time.Time, slot int, armedAt time.Time) {
	s.reattachAfterRecall(gen, started, slot, armedAt)
}

// slotFromSpotifyStreamURL extracts the preset slot N from a per-slot Spotify Ogg
// URL (".../spotify/stream-N.ogg"), or 0 if the URL is the slot-less default.
func slotFromSpotifyStreamURL(u string) int {
	m := regexp.MustCompile(`/spotify/stream-(\d+)\.ogg`).FindStringSubmatch(u)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// spotifyRecoverAfterSkip is the safety net after ANY Spotify skip. The app's
// soft skip keeps the box attached and normally plays straight through; the
// hardware key tears the box's source down and HardwareSkip lightly
// re-attaches it. Both paths funnel through the skip worker, which calls this
// once the queue drains: when the box ends up genuinely playing, stand down;
// when it does not, recover with the proven clean slot recall.
func (s *Server) spotifyRecoverAfterSkip() {
	if s.renderer == nil {
		return
	}
	boxURL := s.play.currentURL()
	if boxURL == "" || !strings.Contains(boxURL, "spotify/stream") {
		return
	}
	slot := slotFromSpotifyStreamURL(boxURL)
	go func() {
		time.Sleep(5 * time.Second)
		// Do not fight a deliberate stop, and do not thrash a box that reported
		// not-logged-in (a re-login is already in flight, re-pushing can wedge it).
		if s.userStoppedRecently() || s.recentLoginError() {
			return
		}
		// Soft skip (and any box that re-synced on its own): already playing, leave it.
		if s.boxSpotifyReallyPlaying() {
			s.NoteBoxHealthy()
			return
		}
		// Not playing at the first look: the skip re-push lands ~1.5 s after
		// the drain and the box then needs a few seconds to re-buffer into
		// PLAY_STATE, so a single early check would fire a recall that
		// restarts the track the user just skipped to. One more beat, then
		// decide for real.
		time.Sleep(4 * time.Second)
		if s.userStoppedRecently() || s.recentLoginError() {
			return
		}
		if s.boxSpotifyReallyPlaying() {
			s.NoteBoxHealthy()
			return
		}
		// A soft skip that (unusually) left the box not playing recovers with the
		// same proven clean slot recall the hardware path uses - identical to
		// /api/play/<slot>, which reloads the context and hands the box a clean
		// track boundary. For a shuffle preset (slots 4/5) that lands a fresh track;
		// a resume preset restarts its context.
		if slot >= 1 && slot <= 6 {
			s.recallSlotClean(context.Background(), slot)
			s.logger.Info("spotify skip recovery: clean slot recall after a skip left the box not playing", "slot", slot)
			return
		}
		s.logger.Warn("spotify skip recovery: box not playing and no slot to recall", "boxURL", boxURL)
	}()
}

// boxSpotifyReallyPlaying reports whether the box's now_playing is on STR's
// Spotify Ogg stream AND actually in PLAY_STATE (audio flowing), not merely
// attached/BUFFERING. The strict signal spotifyRecoverAfterSkip needs to tell a
// genuinely playing box from one stuck buffering on a stalled stream.
func (s *Server) boxSpotifyReallyPlaying() bool {
	np, err := s.readBoxNowWithin(4 * time.Second)
	return err == nil && np.ReallySpotify()
}

// TransportSkip advances playback to the next (forward=true) or previous track,
// source-aware (Spotify or the STR play queue). Exposed so the hardware remote's
// Next/Prev keys drive the same logic as the phone remote's controls: without it
// the box could not skip a UPnP folder source itself and only advanced when the
// track ended naturally, stalling for the remaining track time (#300).
func (s *Server) TransportSkip(ctx context.Context, forward bool) (string, error) {
	return s.transportSkip(ctx, forward)
}

// HardwareSkip handles the SoundTouch remote's physical Next/Prev keys. On the
// hardware key the box first runs its OWN native skip on the UPnP source, fails
// (QPLAY_SKIP_*_FAILED) and tears the source down to INVALID_SOURCE while
// go-librespot is still playing, so unlike the app's soft skip the box has to
// be re-attached afterwards.
//
// History: this used to detour through a full clean slot recall, because a
// skip layered on the teardown made the box re-attach to a mid-transition Ogg
// with mismatched headers and wedge on a 3102 decoder error (live ST30,
// 2026-07-14). That failure class is gone: the engine hands passthrough
// sources over only at Ogg page boundaries and the drain checksums every page
// before forwarding it, so the stream a re-attaching box sees is always
// well-formed. The recall detour's cost had meanwhile become worse than its
// protection: on a resume preset it reloaded the context and RESTARTED the
// current track from zero, so the remote's Next never actually advanced, with
// buffer-replay judder on top (live Portable, 2026-08-01). Now the engine
// skips exactly like the app path, and the box is lightly re-attached to the
// same slot stream; the post-drain safety net (spotifyRecoverAfterSkip) still
// falls back to the proven clean recall when the box does not come back
// playing.
func (s *Server) HardwareSkip(ctx context.Context, forward bool) (string, error) {
	slot := s.currentSpotifySlot()
	if slot < 1 || slot > 6 {
		return s.transportSkip(ctx, forward)
	}
	// Hold the competing auto-repoint off for the whole handover: the box is
	// tearing its UPnP source down right now while go-librespot still plays, so
	// maybeActivate is primed to fire the instant it sees no sink.
	if s.spotifySuppressActivate != nil {
		s.spotifySuppressActivate(12 * time.Second)
	}
	// Mark the hardware flow so the skip worker's soft-skip re-push stands
	// down: this path re-attaches the box itself, one push is enough.
	s.skipRepushMu.Lock()
	s.hwSkipAt = time.Now()
	s.skipRepushMu.Unlock()
	s.enqueueSpotifySkip(forward)
	go s.reattachAfterHardwareSkip(slot)
	s.logger.Info("hardware skip: engine skip issued, re-attaching the box to the slot stream", "slot", slot, "forward", forward)
	return "spotify-skip", nil
}

// reattachAfterHardwareSkip re-points the box at the SAME slot stream after
// its own hardware-key teardown flap settles, without reloading the Spotify
// context (a context reload restarts a resume preset's current track from
// zero). Best effort: when the push fails or the box does not end up playing,
// the skip worker's post-drain recovery performs the clean slot recall.
func (s *Server) reattachAfterHardwareSkip(slot int) {
	if s.renderer == nil {
		return
	}
	// Let the box finish its own INVALID_SOURCE bounce first; pushing into the
	// teardown gets swallowed.
	time.Sleep(1500 * time.Millisecond)
	s.repushSpotifyStream("hardware skip", slot)
}

// repushSpotifyStream pushes the Spotify stream the box last played back at
// it (SetURI + Play of the SAME URL), making the box drop its buffered audio
// and rejoin the live stream. Shared by the hardware-skip re-attach (the box
// tore its source down itself) and the soft-skip re-push (the box is attached
// but buffering the old track). requireSlot > 0 additionally demands playback
// is still on that slot's stream; 0 accepts any Spotify stream, the slot-less
// app-driven one included. Best effort: on failure the skip worker's recovery
// net performs the clean slot recall.
func (s *Server) repushSpotifyStream(why string, requireSlot int) {
	var boxURL, title, art, mime string
	if lp := s.play.current(); lp != nil {
		boxURL, title, art, mime = lp.boxURL, lp.title, lp.art, lp.mime
	}
	if boxURL == "" || !strings.Contains(boxURL, "spotify/stream") {
		return // playback changed while the flap settled; nothing to re-attach
	}
	slot := slotFromSpotifyStreamURL(boxURL)
	if requireSlot > 0 && slot != requireSlot {
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	// This push makes the box drop and re-fetch the Ogg stream; announce the
	// re-attach so the engine's storm damping does not count STR's own work.
	if s.spotifyExpectReattach != nil {
		s.spotifyExpectReattach(15 * time.Second)
	}
	// Re-arm the auto-repoint hold HERE, at the moment of the push: the arm
	// at the start of the soft-skip flow can expire during the up-to-12 s
	// track-boundary wait, leaving the detach gap this push causes uncovered
	// (SuppressActivate only ever extends, so the early arm stays harmless).
	if s.spotifySuppressActivate != nil {
		s.spotifySuppressActivate(12 * time.Second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var err error
	if mime != "" {
		err = s.renderer.PlayURLMime(ctx, boxURL, title, art, mime)
	} else {
		err = s.renderer.PlayURL(ctx, boxURL, title, art)
	}
	if err != nil {
		s.logger.Warn("skip: re-attach push failed (the recovery net will recall)", "why", why, "slot", slot, "err", err)
		return
	}
	s.logger.Info("skip: box re-attached to the Spotify stream", "why", why, "slot", slot)
}

// currentSpotifySlot returns the preset slot (1-6) the box is currently playing a
// Spotify preset from, or 0 if it is not on a numbered Spotify preset stream (the
// slot-less /spotify/stream.ogg, i.e. app-driven playback, or a non-Spotify
// source). Read from lastPlay, the URL STR last pointed the box at.
func (s *Server) currentSpotifySlot() int {
	boxURL := s.play.currentURL()
	if boxURL == "" || !strings.Contains(boxURL, "spotify/stream") {
		return 0
	}
	return slotFromSpotifyStreamURL(boxURL)
}

// recallSlotClean recalls a preset slot through the exact same code path as the
// app's POST /api/play/<slot>, so a hardware-driven recovery behaves identically
// to a soft recall (SetRecalling before the box attaches, cold-engine wait,
// background verify). It calls the playSlot service directly: the caller
// (HardwareSkip's recovery net, the alarms, the stuck-buffer rescue) only
// needs the box driven, not an HTTP body.
//
// The status IS returned, for the callers that need to tell "the box is being
// driven" from "this preset cannot be played at all" (an unreplayable Spotify
// record, no Spotify login): 200 when the recall was delivered, the refusal's
// status otherwise. Callers that do not care ignore it.
func (s *Server) recallSlotClean(ctx context.Context, slot int) int {
	if _, ref := s.playSlot(ctx, slot); ref != nil {
		return ref.Status
	}
	return http.StatusOK
}

// spotifyIsStreaming is a nil-safe wrapper around the streaming predicate.
func (s *Server) spotifyIsStreaming() bool {
	return s.spotifyStreaming != nil && s.spotifyStreaming()
}

// isTimeoutErr reports whether err is a network/deadline timeout (as opposed to
// a connection failure). A go-librespot skip that times out awaiting the HTTP
// response has still performed the skip, so the transport handler treats it as
// success; a connection error (engine down) does not and is a real failure.
func isTimeoutErr(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// boxSourceIsSpotify reports whether the box's now_playing is pointed at STR's
// Spotify Ogg stream, regardless of play/pause. Used so the phone remote's
// Next/Prev reach go-librespot even when a Spotify playlist is paused (where
// spotifyStreaming() is false, since the Ogg sink has detached).
func (s *Server) boxSourceIsSpotify(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	np, err := s.readBoxNow(ctx)
	return err == nil && np.OnSpotifyStream()
}

func (s *Server) handleTransportNext(w http.ResponseWriter, r *http.Request) {
	s.handleTransportSkip(w, r, true)
}

func (s *Server) handleTransportPrev(w http.ResponseWriter, r *http.Request) {
	s.handleTransportSkip(w, r, false)
}

func (s *Server) handleTransportSkip(w http.ResponseWriter, r *http.Request, forward bool) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	src, err := s.transportSkip(ctx, forward)
	if err != nil {
		s.logger.Warn("transport skip failed", "forward", forward, "source", src, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "source": src})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "source": src})
}

func (s *Server) handleQueueShuffle(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if !decodeJSONRequest(w, r, 1<<12, &req) {
		return
	}
	s.queue.setShuffle(req.On)
	// An explicit toggle is the user's standing choice: persist it so the next
	// queue start (a hardware key press included) keeps it (see playmode.go).
	snap := s.queue.snapshot()
	s.savePlayMode(snap.Shuffle, parseRepeat(snap.Repeat))
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleQueueRepeat(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if !decodeJSONRequest(w, r, 1<<12, &req) {
		return
	}
	s.queue.setRepeat(parseRepeat(req.Mode))
	snap := s.queue.snapshot()
	s.savePlayMode(snap.Shuffle, parseRepeat(snap.Repeat))
	writeJSON(w, http.StatusOK, snap)
}

// queueSnapshotWithCard is the GET /api/queue answer: the queue snapshot plus the
// Recently-played card key of the folder it plays, while it is active, the key
// it was saved on while it played, and the preset store's revision.
func (s *Server) queueSnapshotWithCard() queueSnapshot {
	snap := s.queue.snapshot()
	if snap.Active {
		s.recentMu.Lock()
		snap.Card = s.recentQueueCard.key
		s.recentMu.Unlock()
		// A queue started from a key already names it in its card; matching the
		// tracks as well would light a second key that holds the same folder.
		// The card counts only while its key still holds the tracks that play:
		// a key given another folder stayed lit for the old one (#1525). The
		// queue is then a folder like any other, lit on the key that holds it.
		if n, ok := queueSlotCardNum(snap.Card); ok && s.cardSlotHolds(n) {
			snap.CardSlot = n
			snap.Slot = s.queueSavedSlotHolds()
		} else {
			snap.Slot = s.queueKeySlot()
		}
		snap.SlotChecked = true
	}
	if s.presets != nil {
		snap.PresetRev = s.presets.Revision()
	}
	return snap
}
