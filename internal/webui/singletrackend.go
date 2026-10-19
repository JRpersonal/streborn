package webui

import (
	"context"
	"time"

	"github.com/JRpersonal/streborn/internal/upnp"
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

// singleTrack is what the end watch needs to know about the file it watches:
// enough to recognise its end, and enough to play it once more when the
// speaker's sticky repeat mode says "repeat one".
type singleTrack struct {
	boxURL string // the URL the box was handed (direct: the media server's own)
	title  string
	art    string
	mime   string
	meta   upnp.TrackMeta
	dur    time.Duration // the length the caller knew, 0 when unknown
}

// armSingleTrackEnd watches one directly played file and stops the box when it
// reaches its end, or plays it again when repeat-one is on. gen is the recall
// generation of this play: any newer play supersedes this watch immediately.
func (s *Server) armSingleTrackEnd(tr singleTrack, gen uint64) {
	if s.renderer == nil {
		return
	}
	go s.watchSingleTrackEnd(tr, gen)
}

// Repeat one for a lone track (#1065, Issue 5).
//
// The repeat toggle on the Library screen is sticky on the SPEAKER: it lands in
// the play-mode file (playmode.go) whether or not a folder is playing. A folder
// honoured "repeat one" through the queue, but a single track is deliberately
// played without a queue, and its end watch stopped the box unconditionally. So
// a reporter who set "repeat one" and clicked one track got one play, then the
// SoundTouch light went amber and silence (her workaround was the Bose app's
// own repeat). The watch now asks the sticky mode at each natural end.
//
// Only "one" repeats a lone track. "all" is the folder mode, and letting it loop
// a single click forever would surprise everybody who set it for their folders.
func (s *Server) singleTrackRepeatOne() bool {
	_, rep, ok := s.loadPlayMode()
	return ok && rep == repeatOne
}

// replaySingleTrack plays the same file again for repeat-one. It re-checks,
// under boxCmdMu, that nothing newer took the box while the end was being
// detected, the same way the queue's advance does. It returns the generation
// of the new play, which the watch continues under.
func (s *Server) replaySingleTrack(tr singleTrack, gen uint64) (uint64, bool) {
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	if s.RecallGeneration() != gen || s.queue.isActive() || s.userStoppedRecently() {
		return 0, false
	}
	s.ClearUserStop()
	if err := s.renderer.PlayURLTrack(s.queueCtx(), tr.boxURL, tr.title, tr.art, tr.mime, tr.meta); err != nil {
		s.logger.Warn("single track: repeat one could not start the track again", "title", tr.title, "err", err)
		return 0, false
	}
	newGen := s.setLastPlay(tr.boxURL, tr.title, tr.art, tr.mime)
	s.logger.Info("single track: repeat one, playing it again", "title", tr.title)
	return newGen, true
}

// retrySingleTrackStart wakes the speaker and hands it the file once more,
// for a play the speaker never started (#1322). A Wave left on its own source
// before it went to standby took STR's wake and push, never activated the
// stream, and dropped back to standby 30 s later: the app said "playing", the
// room stayed silent. The second wake reached the speaker every time in that
// bundle, so one more try is what the user would have done by hand.
//
// It re-checks, under boxCmdMu, that nothing newer took the box, that nobody
// stopped it, and that no key was pressed on the speaker since the play began
// (a key press is the listener choosing something else). It returns the new
// play's generation.
func (s *Server) retrySingleTrackStart(tr singleTrack, gen uint64, playStart time.Time, why string) (uint64, bool) {
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	if s.RecallGeneration() != gen || s.queue.isActive() || s.userStoppedRecently() {
		return 0, false
	}
	if s.userActivityFn != nil && s.userActivityFn().After(playStart) {
		s.logger.Info("single track: not retrying the start, a key was pressed on the speaker meanwhile",
			"title", tr.title)
		return 0, false
	}
	s.logger.Info("single track: waking the speaker and handing the track over once more",
		"title", tr.title, "why", why, "waitedSec", int(time.Since(playStart).Seconds()))
	ctx := s.queueCtx()
	s.ensureBoxReady(ctx)
	s.ClearUserStop()
	if err := s.playTrackWithWrongStateRepair(ctx, tr.boxURL, tr.title, tr.art, tr.mime, tr.meta); err != nil {
		s.logger.Warn("single track: the second start failed too", "title", tr.title, "err", err)
		return 0, false
	}
	return s.setLastPlay(tr.boxURL, tr.title, tr.art, tr.mime), true
}

func (s *Server) watchSingleTrackEnd(tr singleTrack, gen uint64) {
	for {
		ended, cur := s.watchOneSingleTrackPlay(tr, gen)
		if !ended {
			return
		}
		gen = cur
		if !s.singleTrackRepeatOne() {
			return
		}
		next, ok := s.replaySingleTrack(tr, gen)
		if !ok {
			return
		}
		gen = next
	}
}

// watchOneSingleTrackPlay watches one play of the file to its end. It returns
// true when the track ended naturally and repeat-one may play it again, false
// when the watch ended for any other reason or the box was stopped for good,
// plus the generation the play ended under (a start retry makes a new one).
func (s *Server) watchOneSingleTrackPlay(tr singleTrack, gen uint64) (bool, uint64) {
	ctx, cancel := context.WithTimeout(s.queueCtx(), singleTrackMaxWatch)
	defer cancel()
	ticker := time.NewTicker(queuePollInterval)
	defer ticker.Stop()

	title := tr.title
	start := time.Now()
	var (
		lastPos   time.Duration
		lastPosAt time.Time
		obsTotal  time.Duration
		sawPlay   bool
		// retried: the one start retry this play gets has been used.
		retried bool
	)
	for {
		select {
		case <-ctx.Done():
			return false, gen
		case <-ticker.C:
		}
		// Anything newer wins: another track, a preset key, a station, a
		// hardware recall. They all bump the recall generation, and stopping
		// the box now would stop THEIR audio.
		if s.RecallGeneration() != gen {
			s.logger.Debug("single track: a newer play superseded the end watch", "title", title)
			return false, gen
		}
		if s.queue.isActive() {
			// A folder started meanwhile owns the box; its watcher is the one
			// that should call the end.
			return false, gen
		}
		if s.userStoppedRecently() {
			return false, gen
		}

		ps, pos, total, standby, tornDown, _ := s.pollNowPlaying()
		if standby {
			// Powered off mid-track. Never a track end, and STR must not send
			// anything to a sleeping box (#219).
			return false, gen
		}
		if tornDown && !sawPlay && !retried && time.Since(start) < queueStallTimeout {
			// Not started yet, and the source reads torn down: on a speaker
			// that is still switching away from its own source on the way to
			// the stream that is a transition, not a refusal (#1322). The
			// stall branch below decides once the start has had its time.
			continue
		}
		if tornDown && !sawPlay && !retried {
			if next, ok := s.retrySingleTrackStart(tr, gen, start, "the speaker dropped the track before it started"); ok {
				gen, start, retried = next, time.Now(), true
				continue
			}
		}
		if tornDown {
			// The box dropped the source instead of playing. Same shape as the
			// queue case: nothing was heard, and waiting out the rest of the
			// track's nominal length would just be silence with a ticking
			// progress bar. There is no next track here, so end the watch.
			s.logger.Info("single track: the speaker dropped the track without playing it, ending the watch")
			return false, gen
		}
		if total > obsTotal {
			obsTotal = total
		}
		end := tr.dur
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
			// The box stopped by itself at the end, so there is nothing to
			// stop. With repeat-one the caller plays it again. Otherwise mark it
			// as an end, so the auto re-push does not treat the silence as a
			// dropped stream and start the track over.
			s.logger.Info("single track: the box reported the track stopped", "title", title,
				"posSec", int(lastPos.Seconds()), "trackSec", int(end.Seconds()))
			if s.singleTrackRepeatOne() {
				return true, gen
			}
			s.NoteUserStop()
			return false, gen
		default:
			if !sawPlay && time.Since(start) >= queueStallTimeout {
				if !retried {
					if next, ok := s.retrySingleTrackStart(tr, gen, start, "the speaker never started the track"); ok {
						gen, start, retried = next, time.Now(), true
						continue
					}
				}
				s.logger.Info("single track: it never started, dropping the end watch", "title", title)
				return false, gen
			}
			continue
		}

		// The queue's own wall-clock and frozen-position nets, including the
		// rule that a box still climbing past a wrong server length is not cut
		// off (#1065).
		in := s.trackEndSnapshot(sawPlay, ps, start, tr.dur, obsTotal, lastPos, lastPosAt)
		if net := trackEndNet(in); net != endNetNone {
			s.learnSingleTrackLength(tr, net, lastPos)
			return s.finishSingleTrack(title, net.why(), lastPos, end), gen
		}
	}
}

// finishSingleTrack handles a natural end the watch detected itself. With
// repeat-one the box is NOT stopped first: the replay hands it the same file
// right away, and a Stop in between would only add a gap and a STOP_STATE flash
// on every display. Without it, the box is stopped as before. It returns true
// when the caller should play the track again.
func (s *Server) finishSingleTrack(title, why string, pos, end time.Duration) bool {
	if s.singleTrackRepeatOne() {
		s.logger.Info("single track ended, repeat one is on", "title", title, "why", why,
			"posSec", int(pos.Seconds()), "trackSec", int(end.Seconds()))
		return true
	}
	s.endSingleTrack(title, why, pos, end)
	return false
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
