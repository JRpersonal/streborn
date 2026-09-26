// favorites.go: the starred stations, kept on the speaker so every device
// sees the same list.
//
// Asked for on 2026-09-26: "where do I see on my phone the stations I starred
// on the computer?" The honest answer was nowhere. The stars lived in the
// desktop app's own browser storage on that one PC, and the phone page, which
// only ever talks to the speaker, could not know about them.
//
// So they move to where every device can reach them. The speaker is the only
// thing in the picture that is always on and always reachable, and it already
// holds the six keys for exactly that reason.
//
// Bounded on purpose. This is NAND on hardware that is meant to last, so the
// list has a hard cap and is only ever written when it actually changed.

package webui

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

// favoritesPath is the NAND file. var so a test can point it at a temp tree.
var favoritesPath = "/mnt/nv/streborn/favorites.json"

// Limits. A starred-station list is a human-sized thing; these bounds exist so
// a broken client cannot grow a file on the speaker's flash without limit.
const (
	maxFavorites    = 300
	maxFavoriteJSON = 256 << 10 // 256 KB
)

// Favorite is one starred station, trimmed to what a list row needs. The
// desktop app and the phone page render from the same fields.
type Favorite struct {
	UUID    string `json:"stationuuid,omitempty"`
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	Res     string `json:"url_resolved,omitempty"`
	Codec   string `json:"codec,omitempty"`
	Bitrate int    `json:"bitrate,omitempty"`
	Favicon string `json:"favicon,omitempty"`
	Country string `json:"country,omitempty"`
	Home    string `json:"homepage,omitempty"`
}

// playableURL is the address a row actually plays: the resolved one when the
// directory gave us one, else the listed one.
func (f Favorite) playableURL() string {
	if s := strings.TrimSpace(f.Res); s != "" {
		return s
	}
	return strings.TrimSpace(f.URL)
}

// loadFavorites reads the list, or an empty list when there is none. A damaged
// file reads as empty rather than as an error: the stars are a convenience and
// must never stop the page from loading.
func loadFavorites() []Favorite {
	b, err := os.ReadFile(favoritesPath)
	if err != nil {
		return []Favorite{}
	}
	var out []Favorite
	if err := json.Unmarshal(b, &out); err != nil || out == nil {
		return []Favorite{}
	}
	return out
}

// sanitiseFavorites drops entries nothing could play and enforces the cap.
func sanitiseFavorites(in []Favorite) []Favorite {
	out := make([]Favorite, 0, len(in))
	seen := map[string]bool{}
	for _, f := range in {
		f.Name = strings.TrimSpace(f.Name)
		if f.Name == "" || f.playableURL() == "" {
			continue // a row with no name or no stream is not a station
		}
		key := f.UUID
		if key == "" {
			key = f.Name + "|" + f.playableURL()
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
		if len(out) >= maxFavorites {
			break
		}
	}
	return out
}

// handleFavorites serves and stores the starred stations.
//
// GET  -> [] (never null: the phone page renders a list, not a maybe)
// PUT  [ ... ] replaces the list, from the desktop app.
func (s *Server) handleFavorites(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, loadFavorites())
	case http.MethodPut:
		if !isLocalLAN(r.RemoteAddr) {
			http.Error(w, "only allowed from LAN", http.StatusForbidden)
			return
		}
		var in []Favorite
		if !decodeJSONRequest(w, r, maxFavoriteJSON, &in) {
			return
		}
		clean := sanitiseFavorites(in)
		b, err := json.Marshal(clean)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Only when it actually changed. This is flash on hardware that has to
		// last, and the app re-pushes the list on every discovery cycle, so an
		// unconditional write would be a steady drip of NAND wear for nothing.
		if old, err := os.ReadFile(favoritesPath); err == nil && string(old) == string(b) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(clean), "written": false})
			return
		}
		if err := writeNANDFile(favoritesPath, b); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.logger.Info("favorites stored on the speaker", "count", len(clean), "dropped", len(in)-len(clean))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(clean), "written": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
