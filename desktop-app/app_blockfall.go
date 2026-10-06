package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Blockfall, the hidden game on the Portable's display (internal/oled on the
// agent). The agent keeps the scores and the last round's final screen on the
// speaker; the app shows them once a round has been played and helps post the
// score in the Blockfall announcement thread on GitHub. Nothing is sent
// anywhere by the app itself: it offers copy buttons for the screenshot and a
// score line, a save button for the screenshot, and a button that opens the
// thread. GitHub has no way to
// prefill a comment through a link, and its paste takes either an image or
// text, so each gets its own button.

// blockfallThreadURL is the announcement discussion with the riddle.
const blockfallThreadURL = "https://github.com/JRpersonal/streborn/discussions/1173"

// blockfallScores mirrors the agent's /api/box/blockfall answer.
type blockfallScores struct {
	Best     int    `json:"best"`
	BestAt   string `json:"bestAt,omitempty"`
	Last     int    `json:"last"`
	LastRows int    `json:"lastRows"`
	LastAt   string `json:"lastAt,omitempty"`
	Rounds   int    `json:"rounds"`
}

func (a *App) blockfallScores(host string, port int) (blockfallScores, error) {
	var sc blockfallScores
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/blockfall", "", "")
	if err != nil {
		return sc, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return sc, nil // an agent without the game
	}
	if resp.StatusCode != http.StatusOK {
		return sc, fmt.Errorf("status %d", resp.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&sc)
	return sc, err
}

func (a *App) blockfallScreenshot(host string, port int) ([]byte, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/blockfall/screenshot.png", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// GetBlockfall reads the speaker's Blockfall scores for the settings view:
// {rounds, best, last, lastRows, screenshot}. rounds is 0 on a speaker where
// nobody has played yet (or whose agent has no game), and the app keeps the
// section hidden then. screenshot is the last round's final screen as a data
// URL, empty when the speaker has none.
func (a *App) GetBlockfall(host string, port int) (map[string]any, error) {
	sc, err := a.blockfallScores(host, port)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"rounds":   sc.Rounds,
		"best":     sc.Best,
		"last":     sc.Last,
		"lastRows": sc.LastRows,
	}
	if sc.Rounds > 0 {
		if png, perr := a.blockfallScreenshot(host, port); perr == nil && len(png) > 0 {
			out["screenshot"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		}
	}
	return out, nil
}

// OpenBlockfallThread opens the announcement thread in the browser.
func (a *App) OpenBlockfallThread() {
	wailsrt.BrowserOpenURL(a.appCtx(), blockfallThreadURL)
}

// SaveBlockfallScreenshot saves the last round's screenshot where the user
// picks in a save dialog (Downloads preselected). Also the fallback where the
// webview cannot put an image on the clipboard. Returns the path, or "" when
// the user cancelled.
func (a *App) SaveBlockfallScreenshot(host string, port int) (string, error) {
	sc, err := a.blockfallScores(host, port)
	if err != nil {
		return "", err
	}
	png, err := a.blockfallScreenshot(host, port)
	if err != nil {
		return "", err
	}
	if len(png) == 0 {
		return "", fmt.Errorf("no Blockfall screenshot on this speaker")
	}
	path, err := wailsrt.SaveFileDialog(a.appCtx(), wailsrt.SaveDialogOptions{
		DefaultDirectory: blockfallSaveDir(),
		DefaultFilename:  fmt.Sprintf("blockfall-%d.png", sc.Last),
		Title:            "Save Blockfall screenshot",
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

// blockfallSaveDir is the user's Downloads folder, or the temp folder where
// there is none.
func blockfallSaveDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		d := filepath.Join(home, "Downloads")
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return os.TempDir()
}
