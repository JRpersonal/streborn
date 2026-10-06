package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"
)

// The hidden games on the speaker display (internal/oled on the agent). The
// agent keeps each game's scores and its last round's final screen on the
// speaker; the app shows them once a round has been played and helps post the
// score in the announcement thread on GitHub. Nothing is sent anywhere by the
// app itself: it offers copy buttons for the screenshot and a score line, a
// save button for the screenshot, and a button that opens the thread. GitHub
// has no way to prefill a comment through a link, and its paste takes either
// an image or text, so each gets its own button.

// arcadeThreadURL is the announcement discussion with the riddle, where the
// scores of every game go.
const arcadeThreadURL = "https://github.com/JRpersonal/streborn/discussions/1173"

// arcadeScores mirrors one game in the agent's /api/box/arcade answer.
type arcadeScores struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Best     int    `json:"best"`
	Last     int    `json:"last"`
	LastRows int    `json:"lastRows"`
	Rounds   int    `json:"rounds"`
}

func (a *App) boxGetJSON(host string, port int, path string, out any) (int, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, path, "", "")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("status %d", resp.StatusCode)
	}
	return resp.StatusCode, json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out)
}

// arcadeList reads every game's scores. An agent from before the arcade
// endpoint (v1.0.5) only knows Blockfall; its answer is used as that game.
// legacy reports that case, where the screenshot lives at the old path.
func (a *App) arcadeList(host string, port int) (games []arcadeScores, legacy bool, err error) {
	var out struct {
		Games []arcadeScores `json:"games"`
	}
	code, err := a.boxGetJSON(host, port, "/api/box/arcade", &out)
	if err == nil {
		return out.Games, false, nil
	}
	if code != http.StatusNotFound {
		return nil, false, err
	}
	bf := arcadeScores{ID: "blockfall", Title: "BLOCKFALL"}
	code, err = a.boxGetJSON(host, port, "/api/box/blockfall", &bf)
	if code == http.StatusNotFound {
		return nil, true, nil // an agent without any game
	}
	if err != nil {
		return nil, true, err
	}
	return []arcadeScores{bf}, true, nil
}

func (a *App) arcadeScreenshot(host string, port int, id string, legacy bool) ([]byte, error) {
	path := "/api/box/arcade/screenshot.png?game=" + url.QueryEscape(id)
	if legacy {
		path = "/api/box/blockfall/screenshot.png"
	}
	resp, err := a.boxDo(host, port, http.MethodGet, path, "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// GetArcade reads the speaker's game scores for the settings view: one entry
// per game that was played at least once, {id, title, rounds, best, last,
// lastRows, screenshot}. Games nobody has played yet are left out, so the
// app never names a game before somebody found it. screenshot is the last
// round's final screen as a data URL, empty when the speaker has none.
func (a *App) GetArcade(host string, port int) ([]map[string]any, error) {
	games, legacy, err := a.arcadeList(host, port)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, g := range games {
		if g.Rounds <= 0 {
			continue
		}
		e := map[string]any{
			"id": g.ID, "title": g.Title, "rounds": g.Rounds,
			"best": g.Best, "last": g.Last, "lastRows": g.LastRows,
		}
		if png, perr := a.arcadeScreenshot(host, port, g.ID, legacy); perr == nil && len(png) > 0 {
			e["screenshot"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		}
		out = append(out, e)
	}
	return out, nil
}

// OpenArcadeThread opens the announcement thread in the browser.
func (a *App) OpenArcadeThread() {
	wailsrt.BrowserOpenURL(a.appCtx(), arcadeThreadURL)
}

// SaveArcadeScreenshot saves game id's last final screen where the user
// picks in a save dialog (Downloads preselected). Also the fallback where the
// webview cannot put an image on the clipboard. Returns the path, or "" when
// the user cancelled.
func (a *App) SaveArcadeScreenshot(host string, port int, id string) (string, error) {
	games, legacy, err := a.arcadeList(host, port)
	if err != nil {
		return "", err
	}
	var g *arcadeScores
	for i := range games {
		if games[i].ID == id {
			g = &games[i]
		}
	}
	if g == nil {
		return "", fmt.Errorf("no such game on this speaker")
	}
	png, err := a.arcadeScreenshot(host, port, id, legacy)
	if err != nil {
		return "", err
	}
	if len(png) == 0 {
		return "", fmt.Errorf("no screenshot of this game on this speaker")
	}
	path, err := wailsrt.SaveFileDialog(a.appCtx(), wailsrt.SaveDialogOptions{
		DefaultDirectory: arcadeSaveDir(),
		DefaultFilename:  fmt.Sprintf("%s-%d.png", g.ID, g.Last),
		Title:            "Save screenshot",
		Filters:          []wailsrt.FileFilter{{DisplayName: "PNG image (*.png)", Pattern: "*.png"}},
	})
	if err != nil || path == "" {
		return "", err
	}
	if !strings.EqualFold(filepath.Ext(path), ".png") {
		path += ".png"
	}
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// arcadeSaveDir is the user's Downloads folder, or the temp folder where
// there is none.
func arcadeSaveDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		d := filepath.Join(home, "Downloads")
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return os.TempDir()
}
