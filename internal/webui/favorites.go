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
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// favoritesPath is the NAND file. var so a test can point it at a temp tree.
var favoritesPath = "/mnt/nv/streborn/favorites.json"

// favoriteRemovalsPath remembers which favourites were removed on the phone
// page, and when (#1389). var so a test can point it at a temp tree.
var favoriteRemovalsPath = "/mnt/nv/streborn/favorites-removed.json"

// Limits. A starred-station list is a human-sized thing; these bounds exist so
// a broken client cannot grow a file on the speaker's flash without limit.
const (
	maxFavorites    = 300
	maxFavoriteJSON = 256 << 10 // 256 KB
	// maxFavoriteRemovals bounds the removal notes. The oldest go first; by
	// the time a hundred newer removals have happened, every desktop app that
	// was going to catch up with that one has long done so.
	maxFavoriteRemovals = 100
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
	// StarredAt is when the desktop app starred it (Unix ms). It tells a
	// station starred again AFTER a removal on the phone apart from an old
	// copy of the list that still carries it. Lists from before this field
	// have none, which reads as "starred long ago".
	StarredAt int64 `json:"starredAt,omitempty"`
}

// key names a favourite the way the desktop app does (favId in main.js): the
// directory's station UUID, else name and listed URL. The phone page and the
// desktop app both use this key, so all three agree on which row is meant.
func (f Favorite) key() string {
	if f.UUID != "" {
		return f.UUID
	}
	return f.Name + "|" + f.URL
}

// favoriteRemoval records one favourite removed on the phone page: its key
// and when (Unix ms, this speaker's clock).
type favoriteRemoval struct {
	ID string `json:"id"`
	At int64  `json:"at"`
}

// favoritesMu serialises the read-modify-write of the two files. A phone
// removal and a desktop push can arrive at the same moment.
var favoritesMu sync.Mutex

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

// loadFavoriteRemovals reads the removal notes; damaged or missing reads as
// none, for the same reason as the list itself.
func loadFavoriteRemovals() []favoriteRemoval {
	removalsCache.Lock()
	defer removalsCache.Unlock()
	if removalsCache.ok && removalsCache.path == favoriteRemovalsPath {
		return append([]favoriteRemoval{}, removalsCache.notes...)
	}
	out := readFavoriteRemovals()
	removalsCache.path, removalsCache.notes, removalsCache.ok = favoriteRemovalsPath, out, true
	return append([]favoriteRemoval{}, out...)
}

// removalsCache keeps the removal notes as last read or written. The desktop
// app asks for them every few seconds while its Recently-played tab is open
// (#1339), so a star removed on the phone goes out there within seconds; this
// answers that from memory instead of the flash. The agent is the only writer
// of the file, so the copy cannot go stale behind its back.
var removalsCache struct {
	sync.Mutex
	path  string
	notes []favoriteRemoval
	ok    bool
}

// setRemovalsCache records what is now on the flash; nil forgets it, so the
// next read goes back to the file.
func setRemovalsCache(notes []favoriteRemoval) {
	removalsCache.Lock()
	defer removalsCache.Unlock()
	if notes == nil {
		removalsCache.ok = false
		return
	}
	removalsCache.path, removalsCache.notes, removalsCache.ok = favoriteRemovalsPath, append([]favoriteRemoval{}, notes...), true
}

func readFavoriteRemovals() []favoriteRemoval {
	b, err := os.ReadFile(favoriteRemovalsPath)
	if err != nil {
		return []favoriteRemoval{}
	}
	var out []favoriteRemoval
	if err := json.Unmarshal(b, &out); err != nil || out == nil {
		return []favoriteRemoval{}
	}
	return out
}

