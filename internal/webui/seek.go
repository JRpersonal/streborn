package webui

import (
	"context"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// Jumping inside the playing track (discussion #1207).
//
// A click on the progress bar asks the speaker's own AVTransport to Seek. That
// only means something for a file the speaker fetches itself and whose length
// is known: a music-library file handed over directly, alone or as the current
// track of a folder. Radio has no position to return to, a library file that
// goes through the stream proxy is one long stream to the speaker, and Spotify
// runs on its own engine, so all of those are refused and reported as not
// seekable on GET /api/position, which is what the clients read before they
// make the bar clickable.
//
// What a Seek does on the speaker in the middle of a play is NOT measured. The
// one measured seek is the file restart of resumefile.go (ST10, FW 27.0.6):
// about two seconds after a Play the speaker takes Seek(REL_TIME) and the
// position counts on from there. So this stays defensive on three points:
//
//   - only while the transport reports PLAYING. A paused file is the state
//     the firmware freezes in on the way back (#830), and a seek there is one
//     more unknown on top of it;
//   - both end watches are told about the jump (userSeekNote). They time a
//     track from its push and follow the highest position seen, so a jump
//     back would end the track early by the distance jumped and a jump
//     forward would leave that much silence at its end;
//   - they also look away for seekQuiet after the jump, in case the speaker
//     reports a stop, a zero or the old position while it re-opens the file.

// seekEndGuard keeps a jump off the very last second of a track: a position on
// the end is what both end watches read as the track being over.
const seekEndGuard = time.Second

// seekRangeSlack is how far past the track's length a requested position may
// be and still be meant as "the end": the clients work in whole seconds and
// the speaker rounds its total.
const seekRangeSlack = time.Second

// seekStatusMaxAge bounds how old the last now_playing body may be for GET
// /api/position to read a pause from it. The clients poll /api/status every
// few seconds while they show the bar, so the body is normally fresher.
const seekStatusMaxAge = 15 * time.Second

// userSeekNote is the listener's last jump, for the end watches.
type userSeekNote struct {
	seq uint64
	at  time.Time
	pos time.Duration
	// queue says which watch it is for; queueGen and fileGen name the very
	// play it happened in, so a watch never applies it to another track.
	queue    bool
	queueGen int
	fileGen  uint64
}

// userSeekState holds the last jump under its own lock.
type userSeekState struct {
	mu   sync.Mutex
	note userSeekNote
}

func (u *userSeekState) record(n userSeekNote) {
	u.mu.Lock()
	n.seq = u.note.seq + 1
	u.note = n
	u.mu.Unlock()
}

// after returns the last jump when it is newer than the one the caller has
// already seen.
func (u *userSeekState) after(seen uint64) (userSeekNote, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.note.seq == 0 || u.note.seq == seen {
		return userSeekNote{}, false
	}
	return u.note, true
}

// seekQuietTime is how long an end watch ignores the speaker after a jump.
func (s *Server) seekQuietTime() time.Duration {
	return s.fileResumeTimes().seekQuiet
}

// seekablePlay is the play a jump would act on.
type seekablePlay struct {
	title string
	// dur is the length STR knows, 0 when only the speaker reports one.
	dur      time.Duration
	queue    bool
	queueGen int
	fileGen  uint64
}

// currentSeekablePlay returns the play the speaker can be positioned in, or
// why there is none. It asks only what this agent knows; the speaker's own
// state (playing, paused, its total) is the caller's to read.
func (s *Server) currentSeekablePlay() (seekablePlay, string) {
	if s.queue != nil && s.queue.isActive() {
		it, ok := s.queue.current()
		if !ok {
			return seekablePlay{}, "nothing is playing"
		}
		if !queuePlaysDirect(it.URL, it.Mime) {
			return seekablePlay{}, "this track reaches the speaker as a stream, which has no position to jump to"
		}
		s.queueMu.Lock()
		sp := seekablePlay{title: it.Title, dur: s.queueTrackDur, queue: true, queueGen: s.queueGen}
		s.queueMu.Unlock()
		return sp, ""
	}
	tr, ok := s.currentDirectFile()
	if !ok {
		return seekablePlay{}, "what is playing is not a music-library file with a known length"
	}
	return seekablePlay{title: tr.title, dur: tr.dur, fileGen: s.RecallGeneration()}, ""
}

// lastKnownPaused reports whether the newest now_playing body this agent
// holds, if it is recent enough, shows the speaker paused. It never asks the
// speaker: GET /api/position is polled every couple of seconds.
func (s *Server) lastKnownPaused() bool {
	s.statusMu.Lock()
	body, code, at := s.statusBody, s.statusCode, s.statusAt
	s.statusMu.Unlock()
	if body == nil || code != http.StatusOK || time.Since(at) > seekStatusMaxAge {
		return false
	}
	return boxapi.ParseNowPlaying(body).Status() == "PAUSE_STATE"
}

// positionSeekable is the "seekable" flag of GET /api/position: the play is a
// file the speaker can be positioned in, a length is known (the speaker's
// boxTotal, or STR's own) and the speaker is not known to be paused.
func (s *Server) positionSeekable(boxTotal time.Duration) bool {
	sp, why := s.currentSeekablePlay()
	if why != "" || (boxTotal <= 0 && sp.dur <= 0) {
		return false
	}
	return !s.lastKnownPaused()
}

// seekRefusal is a jump that did not happen: the HTTP status, a stable reason
// code for clients and a sentence for the log and for people.
type seekRefusal struct {
	code   int
	reason string
	msg    string
}

// seekResult is a jump the speaker took.
type seekResult struct {
	to     time.Duration
	length time.Duration
}

// clampSeekTarget validates a requested position against the track's length.
// A position past the end is refused; one on the end is pulled back by
// seekEndGuard so the jump itself is not read as the track ending.
func clampSeekTarget(target, length time.Duration) (time.Duration, bool) {
	if target < 0 || length <= 0 || target > length+seekRangeSlack {
		return 0, false
	}
	if limit := length - seekEndGuard; target > limit {
		target = limit
	}
	if target < 0 {
		target = 0
	}
	return target, true
}

// seekTo positions the playing file at target. It holds boxCmdMu, like every
// other command that changes what the speaker plays, so a jump cannot land
// between the two halves of a track change.
func (s *Server) seekTo(ctx context.Context, target time.Duration) (seekResult, *seekRefusal) {
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	sp, why := s.currentSeekablePlay()
	if why != "" {
		return seekResult{}, &seekRefusal{http.StatusConflict, "not-seekable", why}
	}
	tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	state, err := s.renderer.TransportState(tctx)
	cancel()
	if err != nil {
		return seekResult{}, &seekRefusal{http.StatusBadGateway, "speaker-unreachable",
			"the speaker did not say whether it is playing: " + err.Error()}
	}
	if state != "PLAYING" {
		return seekResult{}, &seekRefusal{http.StatusConflict, "not-playing",
			"the speaker is not playing (" + state + "); a jump is only taken while the track plays"}
	}
	pctx, pcancel := context.WithTimeout(ctx, positionTimeout)
	from, total, _ := s.renderer.PositionInfo(pctx)
	pcancel()
	// The speaker's own total is what its Seek is measured against; STR's
	// length stands in for a speaker that reports none.
	length := total
	if length <= 0 {
		length = sp.dur
	}
	if length <= 0 {
		return seekResult{}, &seekRefusal{http.StatusConflict, "length-unknown",
			"the length of this track is not known, so there is no position to jump to"}
	}
	to, ok := clampSeekTarget(target, length)
	if !ok {
		return seekResult{}, &seekRefusal{http.StatusBadRequest, "out-of-range",
			"position " + target.Round(time.Second).String() + " is outside the track (" + length.Round(time.Second).String() + ")"}
	}
	sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
	err = s.renderer.Seek(sctx, to)
	scancel()
	if err != nil {
		s.logger.Warn("seek: the speaker refused the jump", "title", sp.title,
			"from", from.String(), "to", to.String(), "length", length.String(), "folder", sp.queue, "err", err)
		return seekResult{}, &seekRefusal{http.StatusBadGateway, "seek-refused",
			"the speaker refused the jump: " + err.Error()}
	}
	s.userSeek.record(userSeekNote{at: time.Now(), pos: to, queue: sp.queue, queueGen: sp.queueGen, fileGen: sp.fileGen})
	s.logger.Info("seek: jumped inside the playing track", "title", sp.title,
		"from", from.String(), "to", to.String(), "length", length.String(), "folder", sp.queue)
	return seekResult{to: to, length: length}, nil
}

// handleSeek is POST /api/seek {"positionSec": N}: jump to second N of the
// playing music-library file. Answers 409 with a reason when what plays cannot
// be positioned (radio, Spotify, a proxied track, a paused speaker), 400 for a
// position outside the track and 502 when the speaker refuses.
func (s *Server) handleSeek(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		PositionSec *float64 `json:"positionSec"`
	}
	if !decodeJSONRequest(w, r, 1<<12, &req) {
		return
	}
	if req.PositionSec == nil || math.IsNaN(*req.PositionSec) || math.IsInf(*req.PositionSec, 0) ||
		*req.PositionSec < 0 || *req.PositionSec > (24*time.Hour).Seconds() {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "positionSec must be a position in seconds inside the track", "reason": "bad-position"})
		return
	}
	target := time.Duration(*req.PositionSec * float64(time.Second))
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	res, ref := s.seekTo(ctx, target)
	if ref != nil {
		if ref.reason != "seek-refused" {
			s.logger.Info("seek: not done", "reason", ref.reason, "why", ref.msg, "wanted", target.String())
		}
		writeJSON(w, ref.code, map[string]string{"error": ref.msg, "reason": ref.reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"positionSec": int(res.to.Seconds()),
		"durationSec": int(res.length.Seconds()),
	})
}
