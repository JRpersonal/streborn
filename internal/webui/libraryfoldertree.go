// Playing a library folder together with every folder below it (#1682).
//
// "Play folder" and "Put folder on a key" used to exist only in a folder that
// holds songs itself. A music library sorted the usual way, one folder per
// artist with one sub-folder per album, has no songs in the artist folder, so
// neither action was there where the owner wanted to play "everything by this
// artist". A folder without songs of its own now plays the songs of its
// sub-folders: each album complete and in its own track order (#1487), the
// albums one after the other.
//
// A tree can be any size, so the walk is bounded the same way everywhere it
// runs (here, the desktop app's views/library.js and the phone page):
// libraryTreeMaxDepth levels below the folder, libraryTreeMaxTracks songs and
// libraryTreeMaxFolders folders read. What did not fit is reported, so the
// listener is told the queue is not the whole folder.
//
// The rule is "no songs of its own": a folder that holds songs keeps playing
// exactly those, as it always did. That one rule is what the folder card
// replay and the key re-resolve (libraryreresolve.go) apply too, so a folder
// started here, saved on a key or replayed from Recently played is the same
// list of songs everywhere.

package webui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/internal/presets"
)

const (
	// libraryTreeMaxDepth is how many folder levels below the played folder
	// are read: artist > album > disc still fits, a whole library does not.
	// Keep in step with LIB_TREE_MAX_DEPTH (desktop views/library.js) and the
	// phone page.
	libraryTreeMaxDepth = 3
	// libraryTreeMaxTracks caps the songs one walk collects: the queue preset
	// cap, so the same folder fits on a key whole or not at all.
	libraryTreeMaxTracks = presets.MaxQueueItems
	// libraryTreeMaxFolders caps the folders one walk reads, each at least one
	// Browse against a NAS that may be slow.
	libraryTreeMaxFolders = 300
	// libraryTreeBudget bounds a walk started from the phone page.
	libraryTreeBudget = 60 * time.Second
)

// folderTree is what a walk collected.
type folderTree struct {
	items []dlna.Item
	// folders is how many folders were read, the played one included.
	folders int
	// cut is set when the walk stopped before the end: the song or folder
	// cap, or the time ran out.
	cut bool
	// cutDepth is set when folders deeper than libraryTreeMaxDepth were left
	// out.
	cutDepth bool
	// subfolders is set when the songs come from the sub-folders, because the
	// folder itself holds none.
	subfolders bool
	// partial is set when a folder of the walk was read only in part or not
	// at all: a later page failed, the per-folder cap was reached, or a
	// sub-folder could not be read and was skipped.
	partial bool
}

// complete reports whether the walk read every folder to the end: nothing
// was cut, left out for depth, or skipped. Only then does a song missing
// from it say the folder no longer holds that song.
func (t folderTree) complete() bool {
	return !t.cut && !t.cutDepth && !t.partial
}

// collectFolderTracks returns the songs a folder plays as a whole: its own,
// in album track order (#1487), or, when it holds none, those of its
// sub-folders (walkSubfolders). az orders the sub-folders A to Z instead of
// the server's order (#1257).
func (s *Server) collectFolderTracks(ctx context.Context, srv dlna.Server, container string, az bool) (folderTree, error) {
	subs, items, whole, err := s.browseContainerChildren(ctx, srv, container)
	if err != nil {
		return folderTree{}, err
	}
	t := folderTree{folders: 1, partial: !whole}
	if len(items) > 0 {
		// Only the whole folder is put in track order: a cut-short list ordered
		// on its own would not be the album's order either.
		if whole {
			items = dlna.OrderContainerItems(ctx, srv, container, items)
		}
		t.items = items
		return t, nil
	}
	t.subfolders = len(subs) > 0
	seen := map[string]bool{}
	visited := map[string]bool{container: true}
	s.walkSubfolders(ctx, srv, subs, 1, az, &t, seen, visited)
	return t, nil
}

