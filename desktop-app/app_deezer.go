package main

// Deezer on the speaker's own Deezer client (docs/streaming/deezer.md).
//
// A new Deezer sign-in is not possible without Bose; signing out goes through
// UnlinkMusicService with service "deezer" (app_usservices.go). These read
// the state, run the card's Check again, read the home's Deezer logins, copy
// one on the owner's press, put an older one back, and start a Deezer link.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// deezerCopyTimeout bounds the owner's Copy: the agent reads each target's
// summary and posts to it, up to a minute for a copy to all.
const deezerCopyTimeout = 75 * time.Second

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

// DeezerHousehold returns the agent's GET /api/deezer/household answer: the
// Deezer area's overview of every STR speaker in the home, its Deezer state
// and its kept login's origin, never a login or an account.
func (a *App) DeezerHousehold(host string, port int) (map[string]any, error) {
	resp, err := a.usServicesDo(host, port, http.MethodGet, "/api/deezer/household", "")
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

// DeezerCopyLogin is the Deezer area's Copy: the agent on host sends its kept
// Deezer login, sealed, to the speaker whose overview row id is target, or
// with target "" to every speaker that needs it. The answer names each
// target's outcome.
func (a *App) DeezerCopyLogin(host string, port int, target string) (map[string]any, error) {
	payload := map[string]any{"all": true}
	if t := strings.TrimSpace(target); t != "" {
		payload = map[string]any{"target": t}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := a.boxDoTimeout(host, port, http.MethodPost, "/api/deezer/copy", "application/json", string(body), deezerCopyTimeout)
	if err == nil && resp.StatusCode >= 400 {
		err = readHTTPError(resp)
		resp.Body.Close()
	}
	if err != nil {
		a.logger.Info("deezer: copy failed", "host", host, "all", target == "", "err", err)
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]any{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return nil, err
	}
	a.logger.Info("deezer: copy", "host", host, "all", target == "", "copied", out["copied"])
	return out, nil
}

// DeezerUseOlderLogin is the Deezer area's "Use the older login": the agent
// puts a backed-up login back while Deezer refuses the current one, then
// answers like DeezerStatus with the recreate's step.
func (a *App) DeezerUseOlderLogin(host string, port int) (map[string]any, error) {
	resp, err := a.usServicesDo(host, port, http.MethodPost, "/api/deezer/use-older", "{}")
	if err != nil {
		a.logger.Info("deezer: use older login failed", "host", host, "err", err)
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]any{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&out); err != nil {
		return nil, err
	}
	a.logger.Info("deezer: use older login", "host", host, "state", out["state"], "recreate", out["recreate"])
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
