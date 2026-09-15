package webui

import (
	"context"
	"time"
)

// A single library track played on its own never ended.
//
// The Bose box emits no native "track finished" event, and on a finite file it
// finishes the bytes and then sits in PLAY_STATE with its position frozen at
// the end instead of reporting STOP (#380). A FOLDER survives that, because the
// queue watcher polls and calls the end itself. A lone track had nothing
// watching it at all: handlePlay stops the queue by design, so the app, the
// remote and the speaker's own display all kept showing it playing until the
// auto-off timer cut in twenty minutes later.
//
// Reported on a 2:07 track that the app still showed playing six and a half
// minutes on (#844). The reply that thread got explained a MIME-less track
// going through the stream proxy, which was a different play: this one went
// direct, as audio/x-flac, and stalled for this reason instead.
//
// So give the lone track the same end detection the queue has, without giving
// it a queue: no queue state, no transport controls, no queue card. It reuses
// the queue watcher's own primitives, which are the vetted ones, and it stops
// the box rather than advancing.
//
// It runs only while one finite file plays and exits when that file ends, so it
// adds no standing timer to a box that is idle or on radio.

// singleTrackMaxWatch caps the whole watch. A track longer than this is
// unusual enough (a DJ set, an audiobook chapter) that guessing its end is
// worse than leaving it alone, and the cap means a stuck poll can never leave
// a goroutine reading the box forever.
const singleTrackMaxWatch = 3 * time.Hour

// armSingleTrackEnd watches one directly played file and stops the box when it
// reaches its end. dur is the length the caller knew (0 when unknown, then the
// box's own reported total is used). gen is the recall generation of this play:
// any newer play supersedes this watch immediately.
func (s *Server) armSingleTrackEnd(dur time.Duration, gen uint64, title string) {
	if s.renderer == nil {
		return
	}
	go s.watchSingleTrackEnd(dur, gen, title)
}

func (s *Server) watchSingleTrackEnd(dur time.Duration, gen uint64, title string) {
	ctx, cancel := context.WithTimeout(s.queueCtx(), singleTrackMaxWatch)
	defer cancel()
	ticker := time.NewTicker(queuePollInterval)
	defer ticker.Stop()

	start := time.Now()
	var (
		lastPos   time.Duration
		lastPosAt time.Time
		obsTotal  time.Duration
		sawPlay   bool
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		// Anything newer wins: another track, a preset key, a station, a
		// hardware recall. They all bump the recall generation, and stopping
		// the box now would stop THEIR audio.
		if s.RecallGeneration() != gen {
			s.logger.Debug("single track: a newer play superseded the end watch", "title", title)
			return
		}
		if s.queue.isActive() {
			// A folder started meanwhile owns the box; its watcher is the one
			// that should call the end.
			return
		}
		if s.userStoppedRecently() {
			return
		}

		ps, pos, total, standby := s.pollNowPlaying()
		if standby {
			// Powered off mid-track. Never a track end, and STR must not send
			// anything to a sleeping box (#219).
			return
		}
		if total > obsTotal {
			obsTotal = total
		}
		end := dur
		if obsTotal > end {
			end = obsTotal
		}

		switch ps {
		case "PLAY_STATE", "BUFFERING_STATE":
			sawPlay = true
			if pos > lastPos {
				lastPos = pos
				lastPosAt = time.Now()
			}
			// Falls through to the nets below: a box frozen in PLAY_STATE at
			// EOF is the whole reason this exists.
		case "PAUSE_STATE":
			continue // paused: the wall clock must not run on
		case "STOP_STATE":
			if !sawPlay {
				continue // has not started yet
			}
			// The box stopped by itself, so there is nothing to stop. Mark it
			// as an end anyway, so the auto re-push does not treat the silence
			// as a dropped stream and start the track over.
			s.NoteUserStop()
			s.logger.Info("single track: the box reported the track stopped", "title", title,
				"posSec", int(lastPos.Seconds()), "trackSec", int(end.Seconds()))
			return
		default:
			if !sawPlay && time.Since(start) >= queueStallTimeout {
				s.logger.Info("single track: it never started, dropping the end watch", "title", title)
				return
			}
			continue
		}

		// Wall-clock net, the queue's own: once playback was seen and the
		// length is known, call the end a margin past it.
		if sawPlay && end > 0 && time.Since(start) >= end+s.advanceMargin(lastPos, end) {
			s.endSingleTrack(title, "the track's length elapsed without a stop from the box", lastPos, end)
			return
		}
		// Frozen-position net for the unknown-length case only, so a track with
		// a known length keeps the vetted wall-clock behaviour.
		if sawPlay && ps == "PLAY_STATE" && end == 0 && lastPos > 0 &&
			!lastPosAt.IsZero() && time.Since(lastPosAt) >= queueFrozenTimeout {
			s.endSingleTrack(title, "the box stayed on play with its position frozen", lastPos, end)
			return
		}
	}
}

// endSingleTrack stops the box the way the queue ends its last track:
// NoteUserStop first, so the 6 s guard suppresses an auto re-push of the
// stream we are deliberately ending, then Stop so now_playing goes STOP_STATE
// and the app, the phone remote and the speaker's display all update at once.
func (s *Server) endSingleTrack(title, why string, pos, end time.Duration) {
	s.NoteUserStop()
	if err := s.renderer.Stop(s.queueCtx()); err != nil {
		s.logger.Warn("single track: stopping the box at the end of the track failed",
			"title", title, "why", why, "err", err)
		return
	}
	s.logger.Info("single track ended", "title", title, "why", why,
		"posSec", int(pos.Seconds()), "trackSec", int(end.Seconds()))
}
