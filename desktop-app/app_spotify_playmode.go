package main

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"time"
)

// SpotifyPlayModes is a Spotify playlist's shuffle and repeat as the speaker's
// engine has them (#2058). Repeat is "off", "context" (the whole playlist) or
// "track" (the one song).
type SpotifyPlayModes struct {
	Shuffle bool   `json:"shuffle"`
	Repeat  string `json:"repeat"`
}

// spotifyPlayModeUnsupported is what SpotifyPlayMode returns when the
// speaker's agent is older than the endpoint. The frontend then hides the
// player's Spotify shuffle and repeat buttons for that speaker instead of
// showing an error.
const spotifyPlayModeUnsupported = "spotify_playmode_unsupported"

// spotifyPlayModeTimeout covers the agent's side of one press: the check that
// Spotify plays (up to 4 s on a slow speaker), the engine read and its one or
// two writes, and the read of the answer.
const spotifyPlayModeTimeout = 10 * time.Second

// SpotifyPlayMode presses the player's shuffle ("shuffle") or repeat
// ("repeat") button for the Spotify playlist the speaker plays (#2058). It is
// the step a remote key bound to shuffle or repeat does on the speaker (#1679),
// without the display notice: shuffle on or off, repeat off -> the whole
// playlist -> the one song -> off, each moving on from the state the engine
// really has. The answer carries "shuffle" and "repeat" after the step; one of
// them can be missing when the speaker could not read it back.
func (a *App) SpotifyPlayMode(host string, port int, action string) (map[string]any, error) {
	if action != "shuffle" && action != "repeat" {
		return nil, fmt.Errorf("unknown play mode action %q", action)
	}
	body, _ := json.Marshal(map[string]string{"action": action})
	resp, err := a.boxDoTimeout(host, port, http.MethodPost, "/api/spotify/playmode", "application/json", string(body), spotifyPlayModeTimeout)
	if err != nil {
		a.logger.Info("spotify play mode: request failed", "host", host, "action", action, "err", err)
		return nil, err
	}
	defer resp.Body.Close()
	// An agent older than the endpoint answers an unknown path with its phone
	// page (200, HTML), never with a 404; a 404 or 405 can still come from a
	// port that is not the agent. Either way the speaker cannot do it.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed ||
		(resp.StatusCode < 400 && !isJSONResponse(resp)) {
		a.logger.Info("spotify play mode: not available on this speaker", "host", host, "status", resp.StatusCode)
		return nil, fmt.Errorf("%s", spotifyPlayModeUnsupported)
	}
	if resp.StatusCode >= 400 && !isJSONResponse(resp) {
		return nil, readHTTPError(resp)
	}
	if resp.StatusCode >= 400 {
		var refusal struct {
			Error  string `json:"error"`
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&refusal)
		a.logger.Info("spotify play mode: refused", "host", host, "action", action, "status", resp.StatusCode, "reason", refusal.Reason)
		if refusal.Error == "" {
			refusal.Error = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("%s", refusal.Error)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	a.logger.Info("spotify play mode: stepped", "host", host, "action", action, "shuffle", out["shuffle"], "repeat", out["repeat"])
	return out, nil
}

// isJSONResponse says whether the answer declares itself JSON.
func isJSONResponse(resp *http.Response) bool {
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}