// walkSubfolders adds the songs of every folder in subs, and of theirs, depth
// first: an album's songs, then the next album. depth is the level of subs
// below the played folder.
func (s *Server) walkSubfolders(ctx context.Context, srv dlna.Server, subs []dlna.Container, depth int, az bool,
	t *folderTree, seen, visited map[string]bool) {
	if az {
		subs = sortLibraryFolders(subs)
	}
	for _, c := range subs {
		if t.cut {
			return
		}
		if visited[c.ID] {
			continue // a server that lists a folder inside itself
		}
		visited[c.ID] = true
		if ctx.Err() != nil || t.folders >= libraryTreeMaxFolders {
			t.cut = true
			return
		}
		t.folders++
		more, items, whole, err := s.browseContainerChildren(ctx, srv, c.ID)
		if err != nil {
			if ctx.Err() != nil {
				t.cut = true
				return
			}
			// One unreadable album does not cost the others.
			s.logger.Info("library folder: a sub-folder could not be read, skipping it",
				"server", srv.FriendlyName, "folder", c.ID, "err", err)
			t.partial = true
			continue
		}
		if whole {
			items = dlna.OrderContainerItems(ctx, srv, c.ID, items)
		} else {
			t.partial = true
		}
		if az {
			items = sortLibraryTracks(items, c.Class)
		}
		for _, it := range items {
			if it.StreamURL == "" || seen[it.StreamURL] {
				continue // the same song listed under two folders plays once
			}
			seen[it.StreamURL] = true
			t.items = append(t.items, it)
			if len(t.items) >= libraryTreeMaxTracks {
				t.cut = true
				return
			}
		}
		if len(more) == 0 {
			continue
		}
		if depth >= libraryTreeMaxDepth {
			t.cutDepth = true
			continue
		}
		s.walkSubfolders(ctx, srv, more, depth+1, az, t, seen, visited)
	}
}

// ---- POST /api/library/play-folder ----

type libraryPlayFolderRequest struct {
	UDN  string `json:"udn"`
	ID   string `json:"id"`
	Name string `json:"name"`
	Art  string `json:"art"`
	// Sort is the order the listener sees the folders in: "az" for A to Z,
	// anything else the server's order.
	Sort string `json:"sort"`
}

// handleLibraryPlayFolder answers POST /api/library/play-folder: read a folder
// of a registered media server, its sub-folders included when it holds no
// songs itself (collectFolderTracks), and start it as a queue. The phone page
// uses it for a folder of folders, which it cannot walk itself: it only ever
// sees one page of one folder. The answer says how many songs play and
// whether the walk was cut short.
func (s *Server) handleLibraryPlayFolder(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
	var req libraryPlayFolderRequest
	if !decodeJSONRequest(w, r, 1<<16, &req) {
		return
	}
	udn := strings.TrimSpace(req.UDN)
	if udn == "" {
		http.Error(w, "udn required", http.StatusBadRequest)
		return
	}
	container := containerOrRoot(req.ID)
	if ref := s.pairPlayRefusal(); ref != nil {
		writePlayRefusal(w, ref)
		return
	}
	// The same rule as browse, search and the card replay: only servers the
	// user registered as music sources.
	if s.mediaServers == nil || !s.mediaServerRegistered(udn) {
		http.Error(w, "not a registered music source", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), libraryTreeBudget)
	defer cancel()
	srv, live, found := s.resolveMediaServer(ctx, udn)
	if !found {
		writeJSON(w, http.StatusOK, s.libraryOfflineReply(udnKey(udn), live))
		return
	}
	tree, err := s.collectFolderTracks(ctx, srv, container, req.Sort == "az")
	if err != nil {
		s.logger.Info("library folder: the folder could not be read",
			"server", srv.FriendlyName, "container", container, "err", err)
		writeJSON(w, http.StatusOK, s.libraryOfflineReply(udnKey(udn), live))
		return
	}
	if len(tree.items) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"error": "the folder has no playable tracks", "code": "folder-empty"})
		return
	}
	// A key saved while this plays carries each song's object id (#919).
	s.noteLibraryItems(udn, tree.items)
	items := queueItemsFromLibrary(tree.items)
	art := strings.TrimSpace(req.Art)
	if art == "" {
		art = items[0].Art
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = srv.FriendlyName
	}
	// The card key the desktop app writes for a folder, so Recently played
	// files it as one folder and its replay reads the same tree again.
	card := recentCardCtx{key: queueCardKeyPrefix + "uuid:" + udnKey(udn) + ":" + container, name: name, art: art, source: srv.FriendlyName}
	shuffle, rep, _ := s.loadPlayMode()
	playCtx, playCancel := context.WithTimeout(context.WithoutCancel(r.Context()), playDetachTimeout)
	defer playCancel()
	s.logger.Info("library folder: starting the folder as a queue",
		"server", srv.FriendlyName, "container", container, "tracks", len(items), "folders", tree.folders,
		"subfolders", tree.subfolders, "cut", tree.cut, "cutDepth", tree.cutDepth, "shuffle", shuffle)
	if err := s.startQueue(playCtx, items, -1, shuffle, rep, card); err != nil {
		if isGroupedRejection(err) {
			s.writeGroupedPlayError(w, err)
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"tracks":     len(items),
		"folders":    tree.folders,
		"subfolders": tree.subfolders,
		"cut":        tree.cut,
		"cutDepth":   tree.cutDepth,
		"maxDepth":   libraryTreeMaxDepth,
	})
}
