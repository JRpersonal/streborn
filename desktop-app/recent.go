package main

import (
	"encoding/json"
	"net/http"
)

// RecentItem is one entry of a box's recently-played ring (#135), mirroring the
// agent's recent.Entry JSON. The desktop app is a separate Go module and cannot
// import the agent's internal/recent package, so the shape is redeclared here
// (same as radiobrowser.Station etc.). The frontend reads every box's ring,
// merges by time, and groups consecutive same-CardKey rows into source cards.
type RecentItem struct {
	TS       string `json:"ts"`       // RFC3339 when this track/play started
	Source   string `json:"source"`   // "radio" | "spotify" | "upnp"
	CardKey  string `json:"cardKey"`  // stable group key (one listening session)
	CardName string `json:"cardName"` // station / playlist / file name
	CardArt  string `json:"cardArt"`  // logo / cover URL
	CardURL  string `json:"cardURL"`  // replay target: stream URL / spotify URI / NAS location
	Track    string `json:"track"`    // song / track title (radio ICY, Spotify); may be empty
	Account  string `json:"account"`  // sourceAccount (which Spotify account)
}

// RecentPlayed reads one box's recently-played ring (GET /api/recent),
// oldest-first. Routed through boxDo for the :8888<->:17008 port self-heal that
// BCO boxes need. Best-effort: a box that is unreachable or has no ring yields an
// error the caller can ignore (the frontend just skips that box in the merge).
func (a *App) RecentPlayed(host string, port int) ([]RecentItem, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/recent", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out []RecentItem
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
