// apiclient.go: thin HTTP helpers for go-librespot's local API.

package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// apiGet fetches a go-librespot API path (e.g. /status) and returns the body.
func (m *Manager) apiGet(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+m.apiAddr+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// jsonString quotes a string as a JSON value.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (m *Manager) apiPost(ctx context.Context, path string, body string) error {
	return m.apiPostC(ctx, m.client, path, body)
}

// apiPostQuiet posts like apiPost but does NOT stamp the own-command echo
// window (noteOwnPlayerCmd). Only for commands whose echoes cannot be misread
// as user transport intent. ServeOgg's attach-resume uses it: its
// /player/resume echoes only as playing/active, which the intent classifier
// never filters anyway, yet the stamp opened a 15 s window in which a REAL
// pause pressed in the Spotify app right after the box attached was dropped as
// STR's own echo, so the pause never armed the stop latch and the loop started
// over (Klaus, 2026-08). Recall staging must keep using apiPost: its
// pause/play echoes are exactly what the window exists to excuse.
func (m *Manager) apiPostQuiet(ctx context.Context, path string, body string) error {
	return m.apiPostRawC(ctx, m.client, path, body)
}

func (m *Manager) apiPostC(ctx context.Context, client *http.Client, path string, body string) error {
	m.noteOwnPlayerCmd(path)
	return m.apiPostRawC(ctx, client, path, body)
}

func (m *Manager) apiPostRawC(ctx context.Context, client *http.Client, path string, body string) error {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+m.apiAddr+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("go-librespot %s: %w", path, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("go-librespot %s: status %d", path, resp.StatusCode)
	}
	return nil
}
