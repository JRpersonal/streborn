// favorites_order.go: the order of the starred stations (#1519).
//
// The favourites showed in the order they were starred, with no way to move
// one. The desktop app owns the list and pushes all of it, in its own order,
// to every speaker; moving an entry there is simply a push in the new order.
// Moving one on the phone page (or in a remote app) needs the same kind of
// note the removals (#1389) and additions (#1621) have, or the desktop app's
// next push would put the old order straight back.
//
// The order note is one record, not a list: the last order set outside the
// desktop app and when. A desktop push that knows an order at least that new
// (orderAt) settles it; an older push gets the noted order laid over it. The
// desktop app reads the note with the other notes and takes the order over.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// favoriteOrderPath remembers the last order set on a phone page or in a remote
// app that the desktop app has not taken over yet. var so a test can point it
// at a temp tree.
var favoriteOrderPath = "/mnt/nv/streborn/favorites-order.json"

// favoriteOrder is the order set outside the desktop app: every favourite's key
// in the new order, and when (Unix ms, the first speaker's clock).
type favoriteOrder struct {
	At  int64    `json:"at"`
	IDs []string `json:"ids"`
}

// orderCache keeps the note as last read or written, for the same reason as
// removalsCache: the desktop app asks for the notes every few seconds while a
// screen with stars is open, and the agent is the only writer.
var orderCache struct {
	sync.Mutex
	path string
	note *favoriteOrder
	ok   bool
}

// loadFavoriteOrder returns the order note, or nil when there is none. A
// damaged file reads as none.
func loadFavoriteOrder() *favoriteOrder {
	orderCache.Lock()
	defer orderCache.Unlock()
	if orderCache.ok && orderCache.path == favoriteOrderPath {
		return copyFavoriteOrder(orderCache.note)
	}
	var note *favoriteOrder
	if b, err := os.ReadFile(favoriteOrderPath); err == nil {
		var in favoriteOrder
		if json.Unmarshal(b, &in) == nil && len(in.IDs) > 0 {
			note = &in
		}
	}
	orderCache.path, orderCache.note, orderCache.ok = favoriteOrderPath, note, true
	return copyFavoriteOrder(note)
}

func copyFavoriteOrder(o *favoriteOrder) *favoriteOrder {
	if o == nil {
		return nil
	}
	return &favoriteOrder{At: o.At, IDs: append([]string{}, o.IDs...)}
}

// storeFavoriteOrder writes the note, or removes it for nil. Only when it
// differs from what is on the flash.
func storeFavoriteOrder(o *favoriteOrder) error {
	orderCache.Lock()
	defer orderCache.Unlock()
	if o == nil {
		if err := os.Remove(favoriteOrderPath); err != nil && !os.IsNotExist(err) {
			orderCache.ok = false
			return err
		}
		orderCache.path, orderCache.note, orderCache.ok = favoriteOrderPath, nil, true
		return nil
	}
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(favoriteOrderPath); err != nil || string(old) != string(b) {
		if err := writeNANDFile(favoriteOrderPath, b); err != nil {
			orderCache.ok = false
			return err
		}
	}
	orderCache.path, orderCache.note, orderCache.ok = favoriteOrderPath, copyFavoriteOrder(o), true
	return nil
}

// applyFavoriteOrder puts the favourites named in ids into that order. The
// ones ids does not name (starred after the order was set, or never known to
// the speaker that set it) follow in the order they had, so nothing is lost.
func applyFavoriteOrder(in []Favorite, ids []string) []Favorite {
	if len(ids) == 0 || len(in) < 2 {
		return in
	}
	pos := make(map[string]int, len(ids))
	for i, id := range ids {
		if _, dup := pos[id]; !dup {
			pos[id] = i
		}
	}
	placed := make([]Favorite, len(ids))
	filled := make([]bool, len(ids))
	rest := make([]Favorite, 0, len(in))
	for _, f := range in {
		if p, ok := pos[f.key()]; ok && !filled[p] {
			placed[p], filled[p] = f, true
			continue
		}
		rest = append(rest, f)
	}
	out := make([]Favorite, 0, len(in))
	for i, f := range placed {
		if filled[i] {
			out = append(out, f)
		}
	}
	return append(out, rest...)
}

