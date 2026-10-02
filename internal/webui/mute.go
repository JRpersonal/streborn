package webui

import (
	"context"
	"net/http"
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
		// Already where the caller wants it. Pressing the key anyway would
		// move it the other way, which is the one thing a "set" must never do.
		writeJSON(w, http.StatusOK, map[string]any{"muted": want, "changed": false})
		return
	}

	if err := c.Key(ctx, "MUTE"); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.logger.Info("box mute", "want", want, "was", before.Muted, "volume", before.Actual)
	writeJSON(w, http.StatusOK, map[string]any{"muted": want, "changed": true})
}
