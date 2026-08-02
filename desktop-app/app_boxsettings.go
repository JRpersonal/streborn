package main

// This file was split out of app.go (wave-1 move-only refactor):
// box settings via the stick agent: name, volume, bass, source, and webhooks.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// BoxSettings fetches name/volume/bass/network/sources of the box via the stick.
func (a *App) BoxSettings(host string, port int) (map[string]any, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/settings", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetBoxName changes the display name of the Bose box.
func (a *App) SetBoxName(host string, port int, name string) error {
	return a.boxPut(host, port, "/api/box/name", map[string]string{"name": name})
}

// SetBoxVolume sets the volume (0-100).
func (a *App) SetBoxVolume(host string, port int, value int) error {
	return a.boxPut(host, port, "/api/box/volume", map[string]int{"value": value})
}

// SetBoxBass sets the bass value (range per box, ST10 e.g. -9..0).
func (a *App) SetBoxBass(host string, port int, value int) error {
	return a.boxPut(host, port, "/api/box/bass", map[string]int{"value": value})
}

// SelectBoxSource switches the box to a different source: "AUX", "LOCAL",
// "BLUETOOTH", "STANDBY". The Stick Agent translates that into the matching
// /select or /key call to the Bose REST API.
//
// AUX and LOCAL are the same analogue input under the two names the firmware
// uses for it, and they are NOT interchangeable on the wire: the caller passes
// whichever one the speaker itself reports (#491).
func (a *App) SelectBoxSource(host string, port int, source string) error {
	return a.boxPut(host, port, "/api/box/source", map[string]string{"source": source})
}

// readHTTPError turns a failed box response into an error carrying the status
// code and a bounded slice of the body. One canonical place for the read limit
// and message format, used at every status>=400 / non-200 site.
func readHTTPError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
}

func (a *App) boxPut(host string, port int, path string, body any) error {
	// Routed through boxDo so the small settings PUTs (volume, bass,
	// name, source, wlan) get the same transparent :8888<->:17008 port
	// fallback as every other agent call: if the box record carries the
	// wrong/stale port, the first attempt fails fast (connection refused)
	// and the alternate is tried and cached, instead of the PUT erroring
	// out on a dead port.
	b, _ := json.Marshal(body)
	resp, err := a.boxDo(host, port, http.MethodPut, path, "application/json", string(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	return nil
}

// --- Webhook config (remote thumbs key -> a user-defined HTTP request) ----
//
// The remote's thumbs-up and thumbs-down keys surface on the box only as a
// generic activity ping with no up/down identity, so they share ONE trigger
// (suited to a smart-home on/off toggle). These call STR's /api/webhooks
// endpoints on the agent.

// GetWebhooks reads the agent's webhook config (shape: {"thumb":{...}}).
func (a *App) GetWebhooks(host string, port int) (map[string]any, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/webhooks", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetWebhooks stores the thumbs-trigger HTTP request on the agent.
func (a *App) SetWebhooks(host string, port int, enabled bool, method, url, body, contentType string) error {
	cfg := map[string]any{
		"thumb": map[string]any{
			"enabled":      enabled,
			"method":       method,
			"url":          url,
			"body":         body,
			"content_type": contentType,
		},
	}
	return a.boxPut(host, port, "/api/webhooks", cfg)
}

// SaveWebhookConfig replaces the agent's FULL webhook config (thumb + the
// per-remote-key buttons preset1..preset6, aux, power). The PUT replaces the
// whole config on the agent, so the frontend sends the complete object it built
// from GetWebhooks; saving only one field would wipe the others.
func (a *App) SaveWebhookConfig(host string, port int, cfg map[string]any) error {
	return a.boxPut(host, port, "/api/webhooks", cfg)
}

// TestWebhook fires the given request immediately so the user can verify their
// URL from the app without pressing a key on the box. Returns {ok, status}.
func (a *App) TestWebhook(host string, port int, method, url, body, contentType string) (map[string]any, error) {
	action := map[string]any{
		"enabled":      true,
		"method":       method,
		"url":          url,
		"body":         body,
		"content_type": contentType,
	}
	b, _ := json.Marshal(action)
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/webhooks/test", "application/json", string(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readHTTPError(resp)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// TestWebhookAction fires an arbitrary configured action (http/udp/wol) once for
// the test button, without pressing a key on the box. actionJSON is the full
// webhook Action the frontend built (type + its fields), so the UDP/WoL test
// works the same as the HTTP one (#187). Returns {ok, status}.
func (a *App) TestWebhookAction(host string, port int, actionJSON string) (map[string]any, error) {
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/webhooks/test", "application/json", actionJSON)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readHTTPError(resp)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
