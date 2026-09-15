package webui

import "time"

// queuelog.go gives the library play queue a record of what it did.
//
// #960: a folder stopped part way through and the diagnostic bundle could not
// say why, because every moment in the queue's life was silent. The start wrote
// no line, neither did an advance, the exhausted branch, or any of the seven
// places that stop a queue, and there was no debug section either. In a log
// "the folder finished", "the watcher gave up on a track that never started"
// and "somebody pressed stop" were the same thing: nothing.
//
// The record is deliberately small and free of track titles. Counters and
// indices answer the question that is actually asked ("did it play 3 of 12 or
// 12 of 12, and what ended it"), and a bundle posted in a public issue then
// carries no one's file names. It lives in memory only: nothing here touches
// NAND.

// queueEpisodeKeep is how many finished episodes the debug section keeps. Five
// covers the usual "it stopped again, here is a fresh bundle" round trip
// without the section growing unbounded on a box that plays all day.
const queueEpisodeKeep = 5

// queueEpisode is one queue from start to end.
type queueEpisode struct {
	StartedAt  string `json:"startedAt"`
	Tracks     int    `json:"tracks"`
	StartIndex int    `json:"startIndex"`
	Shuffle    bool   `json:"shuffle"`
	Repeat     string `json:"repeat"`
	// Advances counts tracks the queue moved to after the first, split by who
	// asked: the watcher (auto) or a person pressing next/previous (manual).
	Auto   int `json:"autoAdvances"`
	Manual int `json:"manualSkips"`
	Failed int `json:"failedPushes"`
	// EndReason is empty while the episode is still running.
	EndReason string `json:"endReason,omitempty"`
	EndedAt   string `json:"endedAt,omitempty"`
	RanForSec int    `json:"ranForSec"`

	startedAt time.Time
}

// noteQueueStart opens an episode. An episode that was still open is closed
// first: starting a queue while one runs IS how the old one ended.
func (s *Server) noteQueueStart(tracks, start int, shuffle bool, rep repeatMode) {
	s.queueLogMu.Lock()
	if s.queueEp != nil {
		s.closeEpisodeLocked("replaced by a new queue")
	}
	now := time.Now()
	s.queueEp = &queueEpisode{
		StartedAt:  now.Format(time.RFC3339),
		Tracks:     tracks,
		StartIndex: start,
		Shuffle:    shuffle,
		Repeat:     rep.String(),
		startedAt:  now,
	}
	s.queueLogMu.Unlock()
	s.logger.Info("queue start", "tracks", tracks, "startIndex", start,
		"shuffle", shuffle, "repeat", rep.String())
}

// noteQueueAdvance records a move to the next track. why names which detector
// decided it, so a bundle distinguishes a clean STOP frame from the wall-clock
// net from the frozen-position net; those three mean very different things
// about the box and the media server.
func (s *Server) noteQueueAdvance(natural bool, why, title string, dur time.Duration) {
	s.queueLogMu.Lock()
	if ep := s.queueEp; ep != nil {
		if natural {
			ep.Auto++
		} else {
			ep.Manual++
		}
	}
	s.queueLogMu.Unlock()
	// The title goes in the LOG line (the reporter's own agent log, which they
	// choose to send) but never into the debug section, which is the part that
	// gets pasted into public issues.
	s.logger.Info("queue advance", "why", why, "natural", natural,
		"title", title, "lengthSec", int(dur.Seconds()))
}

// noteQueuePushFailed records an advance whose push to the box did not land.
// Counted separately: a queue that ends after three failed pushes is a delivery
// problem, not a short folder.
func (s *Server) noteQueuePushFailed() {
	s.queueLogMu.Lock()
	if ep := s.queueEp; ep != nil {
		ep.Failed++
	}
	s.queueLogMu.Unlock()
}

// noteQueueEnd closes the open episode and says why. Calling it with no episode
// open is a no-op, which is the normal case: most of the stopQueue callers fire
// on a plain radio play with no queue anywhere near it.
func (s *Server) noteQueueEnd(reason string) {
	s.queueLogMu.Lock()
	ep := s.queueEp
	if ep == nil {
		s.queueLogMu.Unlock()
		return
	}
	s.closeEpisodeLocked(reason)
	s.queueLogMu.Unlock()
	s.logger.Info("queue end", "reason", reason, "tracks", ep.Tracks,
		"autoAdvances", ep.Auto, "manualSkips", ep.Manual,
		"failedPushes", ep.Failed, "ranForSec", ep.RanForSec)
}

// closeEpisodeLocked moves the open episode into the history ring. Caller holds
// queueLogMu.
func (s *Server) closeEpisodeLocked(reason string) {
	ep := s.queueEp
	if ep == nil {
		return
	}
	ep.EndReason = reason
	ep.EndedAt = time.Now().Format(time.RFC3339)
	ep.RanForSec = int(time.Since(ep.startedAt).Seconds())
	s.queueEpPast = append(s.queueEpPast, *ep)
	if len(s.queueEpPast) > queueEpisodeKeep {
		s.queueEpPast = s.queueEpPast[len(s.queueEpPast)-queueEpisodeKeep:]
	}
	s.queueEp = nil
}

// QueueForensics is the "queue_episodes" section of /api/debug/state: the queue
// running right now (if any) and the last few that ended, with the reason each
// one ended.
func (s *Server) QueueForensics() any {
	s.queueLogMu.Lock()
	defer s.queueLogMu.Unlock()
	out := map[string]any{
		"recent": append([]queueEpisode(nil), s.queueEpPast...),
	}
	if ep := s.queueEp; ep != nil {
		live := *ep
		live.RanForSec = int(time.Since(ep.startedAt).Seconds())
		out["current"] = live
	}
	return out
}
