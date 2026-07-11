package webui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/presets"
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
		})
	}
	return out
}

// RecallSlot handles a hardware preset-button press for a queue preset: if the
// slot holds a saved DLNA folder it starts the play-queue and returns true.
// Otherwise it returns false and the caller falls back to the existing
// single-track recall. This keeps the queue logic in webui (it owns the queue)
// without entangling the gabbo handler in cmd/agent.
func (s *Server) RecallSlot(ctx context.Context, slot int) (handled bool) {
	if s.presets == nil {
		return false
	}
	p, ok := s.presets.Get(slot)
	if !ok || p.Type != "queue" {
		return false
	}
	items := presetItemsToQueue(p.Items)
	if len(items) == 0 {
		return false
	}
	s.ensureBoxReady(ctx)
	s.logger.Info("preset slot recall (hardware): queue", "slot", slot, "tracks", len(items), "shuffle", p.Shuffle)
	// Record the saved folder as a Recently-played card (#220), keyed on the slot
	// so repeated recalls of the same preset group together.
	card := recentCardCtx{key: fmt.Sprintf("queue:slot:%d", slot), name: p.Name, art: p.Art}
	if err := s.startQueue(ctx, items, 0, p.Shuffle, repeatOff, card); err != nil {
		s.logger.Warn("hardware queue recall failed", "slot", slot, "err", err)
	}
	return true
}

// Auto-advance tuning. The Bose box emits no native "track finished" event: a
// finished file and a deliberate stop both surface only as a now_playing
// STOP_STATE. The watcher therefore decides from progress (the box's reported
// position, or wall-clock elapsed when the box reports none) plus a wall-clock
// timer as a safety net, per the chosen "position + timer" strategy.
const (
	queuePollInterval = 4 * time.Second  // how often the watcher reads now_playing
	queueEndEpsilon   = 12 * time.Second // progress within this of the end == "ended"
	queueTimerMargin  = 6 * time.Second  // grace past the track length before the net trips
	queueStallTimeout = 25 * time.Second // a track that never starts is skipped
	// queueFrozenTimeout advances when the box sits in PLAY_STATE with its
	// position frozen and the track length is UNKNOWN. Some DLNA servers (a
	// FRITZ!Box mediaserver) expose no duration AND the box reports no total, so
	// end==0 and the wall-clock net cannot fire; the box also finishes the file
	// but stays PLAY_STATE frozen at EOF instead of emitting STOP, so that path
	// cannot fire either, and a folder hung on its first track forever (#380,
	// #381). A position that has not moved for this long is a finished track.
	queueFrozenTimeout = 15 * time.Second
)

// pushStream sends one stream to the box, choosing direct play (a plain-HTTP
// library file the box can range-read) vs the loopback proxy (radio / HTTPS)
// exactly like handlePlay, and records it as the last play. The caller must
// hold boxCmdMu.
func (s *Server) pushStream(ctx context.Context, url, title, art, mime string) error {
	playDirect := mime != "" && isPlainHTTPURL(url)
	playURL := boxurl.RawStream(url)
	if playDirect {
		playURL = url
	}
	var err error
	if mime != "" {
		err = s.renderer.PlayURLMime(ctx, playURL, title, art, mime)
	} else {
		err = s.renderer.PlayURL(ctx, playURL, title, art)
	}
	if err != nil {
		return err
	}
	s.setLastPlay(playURL, title, art, mime)
	// Recently-played (#220): hang this queue track under the active folder card.
	// No-op outside a queue (pushStream is queue-only) or before a card is set.
	s.recentNoteQueueTrack(title)
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
	s.ensureBoxReady(ctx)
	s.queue.load(items, start, shuffle, rep)
	it, ok := s.queue.current()
	if !ok {
		return errors.New("empty queue")
	}
	s.ClearUserStop()
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
		s.recentNoteQueueCard(card.key, card.name, card.art, card.url)
	} else {
		s.recentClearQueueCard()
	}
	if err := s.pushStream(ctx, it.URL, it.Title, it.Art, it.Mime); err != nil {
		return err
	}
	s.setQueueTiming(it.Duration)
	s.ensureWatcher()
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
func (s *Server) stopQueue() {
	s.queue.clear()
	s.cancelWatcher()
	s.recentClearQueueCard()
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
func (s *Server) advanceAndPlay(natural bool, gen int) {
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	s.queueMu.Lock()
	superseded := s.queueGen != gen
	s.queueMu.Unlock()
	if superseded {
		s.logger.Info("queue advance: a new queue/track started while this advance waited, standing down")
		return
	}
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
		}
		s.cancelWatcher()
		return
	}
	s.ClearUserStop()
	if err := s.pushStream(s.queueCtx(), it.URL, it.Title, it.Art, it.Mime); err != nil {
		s.logger.Warn("queue advance: play failed", "title", it.Title, "err", err)
		return
	}
	s.setQueueTiming(it.Duration)
}

