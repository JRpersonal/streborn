package webui

// POST /api/spotify/liked: an app's thumbs up and thumbs down (#1165).
//
// The same thing the remote's thumbs keys do (likekey.go), for a companion
// app: {"liked":true} saves the playing Spotify song to Liked Songs,
// {"liked":false} removes it. It runs through the keys' own function, so the
// rules are the same ones: only while STR's own Spotify engine is what the
// speaker plays (also paused), a repeat of the same action on the same song
// is answered from the engine's memory without a second write, and a speaker
// with a display shows LIKED or REMOVED.
//
// The list is the Liked Songs of the account the SPEAKER's engine is logged
// in with, which is not necessarily the account on the phone that asks.
//
// An agent that has this says "spotifyLiked":"true" in /api/agent/version.

import (
	"context"
	"net/http"
)

// handleSpotifyLiked answers 200 {"ok":true,"liked","uri","name","repeat"},
// or {"error","reason"} with 409 when there is nothing to save (STR's
// Spotify is not what plays, no song is loaded, this speaker has no Spotify
// engine), 400 for a body without a boolean "liked" and 502 when the engine
// or Spotify refused.
func (s *Server) handleSpotifyLiked(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var req struct {
		Liked *bool `json:"liked"`
	}
	if !decodeJSONRequest(w, r, 1<<12, &req) {
		return
	}
	if req.Liked == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": `the body must be {"liked":true} or {"liked":false}`, "reason": "bad-body"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), likeKeyTimeout)
	defer cancel()
	tr, ref := s.setLiked(ctx, *req.Liked, "control API", "control API asked")
	if ref != nil {
		code, msg := likeRefusalAnswer(ref.reason, *req.Liked)
		writeJSON(w, code, map[string]string{"error": msg, "reason": ref.reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"liked":  *req.Liked,
		"uri":    tr.URI,
		"name":   tr.Name,
		"repeat": tr.Repeat,
	})
}

// likeRefusalAnswer is the status and the sentence for a refusal. The state
// of the speaker is a 409 (asking again later can work); everything the
// engine or Spotify refused is a 502.
func likeRefusalAnswer(reason string, liked bool) (int, string) {
	switch reason {
	case "not-playing":
		return http.StatusConflict, "Spotify on STR is not what the speaker plays"
	case "no-engine":
		return http.StatusConflict, "this speaker has no Spotify engine that can do this"
	case "no-track":
		return http.StatusConflict, "the Spotify engine has no song loaded"
	case "not-a-song":
		return http.StatusConflict, "what plays is not a song (an episode cannot go to Liked Songs)"
	}
	if liked {
		return http.StatusBadGateway, "the song was not saved to Liked Songs"
	}
	return http.StatusBadGateway, "the song was not removed from Liked Songs"
}
