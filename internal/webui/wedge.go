package webui

// Wedged-control detection.
//
// Field case (SoundTouch 300, 2026-07-09): the box answers :8090, the agent
// runs, presets are accepted — but the speaker blinks its boot pattern, the
// firmware never pulls the stream URL it just accepted and never reaches a
// playing state. Software reboots do not clear it; only a power-cycle does.
// The user cannot tell this state from a normal boot, so STR now detects it
// and says the one thing that helps: pull the plug.
//
// Signal: a recall verify that exhausts its retries while (a) the box is
// awake (not STANDBY — a user power-off mid-recall is not a wedge), (b) the
// box never opened any proxied stream in the window, and (c) no upstream
// stream failure was recorded (that would be a dead STATION, not a dead box).
// Two such strikes in a row latch boxHealth=wedged, surfaced via
// /api/agent/version to the desktop app and the phone remote. Any observed
// playback clears the state.

import (
	"context"
	"sync"
	"time"
)

// wedgeStrikeWindow is how far back a proxy fetch / upstream failure absolves
// an exhausted recall: it spans the recall's own retry cycle (3x5 s + pushes).
const wedgeStrikeWindow = 90 * time.Second

// wedgeStrikesToLatch is how many consecutive absolved-by-nothing recall
// failures latch the wedged state. Two keeps a single odd failure quiet.
const wedgeStrikesToLatch = 2

type wedgeState struct {
	mu      sync.Mutex
	strikes int
	wedged  bool
	since   time.Time
}

// SetStreamActivityFn wires the stream proxy's LastActivity so the wedge
// detector can tell "box never pulled the stream" from "station failed".
func (s *Server) SetStreamActivityFn(fn func() (lastFetch, lastFailure time.Time)) {
	s.streamActivityFn = fn
}

// NoteRecallExhausted is called when a play/recall verify gave up. It decides
// whether this failure looks like the box (not the station) and counts a
// strike; the second consecutive strike latches wedged.
func (s *Server) NoteRecallExhausted() {
	// A user power-off mid-recall exhausts the verify too; standby is not a
	// wedge. Read the live state once, best-effort.
	npCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	np := s.snapshotNowPlaying(npCtx)
	cancel()
	if np.Source == "STANDBY" || np.Source == "" {
		return
	}
	if s.streamActivityFn != nil {
		fetch, fail := s.streamActivityFn()
		if (!fetch.IsZero() && time.Since(fetch) < wedgeStrikeWindow) ||
			(!fail.IsZero() && time.Since(fail) < wedgeStrikeWindow) {
			// The box pulled a stream (or the station demonstrably failed):
			// the problem is the content path, not a wedged box.
			return
		}
	}
	s.wedge.mu.Lock()
	s.wedge.strikes++
	latch := s.wedge.strikes >= wedgeStrikesToLatch && !s.wedge.wedged
	if latch {
		s.wedge.wedged = true
		s.wedge.since = time.Now()
	}
	strikes := s.wedge.strikes
	s.wedge.mu.Unlock()
	if latch {
		s.logger.Warn("box wedge detected: transport accepted but the box never pulls the stream and never plays; a power-cycle is required (software reboots do not clear this state)",
			"strikes", strikes)
	} else {
		s.logger.Warn("box wedge suspected (strike recorded)", "strikes", strikes)
	}
}

// NoteBoxHealthy clears the wedge state; called whenever playback is actually
// observed (a verify succeeding, the box attaching to a stream).
func (s *Server) NoteBoxHealthy() {
	s.wedge.mu.Lock()
	wasWedged := s.wedge.wedged
	s.wedge.strikes = 0
	s.wedge.wedged = false
	s.wedge.since = time.Time{}
	s.wedge.mu.Unlock()
	if wasWedged {
		s.logger.Info("box wedge cleared: playback observed")
	}
}

// BoxHealth reports "ok" or "wedged" (plus the latch time for the latter).
func (s *Server) BoxHealth() (status string, since time.Time) {
	s.wedge.mu.Lock()
	defer s.wedge.mu.Unlock()
	if s.wedge.wedged {
		return "wedged", s.wedge.since
	}
	return "ok", time.Time{}
}