// storeFavoriteRemovals writes the notes, newest kept when over the cap, and
// only when they differ from what is on the flash.
func storeFavoriteRemovals(in []favoriteRemoval) error {
	sort.SliceStable(in, func(i, j int) bool { return in[i].At > in[j].At })
	if len(in) > maxFavoriteRemovals {
		in = in[:maxFavoriteRemovals]
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(favoriteRemovalsPath); err == nil && string(old) == string(b) {
		setRemovalsCache(in)
		return nil
	}
	if len(in) == 0 {
		if _, err := os.Stat(favoriteRemovalsPath); os.IsNotExist(err) {
			setRemovalsCache(in)
			return nil // nothing noted and no file: leave the flash alone
		}
	}
	if err := writeNANDFile(favoriteRemovalsPath, b); err != nil {
		setRemovalsCache(nil)
		return err
	}
	setRemovalsCache(in)
	return nil
}

// applyFavoriteRemovals drops from an incoming list every favourite that was
// removed on the phone after it was starred, so a desktop app still holding
// the old list cannot quietly bring it back. A favourite starred again after
// the removal stays, and its note is dropped: the newer decision wins. It
// returns the kept list, the notes still in force, and how many it dropped.
func applyFavoriteRemovals(in []Favorite, removals []favoriteRemoval) ([]Favorite, []favoriteRemoval, int) {
	if len(removals) == 0 {
		return in, removals, 0
	}
	at := make(map[string]int64, len(removals))
	for _, r := range removals {
		if r.At > at[r.ID] {
			at[r.ID] = r.At
		}
	}
	restarred := map[string]bool{}
	out := make([]Favorite, 0, len(in))
	dropped := 0
	for _, f := range in {
		t, removed := at[f.key()]
		if removed && f.StarredAt <= t {
			dropped++
			continue
		}
		if removed {
			restarred[f.key()] = true
		}
		out = append(out, f)
	}
	keep := make([]favoriteRemoval, 0, len(removals))
	for _, r := range removals {
		if !restarred[r.ID] {
			keep = append(keep, r)
		}
	}
	return out, keep, dropped
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
//	GET            -> [] (never null: the phone page renders a list, not a maybe)
//	GET ?removed=1 -> {"removed":[{id,at}]}, for the desktop app to catch up
//	PUT [ ... ]    replaces the list, from the desktop app
//	DELETE ?id=KEY removes one favourite, from the phone page (#1389), and
//	               hands the removal on to the other STR speakers unless local=1
//
// Why the removal notes: the desktop app owns the list and pushes all of it to
// every speaker whenever it changes, and it never read anything back. A
// favourite removed on the phone would have come back with the next star set
// on the PC. The note makes the removal stick: a PUT drops a favourite removed
// after it was starred, and the desktop app reads the notes and takes the
// favourite off its own list too.
func (s *Server) handleFavorites(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("removed") == "1" {
			writeJSON(w, http.StatusOK, map[string]any{"removed": loadFavoriteRemovals()})
			return
		}
		writeJSON(w, http.StatusOK, loadFavorites())
	case http.MethodDelete:
		if !isLocalLAN(r.RemoteAddr) {
			http.Error(w, "only allowed from LAN", http.StatusForbidden)
			return
		}
		s.deleteFavorite(w, r)
	case http.MethodPut:
		if !isLocalLAN(r.RemoteAddr) {
			http.Error(w, "only allowed from LAN", http.StatusForbidden)
			return
		}
		var in []Favorite
		if !decodeJSONRequest(w, r, maxFavoriteJSON, &in) {
			return
		}
		favoritesMu.Lock()
		defer favoritesMu.Unlock()
		removals := loadFavoriteRemovals()
		kept, stillRemoved, held := applyFavoriteRemovals(in, removals)
		if len(stillRemoved) != len(removals) {
			if err := storeFavoriteRemovals(stillRemoved); err != nil {
				s.logger.Warn("favorites: could not update the removal notes", "err", err)
			}
		}
		if held > 0 {
			s.logger.Info("favorites: kept removed favourites out of a pushed list", "count", held)
		}
		clean := sanitiseFavorites(kept)
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

// deleteFavorite removes one favourite from this speaker, notes the removal,
// and, unless the request came from another speaker (local=1), hands it on to
// the other STR speakers, so the favourite is gone on every speaker's page.
func (s *Server) deleteFavorite(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" || len(id) > 4096 {
		http.Error(w, "specify id", http.StatusBadRequest)
		return
	}
	favoritesMu.Lock()
	list := loadFavorites()
	kept := make([]Favorite, 0, len(list))
	for _, f := range list {
		if f.key() != id {
			kept = append(kept, f)
		}
	}
	removed := len(list) - len(kept)
	var err error
	if removed > 0 {
		var b []byte
		if b, err = json.Marshal(kept); err == nil {
			err = writeNANDFile(favoritesPath, b)
		}
	}
	if err == nil {
		notes := loadFavoriteRemovals()
		out := make([]favoriteRemoval, 0, len(notes)+1)
		for _, n := range notes {
			if n.ID != id {
				out = append(out, n)
			}
		}
		out = append(out, favoriteRemoval{ID: id, At: time.Now().UnixMilli()})
		err = storeFavoriteRemovals(out)
	}
	favoritesMu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	forward := r.URL.Query().Get("local") != "1"
	s.logger.Info("favorite removed on the speaker", "removed", removed, "forward", forward)
	if forward && s.peersFn != nil {
		go s.forwardFavoriteRemoval(context.Background(), id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
}

// forwardFavoriteRemoval hands a phone removal on to every other STR speaker
// the agent knows. Best effort: a speaker that is off misses it here and gets
// the shorter list from the desktop app once that has read the note.
//
// Each peer is first recognised by its agent's JSON version answer, the way
// the phone-driven update finds them, so a Bose service sharing a port with
// the agent is never sent the DELETE. local=1 stops the peer from handing it
// on again.
func (s *Server) forwardFavoriteRemoval(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, p := range s.fleetPeerAddrs(ctx) {
		wg.Add(1)
		go func(p fleetSpeaker) {
			defer wg.Done()
			for _, port := range fleetPorts(p.Port) {
				base := fleetPeerBase(p.IP, port)
				var v struct {
					Version string `json:"version"`
				}
				if _, err := fleetGetJSON(ctx, base+"/api/agent/version", 4*time.Second, &v); err != nil || v.Version == "" {
					continue
				}
				code, err := fleetDelete(ctx, base+"/api/favorites?local=1&id="+url.QueryEscape(id), 5*time.Second)
				if err != nil || code != http.StatusOK {
					s.logger.Info("favorites: a speaker did not take the removal", "peer", p.Name, "code", code, "err", err)
				}
				return
			}
		}(p)
	}
	wg.Wait()
}

// fleetDelete sends a DELETE to a peer and reports the status code.
func fleetDelete(ctx context.Context, u string, timeout time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := fleetClient(timeout).Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
