package main

// Music library: DLNA/UPnP media servers as a native source on the speaker.
//
// The speaker discovers media servers on the LAN by itself but will not play
// from one until that server is registered as a music account. Once it is, the
// speaker browses and plays the server on its own, and it shows up in the
// original Bose app too. STR only turns the registration on and keeps it on;
// the agent holds the memory of it, because the speaker forgets it on reboot.
//
// This is deliberately thin. All three calls are the agent's endpoint with the
// port self-healing every other box call gets.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// BoxMediaServer is one media server as the speaker sees it. Distinct from the
// Library tab's LibraryServer, which is what the DESKTOP discovered for its own
// browsing: this one is about what the SPEAKER plays by itself.
type BoxMediaServer struct {
	ID           string `json:"id"`
	IP           string `json:"ip"`
	Manufacturer string `json:"manufacturer"`
	ModelName    string `json:"modelName"`
	FriendlyName string `json:"friendlyName"`
	// Registered is what the speaker reports RIGHT NOW; Enabled is what the user
	// asked for. They differ for a while after enabling, and after a reboot,
	// because the speaker confirms the account with STR before the source
	// appears. The UI shows Enabled and explains the wait.
	Registered bool   `json:"registered"`
	Enabled    bool   `json:"enabled"`
	Status     string `json:"status"`
}

// mediaServerCallTimeout is generous on purpose: /listMediaServers comes out of
// the firmware's own discovery cache, and a speaker that just woke can take
// several seconds to produce it.
const mediaServerCallTimeout = 25 * time.Second

// ListBoxMediaServers returns the media servers this speaker can see, each marked
// with whether it is enabled as a music source.
func (a *App) ListBoxMediaServers(host string, port int) ([]BoxMediaServer, error) {
	resp, err := a.boxDoTimeout(host, port, http.MethodGet, "/api/box/mediaservers", "", "", mediaServerCallTimeout)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out struct {
		Servers []BoxMediaServer `json:"servers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Servers, nil
}

// EnableBoxMediaServer registers a media server as a music source on the speaker.
//
// It returns once the speaker has ACCEPTED the registration, which is not the
// same as the source being usable: the speaker then confirms the account with
// STR's marge, and measured on real hardware that took minutes. Callers must
// present this as "on its way", never as "ready now".
func (a *App) EnableBoxMediaServer(host string, port int, id, name string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("no media server selected")
	}
	body, err := json.Marshal(map[string]string{"id": id, "name": name})
	if err != nil {
		return err
	}
	resp, err := a.boxDoTimeout(host, port, http.MethodPost, "/api/box/mediaservers",
		"application/json", string(body), mediaServerCallTimeout)
	if err != nil {
		a.logger.Info("media server: enable failed", "host", host, "id", id, "err", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		herr := readHTTPError(resp)
		a.logger.Info("media server: the speaker refused the registration", "host", host, "id", id, "err", herr)
		return herr
	}
	a.logger.Info("media server: enabled as a music source", "host", host, "id", id, "name", name)
	return nil
}

// DisableBoxMediaServer removes the media server as a music source again.
func (a *App) DisableBoxMediaServer(host string, port int, id, name string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("no media server selected")
	}
	path := "/api/box/mediaservers?id=" + url.QueryEscape(id) + "&name=" + url.QueryEscape(name)
	resp, err := a.boxDoTimeout(host, port, http.MethodDelete, path, "", "", mediaServerCallTimeout)
	if err != nil {
		a.logger.Info("media server: disable failed", "host", host, "id", id, "err", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		herr := readHTTPError(resp)
		a.logger.Info("media server: the speaker refused the removal", "host", host, "id", id, "err", herr)
		return herr
	}
	a.logger.Info("media server: removed as a music source", "host", host, "id", id)
	return nil
}
