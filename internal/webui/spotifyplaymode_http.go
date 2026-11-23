package webui

// /api/spotify/playmode: the desktop app's shuffle and repeat buttons for a
// Spotify playlist (#2058).
//
// POST {"action":"shuffle"} switches the playlist's shuffle on or off, POST
// {"action":"repeat"} steps its repeat the way the Spotify app does (off -> the
// whole playlist -> the one song -> off). It is the remote key's own step
// (playmodekey.go): the state is read fresh from the engine first, so a press
// always moves on from what is really set, even when the Spotify app changed
// it since. Unlike the key it shows nothing on the speaker's display: the
// button in the app is the answer. A key press and an app press run one at a
// time.
//
// GET answers the live state without changing it. The app does not poll it:
// /spotify/info carries the same state on the now-playing refresh the app
// already makes (internal/spotify ServeInfo).
//
// An agent that has this says "spotifyPlayModes":"true" in /api/agent/version.

import (
	"context"
	"net/http"
	"time"

	"github.com/JRpersonal/streborn/internal/keyactions"
)

// playModeAPITimeout bounds one request: the step (the Spotify check, the
// engine read and its one or two writes) and the read of the answer.
const playModeAPITimeout = 8 * time.Second

// handleSpotifyPlayMode answers 200 {"ok":true,"shuffle":bool,"repeat":mode}
// with the state after the step (a POST) or now (a GET); mode is "off",
// "context" or "track". After a POST the mode it changed always comes from
// the step itself, the other one from a fresh engine read, and is left out
// when that read fails. Refusals are {"error","reason"}: 409 "not-playing"
// when STR's Spotify is not what the speaker plays (also paused counts as
// playing), 409 "no-engine" on a speaker without the engine, 400 "bad-body",
// 502 "engine-refused" when the engine did not take or give the state.
func (s *Server) handleSpotifyPlayMode(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	if r.Method == http.MethodGet {
		s.answerSpotifyPlayModes(w, r)
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if !decodeJSONRequest(w, r, 1<<12, &req) {
		return
	}
	if req.Action != keyactions.ActionShuffle && req.Action != keyactions.ActionRepeat {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": `the body must be {"action":"shuffle"} or {"action":"repeat"}`, "reason": "bad-body"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), playModeAPITimeout)
	defer cancel()
	k := &s.playModeKey
	k.mu.Lock()
	st, reason, err := s.stepPlayModeLocked(ctx, req.Action)
	k.mu.Unlock()
	if reason != "" {
		code, msg := playModeRefusalAnswer(reason)
		if err != nil {
			s.logger.Warn("control API: Spotify did not take the "+req.Action+" change", "err", err)
		} else {
			s.logger.Info("control API: "+req.Action+" not changed", "reason", reason)
		}
		writeJSON(w, code, map[string]string{"error": msg, "reason": reason})
		return
	}
	out := map[string]any{"ok": true}
	if s.spotifyPlayModes != nil {
		if shuffle, repeat, rerr := s.spotifyPlayModes(ctx); rerr == nil {
			out["shuffle"], out["repeat"] = shuffle, repeat
		}
	}
	if req.Action == keyactions.ActionShuffle {
		out["shuffle"] = st.shuffle
		s.logger.Info("control API: shuffle switched", "shuffle", st.shuffle)
	} else {
		out["repeat"] = st.repeat
		s.logger.Info("control API: repeat stepped", "repeat", st.repeat)
	}
	writeJSON(w, http.StatusOK, out)
}

// answerSpotifyPlayModes is the GET: the engine's live state, changed by
// nothing.
func (s *Server) answerSpotifyPlayModes(w http.ResponseWriter, r *http.Request) {
	if s.spotifyPlayModes == nil {
		code, msg := playModeRefusalAnswer(playModeNoEngine)
		writeJSON(w, code, map[string]string{"error": msg, "reason": playModeNoEngine})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), playModeAPITimeout)
	defer cancel()
	shuffle, repeat, err := s.spotifyPlayModes(ctx)
	if err != nil {
		code, msg := playModeRefusalAnswer(playModeEngineRefused)
		writeJSON(w, code, map[string]string{"error": msg, "reason": playModeEngineRefused})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "shuffle": shuffle, "repeat": repeat})
}

// playModeRefusalAnswer is the status and the sentence for a refusal: the
// speaker's state is a 409 (asking again later can work), the engine's
// refusal a 502.
func playModeRefusalAnswer(reason string) (int, string) {
	switch reason {
	case playModeNotPlaying:
		return http.StatusConflict, "Spotify on STR is not what the speaker plays"
	case playModeNoEngine:
		return http.StatusConflict, "this speaker has no Spotify engine that can do this"
	}
	return http.StatusBadGateway, "the Spotify engine did not take or give the shuffle and repeat state"
}
