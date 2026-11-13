package webui

// A library queue whose media server went away (#1890, 2026-11-13).
//
// A track the speaker drops is skipped, so one broken file does not end an
// album, and three dropped in a row stop the queue. When the whole media
// server is gone that rule is too slow and too quiet: a bundle from an ST10
// shows the speaker trying two more tracks of a server that had been switched
// off, then "stopped" in the app with no word of why, while the agent's own
// log said "the music server stopped delivering".
//
// So before a dropped track is skipped, the next track's file is asked for
// once (one ranged GET, bounded by trackProbeTimeout). No answer at all means
// the server is gone: the queue ends at once, and the app and the phone page
// say "music server not responding" until something else plays. A server
// that answers, even with an error, is left to the old rule.

import "time"

// queueEndServerGone is the end reason the queue log and the watcher's line
// carry for a media server that stopped answering.
const queueEndServerGone = "the music server stopped delivering; it no longer answers"

// queueLastEndServerGone is LastEnd.Reason for that end, the key the app and
// the phone page translate.
const queueLastEndServerGone = "server_gone"

// queueLastEnd is the /api/queue view of queueEndNote.
type queueLastEnd struct {
	Reason string `json:"reason"`
	At     string `json:"at"`
}

// queueEndNote is a queue end worth telling the listener about. playGen is
// the play generation at the end: any play STR starts afterwards moves it on,
// and the note is then history.
type queueEndNote struct {
	reason  string
	at      time.Time
	playGen uint64
}

// queueNextServerGone reports whether the media server of the track the queue
// would play next gives no answer at all. A server that answers, whatever it
// answers, is not gone; nor is a queue with no next track or a next track that
// is not a plain HTTP file.
func (s *Server) queueNextServerGone() bool {
	if s.queue == nil {
		return false
	}
	next, ok := s.queue.peekNatural()
	if !ok || !isHTTPURL(next.URL) {
		return false
	}
	_, err := s.probeTrackStatus(s.queueCtx(), next.URL)
	return err != nil
}

// stopQueueServerGone ends the queue because its media server stopped
// answering, and keeps the reason for the app until something else plays.
func (s *Server) stopQueueServerGone() {
	s.queueMu.Lock()
	s.queueLastEnd = queueEndNote{reason: queueLastEndServerGone, at: time.Now(), playGen: s.play.gen()}
	s.queueMu.Unlock()
	s.stopQueue(queueEndServerGone)
}

// queueLastEndShown is the LastEnd for /api/queue: the kept end, as long as no
// play has started since. Nil otherwise.
func (s *Server) queueLastEndShown() *queueLastEnd {
	s.queueMu.Lock()
	n := s.queueLastEnd
	s.queueMu.Unlock()
	if n.reason == "" || n.playGen != s.play.gen() {
		return nil
	}
	return &queueLastEnd{Reason: n.reason, At: n.at.UTC().Format(time.RFC3339)}
}
