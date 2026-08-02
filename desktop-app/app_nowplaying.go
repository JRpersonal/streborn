package main

// This file was split out of app.go (wave-1 move-only refactor):
// now-playing readouts: stream bitrate/title, track position, and Spotify now playing.

import (
	"encoding/json"
	"net/http"
)

// StreamBitrate returns the agent's currently-detected stream bitrate in
// kbit/s (icy-br, or a throughput sample), or 0 if none/unavailable.
// Routed through boxDo so it self-heals across :8888 / :17008 like every
// other box call. The frontend previously did a raw fetch pinned to
// box.port, which silently failed on BCO speakers (Portable, ST20-spotty)
// reachable only on :17008, so the live bitrate never showed there.
func (a *App) StreamBitrate(host string, port int) int {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/stream/bitrate", "", "")
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var out struct {
		Bitrate int `json:"bitrate"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0
	}
	return out.Bitrate
}

// TrackPosition returns where the speaker is inside the current track, in
// seconds, plus the track length. A length of 0 means "no end", which is what
// radio reports and is a normal answer, not a failure: the UI then shows the
// elapsed time without a bar (#399). Both are -1 when the speaker could not be
// asked at all, so the caller can leave the previous reading alone instead of
// snapping the bar back to zero on one missed poll.
//
// Routed through boxDo like the other agent reads, so it self-heals across
// :8888 and :17008.
func (a *App) TrackPosition(host string, port int) (int, int) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/position", "", "")
	if err != nil {
		return -1, -1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return -1, -1
	}
	var out struct {
		OK       bool `json:"ok"`
		Position int  `json:"positionSec"`
		Duration int  `json:"durationSec"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.OK {
		return -1, -1
	}
	return out.Position, out.Duration
}

// SpotifyBitrate returns the bitrate the agent measured from the live
// go-librespot Ogg stream in kbit/s, or 0 if Spotify is idle/unavailable.
// Spotify presets carry no radio-browser bitrate, so the tile reads the
// real measured stream rate here instead of a hardcoded nominal. Routed
// through boxDo so it self-heals across :8888 / :17008 like StreamBitrate.
func (a *App) SpotifyBitrate(host string, port int) int {
	resp, err := a.boxDo(host, port, http.MethodGet, "/spotify/info", "", "")
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var out struct {
		Bitrate int `json:"bitrate"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0
	}
	return out.Bitrate
}

// StreamTitle returns the live ICY StreamTitle the agent parsed from the radio
// stream currently proxied, or "" when the station sends no metadata. The app
// shows it next to the station name as the now-playing track. Routed through
// boxDo so it self-heals across :8888 / :17008 like StreamBitrate.
func (a *App) StreamTitle(host string, port int) string {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/stream/title", "", "")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var out struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ""
	}
	return out.Title
}

// SpotifyNowPlaying returns the live Spotify state for the UI: measured
// bitrate plus the current track title, artist and cover URL (from
// go-librespot's events). Empty fields when nothing is playing. Routed
// through boxDo so it self-heals across :8888 / :17008.
type SpotifyNow struct {
	Bitrate int    `json:"bitrate"`
	Track   string `json:"track"`
	Artist  string `json:"artist"`
	Cover   string `json:"cover"`
	Context string `json:"context"` // current playlist/album URI (for a long-press save)
	Account string `json:"account"` // current go-librespot login
	// PremiumRequired is true when the box's Spotify account is free/open, which
	// cannot do the autonomous on-demand playback a preset recall needs (#45). The
	// Spotify view shows a "recall needs Premium" note when set.
	PremiumRequired bool `json:"premiumRequired"`
}

func (a *App) SpotifyNowPlaying(host string, port int) SpotifyNow {
	var out SpotifyNow
	resp, err := a.boxDo(host, port, http.MethodGet, "/spotify/info", "", "")
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}
