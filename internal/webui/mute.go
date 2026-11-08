package webui

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// Muting a speaker, which STR could not do at all.
//
// Asked for by a user on 2026-10-02 who wanted it on the power symbol, and the
// gap was real: the word "mute" appeared nowhere in STR except in the list of
// remote keys the box REPORTS pressing. The firmware has had it the whole time,
// /volume answers with a muted flag, and the speaker's own MUTE key is one
// /key POST away.
//
// Pulling the volume to 0 and remembering the old value would have been the
// obvious shortcut and the wrong one: the speaker's own remote and its physical
// buttons would not agree with it, the box's muted flag would stay false, and a
// restore after a crash or an app restart would have nothing to restore from.
// The native key keeps one source of truth, the speaker's.

// muteDecision says what the speaker should end up as, and whether its key
// has to be pressed to get there.
//
// Pure, because this is the whole risk of the feature and the transport
// cannot be faked: boxapi refuses a host carrying a port, by design, so a
// test server cannot stand in for a speaker. The decision can be exercised
// exhaustively here instead.
//
// want nil means toggle. Otherwise the speaker is pressed only when it
// disagrees, because the key is a TOGGLE and pressing it on a speaker that is
// already where the caller wants it moves it the other way, which is the one
// thing a "set" must never do.
func muteDecision(current bool, want *bool) (target bool, press bool) {
	if want == nil {
		return !current, true
	}
	return *want, *want != current
}

// handleBoxMute toggles or sets the speaker's mute.
//
// POST with no body toggles, which is what a button does. POST {"muted":true}
// or {"muted":false} sets a definite state, which is what home automation and a
// second client need, because a toggle sent twice by two clients is a no-op
// nobody can see.
//
// The speaker's key is a TOGGLE, so setting a state means reading first and
// pressing only when the two disagree. Answering with the state that resulted
// means the caller never has to guess.
func (s *Server) handleBoxMute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var req struct {
		// Muted is a pointer so "not given" is distinguishable from false:
		// an empty body means toggle, {"muted":false} means unmute.
		Muted *bool `json:"muted"`
	}
	if r.ContentLength > 0 && !decodeJSONRequest(w, r, 256, &req) {
		return
	}

	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	c := boxapi.New(s.boxHost)
	before, err := c.GetVolume(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	want, press := muteDecision(before.Muted, req.Muted)
	if !press {
		s.NoteBoxMute(want)
		// Already where the caller wants it. Pressing the key anyway would
		// move it the other way, which is the one thing a "set" must never do.
		writeJSON(w, http.StatusOK, map[string]any{"muted": want, "changed": false})
		return
	}

	if err := c.Key(ctx, "MUTE"); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.NoteBoxMute(want)
	s.logger.Info("box mute", "want", want, "was", before.Muted, "volume", before.Actual)
	writeJSON(w, http.StatusOK, map[string]any{"muted": want, "changed": true})
}

// The phone remote's mute glyph follows a mute set anywhere else (#1748).
//
// The page polls /api/status every five seconds and nothing else, and the mute
// flag lived only in /api/box/settings, which it reads on load and on a return
// to the page. So a mute set in the desktop app or with the speaker's remote
// never reached the glyph while the page stayed open. Reading /volume on every
// status poll would cost the speaker a request per poll per open phone; the
// speaker already announces every volume and mute change on its gabbo bus, so
// the agent keeps the flag from those frames and hands it out with the status,
// in a header for the same reason the playing key is one: the body stays the
// speaker's own XML, which every client regex-parses.

// mutedHeader carries the speaker's mute flag on /api/status: "1" muted, "0"
// not, absent while the agent does not know it (no frame since it started, or
// since the bus last reconnected and may have missed one).
const mutedHeader = "X-STR-Muted"

// boxMuteState is the last mute flag the speaker reported.
type boxMuteState struct {
	mu    sync.Mutex
	known bool
	muted bool
}

// NoteBoxMute records the speaker's mute flag, from a gabbo volume frame or a
// read STR made anyway. Called on the gabbo read loop: never blocks.
func (s *Server) NoteBoxMute(muted bool) {
	s.boxMute.mu.Lock()
	s.boxMute.known, s.boxMute.muted = true, muted
	s.boxMute.mu.Unlock()
}

// ForgetBoxMute drops the recorded flag: after a bus reconnect a change made
// while the socket was down never arrived, and claiming the old state would be
// worse than not saying anything (the page then keeps what it last read).
func (s *Server) ForgetBoxMute() {
	s.boxMute.mu.Lock()
	s.boxMute.known = false
	s.boxMute.mu.Unlock()
}

// setMutedHeader puts the recorded flag on a status response, if there is one.
func (s *Server) setMutedHeader(w http.ResponseWriter) {
	s.boxMute.mu.Lock()
	known, muted := s.boxMute.known, s.boxMute.muted
	s.boxMute.mu.Unlock()
	if !known {
		return
	}
	v := "0"
	if muted {
		v = "1"
	}
	w.Header().Set(mutedHeader, v)
}
