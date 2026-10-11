package main

// Pandora and iHeartRadio, the music services the speaker firmware plays by
// itself (docs/streaming/us-services.md).
//
// The speaker's own clients log in with the account and play the stations; STR
// only stands in for the Bose cloud's bookkeeping. These bindings are thin
// wrappers around the agent's endpoints with the usual :8888/:17008 self-heal.
// The password is passed straight through to the agent, which hands it to the
// speaker; it is never logged or kept here.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// musicServiceCallTimeout covers the firmware logging in with the service
// (account calls) and reading the station list from the service (Pandora
// stations), both of which go out to the internet from the speaker.
const musicServiceCallTimeout = 30 * time.Second

// musicServicePath maps the service name the frontend uses to the agent path.
var musicServicePath = map[string]string{
	"pandora": "/api/pandora/account",
	"iheart":  "/api/iheart/account",
}

// PandoraStation is one station of the Pandora account linked to a speaker.
type PandoraStation struct {
	Name       string `json:"name"`
	Location   string `json:"location"`
	Type       string `json:"type"`
	ItemName   string `json:"itemName,omitempty"`
	Art        string `json:"art,omitempty"`
	Presetable bool   `json:"presetable"`
}

// PandoraStationList is the station list plus the speaker's status for the
// source ("READY" when it can play).
type PandoraStationList struct {
	Status   string           `json:"status"`
	Stations []PandoraStation `json:"stations"`
}

// usServicesDo runs one agent call and turns a refusal into an error that
// carries the agent's own answer (its JSON "code" included), so the frontend
// can tell "not linked" from a firmware refusal.
func (a *App) usServicesDo(host string, port int, method, path, body string) (*http.Response, error) {
	ct := ""
	if body != "" {
		ct = "application/json"
	}
	resp, err := a.boxDoTimeout(host, port, method, path, ct, body, musicServiceCallTimeout)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, readHTTPError(resp)
	}
	return resp, nil
}

// USServicesStatus returns the agent's GET /api/us-services answer: the
// speaker's region, the stand-in's per-service state (accounts masked) and
// the speaker's own PANDORA and IHEART source lines.
func (a *App) USServicesStatus(host string, port int) (map[string]any, error) {
	resp, err := a.usServicesDo(host, port, http.MethodGet, "/api/us-services", "")
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

// SetUSServicesOptIn switches Pandora and iHeartRadio on for a speaker outside
// the US (or off again).
func (a *App) SetUSServicesOptIn(host string, port int, enabled bool) error {
	body, _ := json.Marshal(map[string]bool{"enabled": enabled})
	resp, err := a.usServicesDo(host, port, http.MethodPost, "/api/us-services", string(body))
	if err != nil {
		a.logger.Info("us-services: opt-in switch failed", "host", host, "enabled", enabled, "err", err)
		return err
	}
	resp.Body.Close()
	a.logger.Info("us-services: opt-in switched", "host", host, "enabled", enabled)
	return nil
}

// LinkMusicService hands a Pandora or iHeartRadio account to the speaker
// (service "pandora" or "iheart"). The speaker's own client logs in with it.
func (a *App) LinkMusicService(host string, port int, service, user, password string) error {
	path, ok := musicServicePath[service]
	if !ok {
		return fmt.Errorf("unknown music service %q", service)
	}
	user = strings.TrimSpace(user)
	if user == "" || password == "" {
		return fmt.Errorf("e-mail and password are required")
	}
	body, err := json.Marshal(map[string]string{"user": user, "password": password})
	if err != nil {
		return err
	}
	resp, err := a.usServicesDo(host, port, http.MethodPost, path, string(body))
	if err != nil {
		a.logger.Info("us-services: account link failed", "host", host, "service", service, "err", err)
		return err
	}
	resp.Body.Close()
	a.logger.Info("us-services: account handed to the speaker", "host", host, "service", service)
	return nil
}

// UnlinkMusicService removes the Pandora or iHeartRadio account again.
func (a *App) UnlinkMusicService(host string, port int, service string) error {
	path, ok := musicServicePath[service]
	if !ok {
		return fmt.Errorf("unknown music service %q", service)
	}
	resp, err := a.usServicesDo(host, port, http.MethodDelete, path, "")
	if err != nil {
		a.logger.Info("us-services: account removal failed", "host", host, "service", service, "err", err)
		return err
	}
	resp.Body.Close()
	a.logger.Info("us-services: account removed", "host", host, "service", service)
	return nil
}

// PandoraStations lists the stations of the Pandora account linked to the
// speaker.
func (a *App) PandoraStations(host string, port int) (PandoraStationList, error) {
	resp, err := a.usServicesDo(host, port, http.MethodGet, "/api/pandora/stations", "")
	if err != nil {
		a.logger.Info("pandora: station list failed", "host", host, "err", err)
		return PandoraStationList{}, err
	}
	defer resp.Body.Close()
	var out PandoraStationList
	if err := json.NewDecoder(io.LimitReader(resp.Body, 512<<10)).Decode(&out); err != nil {
		return PandoraStationList{}, err
	}
	if out.Stations == nil {
		out.Stations = []PandoraStation{}
	}
	return out, nil
}

// PlayPandoraStation starts one station of the list on the speaker.
func (a *App) PlayPandoraStation(host string, port int, location, name, typ string) error {
	if strings.TrimSpace(location) == "" {
		return fmt.Errorf("no station selected")
	}
	body, err := json.Marshal(map[string]string{"location": location, "name": name, "type": typ})
	if err != nil {
		return err
	}
	resp, err := a.usServicesDo(host, port, http.MethodPost, "/api/pandora/play", string(body))
	if err != nil {
		a.logger.Info("pandora: play failed", "host", host, "location", location, "err", err)
		return err
	}
	resp.Body.Close()
	a.logger.Info("pandora: station started", "host", host, "location", location, "name", name)
	return nil
}
