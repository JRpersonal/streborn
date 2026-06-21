package webui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxurl"
)

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
	return nil
}

func (s *Server) queueCtx() context.Context {
	if s.baseCtx != nil {
		return s.baseCtx
	}
	return context.Background()
}

// startQueue replaces the queue with items and starts playing from start.
func (s *Server) startQueue(ctx context.Context, items []queueItem, start int, shuffle bool, rep repeatMode) error {
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
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
}

// advanceAndPlay moves to the next track (natural end vs manual skip differ on
// repeatOne) and plays it, or stops the queue at the end. The caller must NOT
// hold boxCmdMu.
func (s *Server) advanceAndPlay(natural bool) {
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
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
		gen     int
		lastPos time.Duration
		sawPlay bool
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
			gen, lastPos, sawPlay = curGen, 0, false
		}

		ps, pos, total := s.pollNowPlaying()
		end := dur
		if total > 0 {
			end = total
		}

		switch ps {
		case "PLAY_STATE", "BUFFERING_STATE":
			sawPlay = true
			if pos > 0 {
				lastPos = pos
			}
			continue
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
				s.advanceAndPlay(true)
			} else {
				s.stopQueue() // stopped well before the end: a real stop, not an end
				return
			}
			continue
		}

		// No usable status this tick (poll error or an idle box). Safety nets:
		if !sawPlay && time.Since(start) >= queueStallTimeout {
			s.advanceAndPlay(true) // a track that never started: skip it
		} else if sawPlay && dur > 0 && time.Since(start) >= dur+queueTimerMargin {
			s.advanceAndPlay(true) // missed the STOP frame: the track length elapsed
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
)

// pollNowPlaying reads the box's now_playing once and returns the play status
// and the current/total position. Zero values on any error.
func (s *Server) pollNowPlaying() (status string, pos, total time.Duration) {
	if s.boxHost == "" {
		return "", 0, 0
	}
	cl := &http.Client{Timeout: 3 * time.Second}
	resp, err := cl.Get("http://" + s.boxHost + ":8090/now_playing")
	if err != nil {
		return "", 0, 0
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	resp.Body.Close()
	body := string(b)
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
	return status, pos, total
}

// --- HTTP handlers -------------------------------------------------------

type queueStartItem struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Art         string `json:"art"`
	Mime        string `json:"mime"`
	DurationSec int    `json:"duration_sec"`
}

type queueStartRequest struct {
	Items   []queueStartItem `json:"items"`
	Start   int              `json:"start"`
	Shuffle bool             `json:"shuffle"`
	Repeat  string           `json:"repeat"`
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
		if err := s.startQueue(r.Context(), items, req.Start, req.Shuffle, parseRepeat(req.Repeat)); err != nil {
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
