package main

// This file was split out of app.go (wave-1 move-only refactor):
// box-native (:8090) Bose API calls that STR does not proxy.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// --- Box-native (:8090) controls that STR does not proxy ---------------
//
// Clock display and language live on the box's OWN Bose HTTP API (:8090),
// not STR's REST API. They must be driven server-side from here: the
// box's :8090 sends no CORS headers, so the previous frontend
// fetch(boseUrl('/clockDisplay'|'/language')) with a text/xml POST
// triggered a CORS preflight the box never answered and failed with
// "TypeError: Failed to fetch". :8090 is a Bose-owned port and stays
// externally reachable even on Series-I/BCO boxes where STR's :8888 is
// firewalled (verified live 2026-06-01), so a direct server-side call
// works on every model. (WLAN + presets etc. already go through STR's
// CORS-enabled :8888/:17008 API, so only these two needed moving.)
func (a *App) boseURL(host string) string { return fmt.Sprintf("http://%s:8090", host) }

func (a *App) boseGet(host, path string) (string, error) {
	ctx, cancel := context.WithTimeout(a.appCtx(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.boseURL(host)+path, nil)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return string(b), nil
}

func (a *App) bosePostXML(host, path, body string) error {
	ctx, cancel := context.WithTimeout(a.appCtx(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.boseURL(host)+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	return nil
}

// xmlTagOrAttr pulls the text content of <tag ...>VALUE</tag>, or if the
// element is self-closing, the value of one of the given attributes
// (enable/enabled/value). Cheap substring scan, no encoding/xml. Returns
// "" when nothing matches (caller shows "unknown").
func xmlTagOrAttr(xml, tag string, attrs ...string) string {
	open := "<" + tag
	i := strings.Index(xml, open)
	if i < 0 {
		return ""
	}
	gt := strings.IndexByte(xml[i:], '>')
	if gt < 0 {
		return ""
	}
	head := xml[i : i+gt+1]
	// Element with text content: <tag ...>VALUE</tag>
	if !strings.HasSuffix(strings.TrimSpace(head), "/>") {
		rest := xml[i+gt+1:]
		if end := strings.Index(rest, "</"+tag+">"); end >= 0 {
			if v := strings.TrimSpace(rest[:end]); v != "" {
				return v
			}
		}
	}
	// Self-closing / attribute form: <tag enable="VALUE"/>
	for _, at := range attrs {
		key := at + "=\""
		if j := strings.Index(head, key); j >= 0 {
			r := head[j+len(key):]
			if k := strings.IndexByte(r, '"'); k >= 0 {
				return r[:k]
			}
		}
	}
	return ""
}

// GetClockDisplay reads the box clock-display state (BETA, undocumented,
// not on every model). Live-verified schema 2026-06-01 (taigan):
// GET /clockDisplay -> <clockDisplay><clockConfig userEnable="false"
// timeFormat="..." .../></clockDisplay>. The on/off state is the
// userEnable attribute of the inner <clockConfig>. Returns "true"/
// "false" or "" if absent / endpoint unsupported.
func (a *App) GetClockDisplay(host string) (string, error) {
	body, err := a.boseGet(host, "/clockDisplay")
	if err != nil {
		return "", err
	}
	return xmlTagOrAttr(body, "clockConfig", "userEnable"), nil
}

// SetClockDisplay toggles the box clock display and sets the local-time
// offset + 12/24h format. The box rejects a bare <clockConfig .../>
// (HTTP 400 CLIENT_XML_ERROR); it requires the full
// <clockDisplay><clockConfig .../></clockDisplay> wrapper (live-verified
// 2026-06-01). The box keeps its UTC time from NTP but shows it raw
// (timezoneInfo stays NOT_SET); userOffsetMinute is the minutes EAST of
// UTC to add, so passing the desktop's current offset makes the speaker
// display local time. timeFormat picks 12h vs 24h. offsetMinutes is
// ignored by the box when userEnable is false but we always send a
// consistent config.
func (a *App) SetClockDisplay(host string, enable bool, timezone string, offsetMinutes int, format24 bool) error {
	tf := "TIME_FORMAT_12HOUR_ID"
	if format24 {
		tf = "TIME_FORMAT_24HOUR_ID"
	}
	// timezoneInfo is the real IANA zone (e.g. "Europe/Berlin"), the same
	// thing the Bose iOS app sets (live-verified 2026-06-01); with it the
	// speaker handles DST itself from its own tz database. We also send
	// the current userOffsetMinute as a correct-now fallback. timezone ""
	// leaves it unset.
	tz := timezone
	off := offsetMinutes
	if tz == "" {
		tz = "NOT_SET" // no zone: fall back to the raw offset shift
	} else {
		// With a real IANA zone the box derives the offset (incl DST)
		// itself. Sending userOffsetMinute on TOP would DOUBLE-shift the
		// clock: live 2026-06-01, timezoneInfo=Europe/Berlin (+2) plus
		// userOffsetMinute=120 (+2) showed 06:00 instead of 04:00. So
		// whenever a zone is set, the offset must be 0.
		off = 0
	}
	body := fmt.Sprintf(
		`<clockDisplay><clockConfig userEnable="%t" timezoneInfo="%s" userOffsetMinute="%d" timeFormat="%s" /></clockDisplay>`,
		enable, tz, off, tf)
	return a.bosePostXML(host, "/clockDisplay", body)
}

// GetClockFormat24 reports whether the box clock is currently in 24h
// mode, so the UI can preselect the right radio. "" GET -> false (12h
// default). Separate tiny method to avoid changing GetClockDisplay's
// return shape (its string drives the on/off label).
func (a *App) GetClockFormat24(host string) (bool, error) {
	body, err := a.boseGet(host, "/clockDisplay")
	if err != nil {
		return false, err
	}
	return strings.Contains(xmlTagOrAttr(body, "clockConfig", "timeFormat"), "24HOUR"), nil
}

// GetBoxLanguage reads the box sysLanguage integer (as a string), or "".
func (a *App) GetBoxLanguage(host string) (string, error) {
	body, err := a.boseGet(host, "/language")
	if err != nil {
		return "", err
	}
	return xmlTagOrAttr(body, "sysLanguage"), nil
}

// SetBoxLanguage sets the box sysLanguage integer (see project_bose_language_enum).
func (a *App) SetBoxLanguage(host string, value int) error {
	return a.bosePostXML(host, "/language", fmt.Sprintf(`<sysLanguage>%d</sysLanguage>`, value))
}

// GetAirplayOpt reads the BCO "AirPlay optimization" toggle from the STR
// agent on host:port. Returns {"supported":bool,"enabled":bool}. Only
// BCO speakers (Portable, ST20-spotty) support it; others report
// supported=false. See internal/webui handleBoxAirplayOpt.
func (a *App) GetAirplayOpt(host string, port int) (map[string]bool, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/airplay-opt", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out map[string]bool
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetAirplayOpt flips the AirPlay-optimization toggle. The agent
// rewrites BCOResetTimerEnabled and reboots the speaker to apply it
// (BoseApp reads the value at boot, like the iOS app), so the box drops
// off the LAN for ~60-120s after this returns.
func (a *App) SetAirplayOpt(host string, port int, enabled bool) error {
	body, _ := json.Marshal(map[string]bool{"enabled": enabled})
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/airplay-opt", "application/json", string(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// GetResumeOnPowerOn reads the per-box "resume the last station on power-on"
// toggle from the STR agent. Returns {"supported":bool,"enabled":bool}; default
// is enabled. Routed through boxDo so it self-heals across :8888 / :17008 like
// the other box calls (a BCO speaker reachable only on :17008 still answers).
// See internal/webui handleResumeOnPowerOn.
func (a *App) GetResumeOnPowerOn(host string, port int) (map[string]bool, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/resume-on-power-on", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out map[string]bool
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetResumeOnPowerOn flips the power-on resume toggle on the box. The agent
// persists it to NAND and applies it live (no reboot needed): the next real
// power-on either resumes the last station or stays silent.
func (a *App) SetResumeOnPowerOn(host string, port int, enabled bool) error {
	body, _ := json.Marshal(map[string]bool{"enabled": enabled})
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/resume-on-power-on", "application/json", string(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// GetDisplayTrack reads the per-box "show the live radio track on the speaker
// display" opt-in (default off). Returns {supported, enabled, mode} where mode is
// "both" | "title" | "artist".
func (a *App) GetDisplayTrack(host string, port int) (map[string]any, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/display-track", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetDisplayTrack toggles the per-box "show the live radio track on the speaker
// display" opt-in and sets what it shows (mode: "both" | "title" | "artist").
// Enabling it makes the box re-buffer (a brief audio dropout) on each text change.
func (a *App) SetDisplayTrack(host string, port int, enabled bool, mode string) error {
	body, _ := json.Marshal(map[string]any{"enabled": enabled, "mode": mode})
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/display-track", "application/json", string(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// BoxPresetInfo is one of the box's OWN presets (incl. foreign sources like
// Deezer that STR did not set), from GET /api/box/presets.
type BoxPresetInfo struct {
	Slot          int    `json:"slot"`
	Source        string `json:"source"`
	Type          string `json:"type"`
	Location      string `json:"location"`
	SourceAccount string `json:"sourceAccount"`
	Name          string `json:"name"`
}

// BoxPresets reads the box's own preset list (incl. foreign sources). Empty until
// the box has reported a presetsUpdated frame at least once since the agent start.
func (a *App) BoxPresets(host string, port int) ([]BoxPresetInfo, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/presets", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out []BoxPresetInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// BoxSnapshot returns the agent's pre-takeover snapshot of the box's presets +
// sources. Used to warn the user about account-linked cloud sources (Deezer,
// ...) that STR cannot carry over yet, and to show what was there. The shape is
// {captured:bool, lostServices:[], lostPresets:[], presets:[], sources:[]};
// returns {captured:false} when nothing was captured.
func (a *App) BoxSnapshot(host string, port int) (map[string]any, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/snapshot", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// RestoreBoxSnapshot (EXPERIMENTAL) asks the agent to write account-linked cloud
// presets (e.g. Deezer) back onto their original slots and re-advertise their
// sources, so the box plays them again via its cached account token. presetsXML
// is an optional box /presets dump the user saved; empty uses the agent's
// snapshot. Returns the agent's result (restored slots, services, failed,
// rebootRecommended).
func (a *App) RestoreBoxSnapshot(host string, port int, presetsXML string) (map[string]any, error) {
	body, _ := json.Marshal(map[string]string{"presetsXML": presetsXML})
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/snapshot/restore", "application/json", string(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// RecallBoxPreset plays one of the box's own presets by pressing its hardware
// preset key, so a foreign preset (Deezer) plays via the box's cached account.
func (a *App) RecallBoxPreset(host string, port int, slot int) error {
	body, _ := json.Marshal(map[string]int{"slot": slot})
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/presets/recall", "application/json", string(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
