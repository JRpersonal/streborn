package webui

// The GET /api/presets read log, rate-limited per client.
//
// The empty-answer WARN used to fire on every GET. Two phone remotes polling
// a speaker with an empty store every 15 s wrote 250 lines in 19 minutes,
// which evicted every boot line from the 32 KB NAND log tail a diagnostic
// bundle carries (#882: the one boot the bundle needed to show was gone). One
// line per client per window says the same thing; the count of what was left
// out rides on the next line.
//
// Non-empty answers are the steady state of every healthy box, and the agent
// log is an unbuffered append to NAND, so they must not cost a periodic
// write: a client gets ONE "served" line, on its first read or when the store
// turns from empty to non-empty, and stays silent after that. That one line
// is all a bundle needs to prove the app was handed the list it asked for
// (non-empty reads used to be Debug, invisible in a bundle).

import (
	"net"
	"time"
)

// presetsReadLogWindow is how long one client's empty-answer WARN stands
// before the next one for that client is written. An hour is enough: the
// suppressed count on the next line carries how often the client asked.
const presetsReadLogWindow = time.Hour

// presetsReadMark is the last line written for one client.
type presetsReadMark struct {
	at         time.Time
	empty      bool
	suppressed int
}

// notePresetsRead logs one GET /api/presets answer and says how many
// identical answers went unlogged since the last line for that client. An
// empty answer is logged at most once per client per window; a non-empty
// answer only on the client's first read or right after an empty phase. A
// change between empty and non-empty is therefore always logged at once:
// that transition is the one a bundle has to show.
func (s *Server) notePresetsRead(remoteAddr string, count int) {
	ip := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		ip = h
	}
	now := time.Now()
	if s.presetsReadNow != nil {
		now = s.presetsReadNow()
	}
	empty := count == 0
	s.presetsReadMu.Lock()
	if s.presetsReadLast == nil {
		s.presetsReadLast = map[string]*presetsReadMark{}
	}
	m := s.presetsReadLast[ip]
	if m != nil && m.empty == empty {
		// Same answer as the last line. Non-empty: silent for good, the
		// steady state of a healthy box must not write to NAND. Empty: silent
		// until the window has passed.
		if !empty || now.Sub(m.at) < presetsReadLogWindow {
			m.suppressed++
			s.presetsReadMu.Unlock()
			return
		}
	}
	suppressed := 0
	if m != nil {
		suppressed = m.suppressed
	}
	// A LAN has a handful of clients; a runaway map is not worth a real cache.
	if len(s.presetsReadLast) > 64 {
		s.presetsReadLast = map[string]*presetsReadMark{}
	}
	s.presetsReadLast[ip] = &presetsReadMark{at: now, empty: empty}
	s.presetsReadMu.Unlock()
	if s.logger == nil {
		return
	}
	if empty {
		// Phase marker for the "presets reported empty" symptom (#60): a bundle
		// shows the client was served an empty array, not that it never reached
		// the agent.
		s.logger.Warn("preset store phase: GET /api/presets returned empty",
			"remote", ip, "suppressed", suppressed, "window", presetsReadLogWindow.String())
		return
	}
	s.logger.Info("GET /api/presets served", "count", count, "remote", ip, "suppressed", suppressed)
}
