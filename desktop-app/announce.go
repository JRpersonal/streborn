package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Audio notifications / announcements (#125): the desktop app's thin wrapper over
// the agent's POST /api/announce. The agent does the real work (fetch TTS,
// interrupt, play, resume); the app offers a quick test field plus a copy-paste
// command for power users wiring this into Home Assistant or scripts.

// AnnounceExample returns a ready-to-paste curl command that fires a test
// announcement at the given box. It uses the agent port that actually answers the
// box (classic ST10/30 reply on :8888 directly, BCO/Portable speakers only on the
// REDIRECTed :17008), so the command works as-is from a terminal or an automation.
func (a *App) AnnounceExample(host string, port int) string {
	p := port
	if cp, ok := a.cachedPort(host); ok {
		p = cp
	}
	if p == 0 {
		p = 17008
	}
	return fmt.Sprintf(`curl -X POST "http://%s:%d/api/announce" -H "Content-Type: application/json" -d "{\"text\":\"Someone is at the door\",\"volume\":20}"`, host, p)
}

// SendAnnounce fires an announcement at the box (POST /api/announce). lang is the
// TTS language code (e.g. "de", "en"); empty lets the agent default to en. A zero
// volume leaves the current volume untouched. Routed through boxDo for the
// :8888<->:17008 self-heal.
func (a *App) SendAnnounce(host string, port int, text, lang string, volume int) error {
	payload := map[string]any{"text": text}
	if lang != "" {
		payload["lang"] = lang
	}
	if volume > 0 {
		payload["volume"] = volume
	}
	body, _ := json.Marshal(payload)
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/announce", "application/json", string(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	return nil
}
