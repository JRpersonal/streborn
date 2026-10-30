// favorites_add.go: a favourite starred somewhere other than the desktop app
// (#1621).
//
// The desktop app owns the starred list and pushes all of it to every speaker.
// Removing one from the phone page has worked since #1389, through a removal
// note the desktop app reads back. Adding one had no way in at all: a remote
// app that PUT a longer list saw its star wiped by the desktop app's next push,
// and the phone page had no star to set. The author of STR Remote (#1233) ran
// into exactly that.
//
// The addition note is the mirror image of the removal note. POST stores the
// favourite on the speaker and notes it, with the whole row, so the desktop app
// can add it without looking anything up. Until the desktop app has it, a
// pushed list that lacks it gets it back; the first push that carries it
// settles the note. A desktop app that never catches up (an older one) simply
// never shows the star, and nothing on the speaker is lost.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// favoriteAdditionsPath remembers favourites starred outside the desktop app
// that the desktop app has not taken over yet. var so a test can point it at a
// temp tree.
var favoriteAdditionsPath = "/mnt/nv/streborn/favorites-added.json"

// maxFavoriteAdditions bounds the addition notes, newest kept. A note only
// lives until the desktop app's next push carries the favourite, so this is
// reached only by somebody starring a hundred stations with no desktop app.
const maxFavoriteAdditions = 100

// maxFavoriteAddJSON bounds one POSTed favourite. A row is a few hundred bytes.
const maxFavoriteAddJSON = 16 << 10

// favoriteAddition records one favourite starred outside the desktop app: its
// key, when (Unix ms, this speaker's clock), and the row itself.
type favoriteAddition struct {
	ID  string   `json:"id"`
	At  int64    `json:"at"`
	Fav Favorite `json:"fav"`
}

// additionsCache keeps the addition notes as last read or written, for the
// same reason as removalsCache: the desktop app asks for the notes every few
// seconds while a screen with stars is open, and the agent is the only writer.
var additionsCache struct {
	sync.Mutex
	path  string
	notes []favoriteAddition
	ok    bool
}

func loadFavoriteAdditions() []favoriteAddition {
	additionsCache.Lock()
	defer additionsCache.Unlock()
	if additionsCache.ok && additionsCache.path == favoriteAdditionsPath {
		return append([]favoriteAddition{}, additionsCache.notes...)
	}
	out := []favoriteAddition{}
	if b, err := os.ReadFile(favoriteAdditionsPath); err == nil {
		var in []favoriteAddition
		if json.Unmarshal(b, &in) == nil && in != nil {
			out = in
		}
	}
	additionsCache.path, additionsCache.notes, additionsCache.ok = favoriteAdditionsPath, out, true
	return append([]favoriteAddition{}, out...)
}

func setAdditionsCache(notes []favoriteAddition) {
	additionsCache.Lock()
	defer additionsCache.Unlock()
	if notes == nil {
		additionsCache.ok = false
		return
	}
	additionsCache.path, additionsCache.notes, additionsCache.ok = favoriteAdditionsPath, append([]favoriteAddition{}, notes...), true
}