// favoriteKeys lists the keys of a list, in its order.
func favoriteKeys(list []Favorite) []string {
	out := make([]string, len(list))
	for i, f := range list {
		out[i] = f.key()
	}
	return out
}

// sameFavoriteOrder reports whether two lists hold the same favourites in the
// same order.
func sameFavoriteOrder(a, b []Favorite) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].key() != b[i].key() {
			return false
		}
	}
	return true
}

// pushedOrderAt reads orderAt from a desktop push: when the desktop app's own
// order was last set (Unix ms), 0 when the app is older than #1519.
func pushedOrderAt(r *http.Request) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("orderAt")), 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// settleFavoriteOrder applies the order note to a pushed list. A push whose
// order is at least as new as the note settles it (the desktop app has taken
// the order over, or set a newer one); an older push gets the noted order.
// Called with favoritesMu held.
func (s *Server) settleFavoriteOrder(in []Favorite, orderAt int64) []Favorite {
	note := loadFavoriteOrder()
	if note == nil {
		return in
	}
	if orderAt >= note.At {
		if err := storeFavoriteOrder(nil); err != nil {
			s.logger.Warn("favorites: could not drop the order note", "err", err)
		}
		return in
	}
	out := applyFavoriteOrder(in, note.IDs)
	if !sameFavoriteOrder(in, out) {
		s.logger.Info("favorites: kept the order set on a phone in a pushed list")
	}
	return out
}

// maxFavoriteOrderJSON bounds an order request: up to maxFavorites keys, and a
// key can be a name and an address.
const maxFavoriteOrderJSON = 256 << 10

// handleFavoriteOrder sets the order of the favourites (#1519).
//
//	POST {"ids":["key", ...]}   the favourites' keys in the new order
//
// Keys the speaker does not know are skipped; favourites the request does not
// name follow in their old order. The order is stored on this speaker, noted
// for the desktop app, and handed on to the other STR speakers unless local=1.
// Answers {"ok":true,"count":n,"at":ms}.
func (s *Server) handleFavoriteOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	var in struct {
		IDs []string `json:"ids"`
		At  int64    `json:"at"`
	}
	if !decodeJSONRequest(w, r, maxFavoriteOrderJSON, &in) {
		return
	}
	ids := make([]string, 0, len(in.IDs))
	for _, id := range in.IDs {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 4096 {
			http.Error(w, "every id must be a favourite's key", http.StatusBadRequest)
			return
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 || len(ids) > maxFavorites {
		http.Error(w, "specify the favourites' keys in the new order", http.StatusBadRequest)
		return
	}
	local := r.URL.Query().Get("local") == "1"
	// A fresh order is set now. A peer's copy keeps the time the first speaker
	// gave it, so every speaker and the desktop app agree on it.
	at := time.Now().UnixMilli()
	if local && in.At > 0 {
		at = in.At
	}

	favoritesMu.Lock()
	list := loadFavorites()
	next := applyFavoriteOrder(list, ids)
	var err error
	if !sameFavoriteOrder(list, next) {
		var b []byte
		if b, err = json.Marshal(next); err == nil {
			err = writeNANDFile(favoritesPath, b)
		}
	}
	if err == nil {
		// The whole new order, not only the keys asked for: the desktop app
		// may know favourites this request did not name.
		err = storeFavoriteOrder(&favoriteOrder{At: at, IDs: favoriteKeys(next)})
	}
	favoritesMu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.logger.Info("favorites reordered on the speaker", "count", len(next), "forward", !local)
	if !local && s.peersFn != nil {
		body, _ := json.Marshal(map[string]any{"ids": favoriteKeys(next), "at": at})
		go s.forwardFavoriteChange(context.Background(), "order", http.MethodPost, "/api/favorites/order?local=1", body)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(next), "at": at})
}
