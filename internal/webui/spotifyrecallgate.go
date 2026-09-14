package webui

import (
	"context"
	"net/http"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// spotifyGateTimeout bounds the pre-wake probe. CanRecall is a local
// go-librespot /status read plus an on-disk credential check, so it answers in
// milliseconds on a healthy box; the timeout only stops a wedged sidecar from
// holding a preset press.
const spotifyGateTimeout = 4 * time.Second

// spotifyRecallRefused answers the preconditions for a Spotify preset recall
// and writes the refusal itself, reporting whether the caller must stop.
//
// It runs BEFORE the speaker is woken, and that ordering is the whole point.
//
// The firmware resumes whatever it last played when it is powered on, so the
// wake that a recall performs restarts that station a second or so before the
// recall is even evaluated. When the recall was then refused, the user had
// pressed key 1, seen an error on key 1, and was listening to key 6 (#948).
// Two releases tried to stop the resumed station again afterwards and both
// lost the race, because the box answers the stop with an HTTP 200 whether or
// not the audio actually stopped. None of these three checks needs an awake
// speaker: the engine handle is local state, CanRecall probes the sidecar on
// loopback and reads a file, and the account type is already known. Asked
// here, a doomed recall never sends `sys power`, so there is nothing to undo.
func (s *Server) spotifyRecallRefused(w http.ResponseWriter, slot int, p presets.Preset) bool {
	if p.Type != "spotify" || p.URI == "" {
		return false
	}
	if s.spotifyPlay == nil {
		s.logger.Warn("spotify preset recall (app): Spotify not configured on this box, not waking the speaker", "slot", slot)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "Spotify not configured", "slot": slot, "name": p.Name,
		})
		return true
	}
	// A speaker that has never been logged into Spotify AND holds no live
	// session has no way for go-librespot to start playback on its own, so the
	// recall would silently do nothing (#45 Pierre: saved preset account="" and
	// go-librespot not running). Tell the user how to fix it instead of
	// optimistically reporting "playing" and failing in the background. Gate on
	// CanRecall (live session OR persisted credential), NOT a persisted
	// credential alone: a box with a live-but-never-persisted zeroconf session
	// plays Spotify fine yet reports not-logged-in, and gating on the credential
	// alone wrongly refused its recall (Patrick, ST10, 2026-06-24).
	//
	// The context is detached and short (#252 in spirit): the caller's request
	// may be cancelled by an impatient app, and a cancelled probe would
	// misreport a logged-in speaker as "not picked in Spotify yet".
	if s.spotifyCanRecall != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), spotifyGateTimeout)
		ok := s.spotifyCanRecall(ctx)
		cancel()
		if !ok {
			s.logger.Info("spotify preset recall (app): speaker not logged into Spotify, not waking it", "slot", slot)
			// STR plays Spotify through this speaker as a Spotify Connect receiver
			// (the go-librespot sidecar), not via any Bose account link. The
			// speaker has to be picked in Spotify once so it stores a credential.
			// The desktop app branches on the code, so this wording is free to be
			// the accurate, non-Bose-linking instruction.
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": "This speaker has not been picked in Spotify yet. In the Spotify app on a device on the same Wi-Fi, tap the Connect/devices icon, choose this speaker and play any track once. After that this preset will recall on its own.",
				"code":  "spotify-not-logged-in",
				"slot":  slot, "name": p.Name,
			})
			return true
		}
	}
	// A free/open Spotify account cannot do the autonomous on-demand playback a
	// recall needs (it can only play when the phone app drives it), so the
	// recall would silently fail. Tell the user it needs Premium instead (#45).
	if s.spotifyPremiumRequired != nil && s.spotifyPremiumRequired() {
		s.logger.Info("spotify preset recall (app): account is free/open, recall needs Premium, not waking the speaker", "slot", slot)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "This speaker's Spotify account is free. Spotify preset recall needs Spotify Premium.",
			"code":  "spotify-premium-required",
			"slot":  slot, "name": p.Name,
		})
		return true
	}
	return false
}