// storeFavoriteAdditions writes the notes, newest kept when over the cap, and
// only when they differ from what is on the flash.
func storeFavoriteAdditions(in []favoriteAddition) error {
	sort.SliceStable(in, func(i, j int) bool { return in[i].At > in[j].At })
	if len(in) > maxFavoriteAdditions {
		in = in[:maxFavoriteAdditions]
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(favoriteAdditionsPath); err == nil && string(old) == string(b) {
		setAdditionsCache(in)
		return nil
	}
	if len(in) == 0 {
		if _, err := os.Stat(favoriteAdditionsPath); os.IsNotExist(err) {
			setAdditionsCache(in)
			return nil
		}
	}
	if err := writeNANDFile(favoriteAdditionsPath, b); err != nil {
		setAdditionsCache(nil)
		return err
	}
	setAdditionsCache(in)
	return nil
}

// dropFavoriteAddition removes the note for id, if there is one. Called with
// favoritesMu held.
func dropFavoriteAddition(id string) error {
	notes := loadFavoriteAdditions()
	out := make([]favoriteAddition, 0, len(notes))
	for _, n := range notes {
		if n.ID != id {
			out = append(out, n)
		}
	}
	if len(out) == len(notes) {
		return nil
	}
	return storeFavoriteAdditions(out)
}

// applyFavoriteAdditions puts back into a pushed list every favourite starred
// outside the desktop app that the list does not carry yet: the desktop app
// has not read the note, so its list is simply older. A note whose favourite
// the list already carries has been taken over and is settled. It returns the
// list, the notes still open, and how many favourites it put back.
func applyFavoriteAdditions(in []Favorite, adds []favoriteAddition) ([]Favorite, []favoriteAddition, int) {
	if len(adds) == 0 {
		return in, adds, 0
	}
	have := make(map[string]bool, len(in))
	for _, f := range in {
		have[f.key()] = true
	}
	out := append(make([]Favorite, 0, len(in)+len(adds)), in...)
	open := make([]favoriteAddition, 0, len(adds))
	restored := 0
	for _, a := range adds {
		if have[a.ID] {
			continue
		}
		have[a.ID] = true
		out = append(out, a.Fav)
		open = append(open, a)
		restored++
	}
	return out, open, restored
}

// isStreamAddress reports whether u is an http(s) address, the only kind a
// favourite can play from.
func isStreamAddress(u string) bool {
	u = strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// addFavorite stars one favourite on this speaker, notes it for the desktop
// app, and, unless the request came from another speaker (local=1), hands it
// on to the other STR speakers.
func (s *Server) addFavorite(w http.ResponseWriter, r *http.Request) {
	var in Favorite
	if !decodeJSONRequest(w, r, maxFavoriteAddJSON, &in) {
		return
	}
	local := r.URL.Query().Get("local") == "1"
	clean := sanitiseFavorites([]Favorite{in})
	if len(clean) == 0 || !isStreamAddress(clean[0].playableURL()) {
		http.Error(w, "a favourite needs a name and an http(s) stream address", http.StatusBadRequest)
		return
	}
	f := clean[0]
	// A fresh star is starred now. A peer's copy keeps the time the first
	// speaker gave it, so every speaker and the desktop app agree on it.
	if !local || f.StarredAt <= 0 {
		f.StarredAt = time.Now().UnixMilli()
	}
	id := f.key()

	favoritesMu.Lock()
	list := loadFavorites()
	present := false
	for _, have := range list {
		if have.key() == id {
			present = true
			break
		}
	}
	var err error
	if !present {
		if len(list) >= maxFavorites {
			favoritesMu.Unlock()
			http.Error(w, "the favourites list is full", http.StatusConflict)
			return
		}
		var b []byte
		if b, err = json.Marshal(append(list, f)); err == nil {
			err = writeNANDFile(favoritesPath, b)
		}
	}
	if err == nil {
		// Starred again after a removal: the newer decision wins, so the
		// removal note goes.
		notes := loadFavoriteRemovals()
		kept := make([]favoriteRemoval, 0, len(notes))
		for _, n := range notes {
			if n.ID != id {
				kept = append(kept, n)
			}
		}
		if len(kept) != len(notes) {
			err = storeFavoriteRemovals(kept)
		}
	}
	if err == nil {
		adds := loadFavoriteAdditions()
		out := make([]favoriteAddition, 0, len(adds)+1)
		for _, a := range adds {
			if a.ID != id {
				out = append(out, a)
			}
		}
		out = append(out, favoriteAddition{ID: id, At: f.StarredAt, Fav: f})
		err = storeFavoriteAdditions(out)
	}
	favoritesMu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.logger.Info("favorite added on the speaker", "added", !present, "forward", !local)
	if !local && s.peersFn != nil {
		body, _ := json.Marshal(f)
		go s.forwardFavoriteChange(context.Background(), "addition", http.MethodPost, "/api/favorites?local=1", body)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": !present, "id": id})
}
