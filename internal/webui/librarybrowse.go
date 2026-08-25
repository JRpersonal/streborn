package webui

// The phone remote's media-server browser (#390, mail request 2026-08-25 with
// the Bose app's home screen as the reference): browse the REGISTERED media
// servers folder by folder from the :8888 page and play a track on the box,
// so the phone covers the last thing that still needed the desktop app or the
// dead Bose app. Strictly user-driven, one Browse page per tap: the box never
// walks a library on its own here (the bounded search walk stays the only
// automated reader).

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/JRpersonal/streborn/dlna"
)

const (
	// libraryBrowsePage is one Browse page. The same size the desktop Library
	// uses, for the same reason: one bigger SOAP call beats many small ones.
	libraryBrowsePage = 200
	// libraryBrowseTimeout bounds one page fetch, resolution included. A slow
	// NAS answering a first page can take a while (the #666 QNAP let one
	// container time out at 15 s), so this is deliberately looser than the
	// search's per-server share; it is one user tap, not a fan-out.
	libraryBrowseTimeout = 20 * time.Second
)

// handleLibraryServers lists the registered media servers for the phone page.
// Store only, no network I/O: whether a server currently answers is settled by
// the first browse tap, where the answer can be shown next to the action.
func (s *Server) handleLibraryServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	type srvOut struct {
		UDN  string `json:"udn"`
		Name string `json:"name"`
	}
	out := []srvOut{}
	if s.mediaServers != nil {
		for _, reg := range s.mediaServers.List() {
			out = append(out, srvOut{UDN: reg.ID, Name: reg.Name})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// resolveMediaServer turns a registered UDN into a live dlna.Server: the last
// known device-description address first (fast, one direct probe), a fresh
// SSDP round only when that fails. The search flow does it the other way
// around because it resolves EVERY server at once; a browse tap resolves one,
// and the user is waiting.
func (s *Server) resolveMediaServer(ctx context.Context, udn string) (dlna.Server, bool) {
	key := udnKey(udn)
	if srv, ok := s.recallMediaServer(ctx, key); ok {
		return srv, true
	}
	found, err := dlna.DiscoverServers(ctx, librarySearchDiscovery)
	if err != nil {
		return dlna.Server{}, false
	}
	s.rememberMediaServerLocations(found)
	for _, srv := range found {
		if udnKey(srv.UDN) == key {
			return srv, true
		}
	}
	return dlna.Server{}, false
}

// handleLibraryBrowse serves one page of one container of one registered
// server: GET /api/library/browse?udn=<id>&id=<container>&start=<n>.
// Container id "" means the server root ("0").
func (s *Server) handleLibraryBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	udn := r.URL.Query().Get("udn")
	if udn == "" || s.mediaServers == nil {
		http.Error(w, "udn required", http.StatusBadRequest)
		return
	}
	// Only REGISTERED servers are browsable, the same rule the search applies:
	// this endpoint answers an unauthenticated LAN GET, and it must not turn
	// the speaker into a generic proxy for probing arbitrary UPnP devices.
	registered := false
	for _, reg := range s.mediaServers.List() {
		if udnKey(reg.ID) == udnKey(udn) {
			registered = true
			break
		}
	}
	if !registered {
		http.Error(w, "not a registered music source", http.StatusNotFound)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		id = "0"
	}
	start, _ := strconv.Atoi(r.URL.Query().Get("start"))
	if start < 0 {
		start = 0
	}

	ctx, cancel := context.WithTimeout(r.Context(), libraryBrowseTimeout)
	defer cancel()
	srv, ok := s.resolveMediaServer(ctx, udn)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"offline": true})
		return
	}
	res, err := dlna.Browse(ctx, srv, id, start, libraryBrowsePage)
	if err != nil {
		s.logger.Info("library browse: container could not be read",
			"server", srv.FriendlyName, "container", id, "start", start, "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"offline": true})
		return
	}

	type folderOut struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		ChildCount int    `json:"childCount,omitempty"`
	}
	type trackOut struct {
		Title       string `json:"title"`
		Artist      string `json:"artist,omitempty"`
		Album       string `json:"album,omitempty"`
		URL         string `json:"url"`
		Art         string `json:"art,omitempty"`
		Mime        string `json:"mime,omitempty"`
		DurationSec int    `json:"durationSec,omitempty"`
	}
	folders := []folderOut{}
	for _, c := range res.Containers {
		folders = append(folders, folderOut{ID: c.ID, Title: c.Title, ChildCount: c.ChildCount})
	}
	tracks := []trackOut{}
	for _, it := range res.Items {
		if it.StreamURL == "" {
			continue
		}
		tracks = append(tracks, trackOut{
			Title: it.Title, Artist: it.Artist, Album: it.Album,
			URL: it.StreamURL, Art: it.AlbumArtURL, Mime: it.MimeType,
			DurationSec: it.DurationSec,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server":  srv.FriendlyName,
		"folders": folders,
		"tracks":  tracks,
		"total":   res.TotalMatches,
		"start":   start,
		"count":   res.Returned,
	})
}
