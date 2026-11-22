package main

// Deezer on the speaker's own Deezer client (docs/streaming/deezer.md).
//
// A new Deezer sign-in is not possible without Bose; signing out goes through
// UnlinkMusicService with service "deezer" (app_usservices.go). These read
// the state, run the card's Check again and start a Deezer link.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DeezerStatus returns the agent's GET /api/deezer/account answer: the state
// the Deezer card shows, the speaker's DEEZER line (account masked) and the
// last attempt.
func (a *App) DeezerStatus(host string, port int) (map[string]any, error) {
	resp, err := a.usServicesDo(host, port, http.MethodGet, "/api/deezer/account", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]any{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeezerRecheck is the Deezer card's Check again: the agent's POST
// /api/deezer/recheck hands a kept Deezer login back to a speaker that lists
// Deezer without a working account, then answers like DeezerStatus.
func (a *App) DeezerRecheck(host string, port int) (map[string]any, error) {
	resp, err := a.usServicesDo(host, port, http.MethodPost, "/api/deezer/recheck", "{}")
	if err != nil {
		a.logger.Info("deezer: recheck failed", "host", host, "err", err)
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]any{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&out); err != nil {
		return nil, err
	}
	a.logger.Info("deezer: recheck", "host", host, "state", out["state"], "recreate", out["recreate"])
	return out, nil
}

// PlayDeezerLink starts a Deezer playlist, album or track link on the
// speaker's own Deezer client. The answer names what started.
func (a *App) PlayDeezerLink(host string, port int, link, name string) (map[string]any, error) {
	link = strings.TrimSpace(link)
	if link == "" {
		return nil, fmt.Errorf("no Deezer link")
	}
	body, err := json.Marshal(map[string]string{"url": link, "name": strings.TrimSpace(name)})
	if err != nil {
		return nil, err
	}
	resp, err := a.usServicesDo(host, port, http.MethodPost, "/api/deezer/play", string(body))
	if err != nil {
		a.logger.Info("deezer: link play failed", "host", host, "err", err)
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]any{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return nil, err
	}
	a.logger.Info("deezer: link started", "host", host, "kind", out["kind"], "id", out["id"])
	return out, nil
}