// queueSkip plays the next (forward) or previous track on demand.
func (s *Server) queueSkip(forward bool) (queueItem, bool, error) {
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	if !s.queue.isActive() {
		return queueItem{}, false, nil
	}
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
		s.cancelWatcher()
		return queueItem{}, false, nil
	}
	s.ClearUserStop()
	if err := s.pushStream(s.queueCtx(), it.URL, it.Title, it.Art, it.Mime); err != nil {
		return queueItem{}, false, err
	}
	s.setQueueTiming(it.Duration)
	return it, true, nil
}

// runQueueWatcher polls now_playing while a queue is active and advances when
// the current track ends. It exits when the queue is no longer active or ctx is
// cancelled.
func (s *Server) runQueueWatcher(ctx context.Context) {
	ticker := time.NewTicker(queuePollInterval)
	defer ticker.Stop()
	var (
		gen       int
		lastPos   time.Duration
		lastPosAt time.Time     // when lastPos last increased (frozen-position net)
		obsTotal  time.Duration // largest total the box reported for this track
		sawPlay   bool
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !s.queue.isActive() {
			return
		}
		s.queueMu.Lock()
		curGen := s.queueGen
		start := s.queueTrackStart
		dur := s.queueTrackDur
		s.queueMu.Unlock()
		if curGen != gen {
			gen, lastPos, lastPosAt, obsTotal, sawPlay = curGen, 0, time.Time{}, 0, false
		}

		ps, pos, total, standby := s.pollNowPlaying()
		if standby {
			// The box was powered off mid-queue (top switch, remote, or the app's
			// Standby button -> now_playing source=STANDBY). A standby is never a
			// track end: stop the queue so STR does not advance and re-push the next
			// track, which would wake the box back up and resume playing (#219).
			s.logger.Info("queue watcher: box entered standby, stopping queue (not advancing)")
			s.stopQueue()
			return
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

		switch ps {
		case "PLAY_STATE", "BUFFERING_STATE":
			sawPlay = true
			// Track the MAX position and when it last advanced: the box's position
			// climbs each second while playing and then freezes at EOF, so a
			// position that stops moving is the finished-track signal for the
			// unknown-length case below.
			if pos > lastPos {
				lastPos = pos
				lastPosAt = time.Now()
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
			if nearEnd(prog, end) {
				s.advanceAndPlay(true, curGen)
			} else {
				s.stopQueue() // stopped well before the end: a real stop, not an end
				return
			}
			continue
		default:
			// No usable status this tick (poll error or an idle box).
			if !sawPlay && time.Since(start) >= queueStallTimeout {
				s.advanceAndPlay(true, curGen) // a track that never started: skip it
				continue
			}
		}

		// Wall-clock safety net, reached on PLAY_STATE and on an unknown status:
		// once playback was seen and the track length is known, advance a margin
		// past it. This covers both a missed STOP frame and a box that freezes at
		// PLAY_STATE on a finite file's EOF (#219), neither of which the STOP path
		// above can catch.
		if sawPlay && end > 0 && time.Since(start) >= end+queueTimerMargin {
			s.advanceAndPlay(true, curGen)
			continue
		}
		// Frozen-position net, for the UNKNOWN-length case only (end==0): the box
		// stays PLAY_STATE but its position has not advanced for queueFrozenTimeout,
		// which the wall-clock net above cannot catch without a length. Gated on
		// end==0 so any queue with a known duration/total keeps the vetted
		// wall-clock net unchanged (the Synology/#219 path does not regress). A
		// genuine mid-track stall reports BUFFERING_STATE (excluded here), so this
		// trips only at a real EOF (#380, #381 FRITZ!Box mediaserver).
		if sawPlay && ps == "PLAY_STATE" && end == 0 && lastPos > 0 &&
			!lastPosAt.IsZero() && time.Since(lastPosAt) >= queueFrozenTimeout {
			s.advanceAndPlay(true, curGen)
		}
	}
}

func nearEnd(progress, end time.Duration) bool {
	if end <= 0 {
		return true // unknown length: a STOP after real playback counts as the end
	}
	return progress >= end-queueEndEpsilon
}

var (
	reNowPlayStatus = regexp.MustCompile(`<playStatus>([^<]+)</playStatus>`)
	reNowPlayTime   = regexp.MustCompile(`<time total="(\d+)"\s*>(\d+)</time>`)
	// reNowPlaySource reads the <nowPlaying source="..."> attribute (not a track
	// title), so a station/track literally named "STANDBY" cannot be mistaken for
	// the box being off.
	reNowPlaySource = regexp.MustCompile(`<nowPlaying[^>]*\bsource="([^"]*)"`)
	// reNowPlayLocation reads the ContentItem location attribute: the URL the
	// box is currently tuned to. Used by the recall verify to detect the box
	// playing a DIFFERENT stream than the one just recalled (#252).
	reNowPlayLocation = regexp.MustCompile(`\blocation="([^"]*)"`)
)

// nowPlayingStandby reports whether the box's now_playing says it is in standby
// (powered off). The box reports source="STANDBY" (some firmwares
// "NETWORK_STANDBY") on the nowPlaying root when it is off.
func nowPlayingStandby(body string) bool {
	if m := reNowPlaySource.FindStringSubmatch(body); m != nil {
		return strings.Contains(m[1], "STANDBY")
	}
	return false
}

// pollNowPlaying reads the box's now_playing once and returns the play status,
// the current/total position, and whether the box is in standby. Zero values on
// any error.
func (s *Server) pollNowPlaying() (status string, pos, total time.Duration, standby bool) {
	if s.boxHost == "" {
		return "", 0, 0, false
	}
	cl := &http.Client{Timeout: 3 * time.Second}
	resp, err := cl.Get("http://" + s.boxHost + ":8090/now_playing")
	if err != nil {
		return "", 0, 0, false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	resp.Body.Close()
	body := string(b)
	standby = nowPlayingStandby(body)
	if m := reNowPlayStatus.FindStringSubmatch(body); m != nil {
		status = m[1]
	} else {
		switch { // some firmwares put it on an attribute; fall back to a scan
		case strings.Contains(body, "PLAY_STATE"):
			status = "PLAY_STATE"
		case strings.Contains(body, "BUFFERING_STATE"):
			status = "BUFFERING_STATE"
		case strings.Contains(body, "PAUSE_STATE"):
			status = "PAUSE_STATE"
		case strings.Contains(body, "STOP_STATE"):
			status = "STOP_STATE"
		}
	}
	if m := reNowPlayTime.FindStringSubmatch(body); m != nil {
		if t, err := strconv.Atoi(m[1]); err == nil {
			total = time.Duration(t) * time.Second
		}
		if c, err := strconv.Atoi(m[2]); err == nil {
			pos = time.Duration(c) * time.Second
		}
	}
	return status, pos, total, standby
}

// --- HTTP handlers -------------------------------------------------------

type queueStartItem struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Art         string `json:"art"`
	Mime        string `json:"mime"`
	DurationSec int    `json:"duration_sec"`
}

// queueCard is the optional Recently-played folder identity the desktop app
// sends with a folder play (#220). When absent the queue still plays; it just is
// not recorded as a card.
type queueCard struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Art  string `json:"art"`
}

type queueStartRequest struct {
	Items   []queueStartItem `json:"items"`
	Start   int              `json:"start"`
	Shuffle bool             `json:"shuffle"`
	Repeat  string           `json:"repeat"`
	Card    queueCard        `json:"card"`
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
		})
	}
	return out
}

