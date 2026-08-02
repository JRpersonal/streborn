package main

// This file was split out of app.go (wave-1 move-only refactor):
// persistent app flags stored in the OS user-config dir.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// --- Persistent app flags ---
//
// A few one-way app-level flags (e.g. "the user has already been invited to the
// community world map") must survive app version updates and even a reinstall, so
// a one-time prompt never reappears and becomes annoying. The frontend's
// localStorage is NOT reliable for this: a WebView2/WKWebView profile can reset
// on an update or be cleared. These flags live in a tiny JSON file in the OS
// user-config dir (Roaming AppData / ~/Library/Application Support / ~/.config),
// a stable path independent of the app version and the executable location.
var appFlagsMu sync.Mutex

func appStatePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ST Reborn", "app-state.json"), nil
}

func readAppFlags() map[string]bool {
	m := map[string]bool{}
	path, err := appStatePath()
	if err != nil {
		return m
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

// GetAppFlag reports whether a persistent one-way app flag has been set. Used by
// the frontend to gate once-ever prompts so they survive app updates, unlike
// localStorage. Unknown/unset flags return false.
func (a *App) GetAppFlag(name string) bool {
	appFlagsMu.Lock()
	defer appFlagsMu.Unlock()
	return readAppFlags()[name]
}

// SetAppFlag persists a one-way app flag (sets it true, never unset). Best-effort
// and atomic (temp file + rename); a write failure is returned but the caller
// treats it as non-fatal and still falls back to the frontend localStorage guard.
func (a *App) SetAppFlag(name string) error {
	appFlagsMu.Lock()
	defer appFlagsMu.Unlock()
	m := readAppFlags()
	if m[name] {
		return nil
	}
	m[name] = true
	path, err := appStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RescuedSpeakerCount returns how many speakers are shown on the community world
// map, i.e. the sum of the per-pin reaction counts at st-reborn.de/api/pins.php,
// which is exactly what the website's "rescued" counter displays. The world-map
// invite shows it to motivate the user to add their pin. Best-effort: returns 0
// on any error so the invite simply omits the count. Fetched server-side here to
// avoid a cross-origin fetch from the webview.
func (a *App) RescuedSpeakerCount() int {
	ctx, cancel := context.WithTimeout(a.appCtx(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://st-reborn.de/api/pins.php", nil)
	if err != nil {
		return 0
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var out struct {
		Pins []struct {
			Count int `json:"count"`
		} `json:"pins"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return 0
	}
	total := 0
	for _, p := range out.Pins {
		if p.Count > 0 {
			total += p.Count
		} else {
			total++ // a pin with no explicit count still represents one rescued box
		}
	}
	return total
}