// handleQueue is POST (set + start a queue) or GET (current queue snapshot).
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.queue.snapshot())
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
		card := recentCardCtx{key: req.Card.Key, name: req.Card.Name, art: req.Card.Art}
		// Detach the queue start from the request context (#252): the standby
		// wake inside startQueue can outlast the app's HTTP timeout, and the
		// first track's push must still reach the box after the app gave up.
		playCtx, playCancel := context.WithTimeout(context.WithoutCancel(r.Context()), playDetachTimeout)
		defer playCancel()
		if err := s.startQueue(playCtx, items, req.Start, req.Shuffle, parseRepeat(req.Repeat), card); err != nil {
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
		err := s.spotifySkip(ctx, forward)
		if err != nil && isTimeoutErr(err) {
			// go-librespot performs the skip but holds its /player/next response
			// while the next track loads, past the API client's timeout; the track
			// still changes. Report success rather than a spurious error, only a
			// real transport failure (go-librespot down) propagates.
			s.logger.Info("spotify skip: go-librespot slow to ack, skip issued", "forward", forward)
			return "spotify", nil
		}
		return "spotify", err
	}
	if _, _, err := s.queueSkip(forward); err != nil {
		return "queue", err
	}
	return "queue", nil
}

// TransportSkip advances playback to the next (forward=true) or previous track,
// source-aware (Spotify or the STR play queue). Exposed so the hardware remote's
// Next/Prev keys drive the same logic as the phone remote's controls: without it
// the box could not skip a UPnP folder source itself and only advanced when the
// track ended naturally, stalling for the remaining track time (#300).
func (s *Server) TransportSkip(ctx context.Context, forward bool) (string, error) {
	return s.transportSkip(ctx, forward)
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
	host := s.boxHost
	if host == "" {
		host = "127.0.0.1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+":8090/now_playing", nil)
	if err != nil {
		return false
	}
	cl := &http.Client{Timeout: 4 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return strings.Contains(string(b), "spotify/stream")
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
	writeJSON(w, http.StatusOK, s.queue.snapshot())
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
	writeJSON(w, http.StatusOK, s.queue.snapshot())
}
