// Library search for the phone remote: search the media servers the user
// registered as music sources, from the speaker itself.
//
// The desktop app browses DLNA servers with the PC's own network stack; the
// phone remote has no such luxury (it is a plain page served by the agent), so
// the agent does the searching. Scope is deliberately the REGISTERED servers
// (the mediaservers store), not everything SSDP can see: those are the servers
// the user chose as music sources, and searching a neighbour's random DLNA
// device from a speaker would surprise more than it helps.
//
// #666 is the reason this file looks the way it does. A reporter with a 2600
// track NAS could browse and play every song from the desktop app but the phone
// found the same song "sometimes". Three separate defects made the outcome
// depend on where a track sits rather than on what was typed, and they are
// called out at the code that fixes each one.

package webui

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/dlna"
)

// librarySearchResult is one playable hit, shaped for the remote's list.
type librarySearchResult struct {
	Title       string `json:"title"`
	Artist      string `json:"artist,omitempty"`
	Album       string `json:"album,omitempty"`
	URL         string `json:"url"`
	Art         string `json:"art,omitempty"`
	Mime        string `json:"mime,omitempty"`
	DurationSec int    `json:"durationSec,omitempty"`
	Server      string `json:"server"`
}

const (
	// librarySearchMax caps the merged result list; a phone screen shows a
	// handful, and every extra row costs the box memory it does not have.
	librarySearchMax = 40
	// libraryWalkMaxBrowses and libraryWalkPage are the bounds of the fallback
	// walk for servers without a usable ContentDirectory Search action. The walk
	// itself lives in the shared dlna package (dlna.WalkSearch) so the desktop
	// app searches such servers the same way (#1705); the bounds and their
	// history are documented there.
	libraryWalkMaxBrowses = dlna.WalkMaxBrowses
	libraryWalkPage       = dlna.WalkPageSize
	// librarySearchDiscovery is the SSDP window. 3 s used to be it, and #110
	// taught the desktop that a NAS needs longer to compose its answer; the
	// same speaker-side miss makes a search silently report nothing.
	librarySearchDiscovery = 5 * time.Second
	// librarySearchBudget is the wall clock the whole request may take,
	// discovery included.
	librarySearchBudget = 35 * time.Second
	// librarySearchPerServer bounds one server's share so a single slow NAS
	// cannot consume the budget of every other registered server.
	librarySearchPerServer = 15 * time.Second
	// libraryRecallTimeout bounds the direct re-probe of a server SSDP missed.
	libraryRecallTimeout = 4 * time.Second
)

// handleLibrarySearch answers GET /api/library/search?q=... with matching
// audio items from every registered media server. LAN-only like the other
// endpoints that make the speaker reach out.
func (s *Server) handleLibrarySearch(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "library search only allowed from LAN", http.StatusForbidden)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Error(w, "q must not be empty", http.StatusBadRequest)
		return
	}
	// field narrows the search to album, artist or title; absent is all three,
	// as before (#1325).
	field := librarySearchField(r.URL.Query().Get("field"))
	if s.mediaServers == nil {
		writeJSON(w, http.StatusOK, map[string]any{"results": []librarySearchResult{}, "registered": 0})
		return
	}
	registered := s.mediaServers.List()
	if len(registered) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"results": []librarySearchResult{}, "registered": 0})
		return
	}

	// Resolve the registered UDNs to live servers. Discovery is the only way
	// to a control URL (the store keeps intent, not addresses, because DHCP
	// moves servers around).
	ctx, cancel := context.WithTimeout(r.Context(), librarySearchBudget)
	defer cancel()
	found, derr := dlna.DiscoverServers(ctx, librarySearchDiscovery)
	if derr != nil {
		s.logger.Info("library search: discovery failed", "err", derr)
	}
	byUDN := make(map[string]dlna.Server, len(found))
	for _, srv := range found {
		byUDN[udnKey(srv.UDN)] = srv
	}
	s.rememberMediaServerLocations(found)

	var results []librarySearchResult
	var missing []string
	partial := false
	for _, reg := range registered {
		key := udnKey(reg.ID)
		srv, ok := byUDN[key]
		if !ok {
			// One silent M-SEARCH round is not proof the server is gone. The
			// desktop hit exactly this in #341 and kept the last known device
			// description URL; the agent now does the same, so a search that
			// happens to fall into a quiet moment does not report an empty
			// library.
			srv, ok = s.recallMediaServer(ctx, key)
		}
		if !ok {
			// Same last resort the browse path takes: the firmware's own
			// discovery cache plus a unicast probe, for networks that filter
			// the agent's multicast round (#726).
			srv, ok = s.resolveViaBoxCache(ctx, key, len(found))
		}
		if !ok {
			missing = append(missing, reg.Name)
			continue
		}
		sctx, scancel := context.WithTimeout(ctx, librarySearchPerServer)
		items, serverPartial := s.searchOneServerBy(sctx, srv, q, field)
		scancel()
		partial = partial || serverPartial
		name := srv.FriendlyName
		if name == "" {
			name = reg.Name
		}
		// A key saved from these hits carries each track's object id (#919).
		s.noteLibraryItems(reg.ID, items)
		for _, it := range items {
			if it.StreamURL == "" {
				continue
			}
			results = append(results, librarySearchResult{
				Title: it.Title, Artist: it.Artist, Album: it.Album,
				URL: it.StreamURL, Art: it.AlbumArtURL, Mime: trackMime(it),
				DurationSec: it.DurationSec, Server: name,
			})
			if len(results) >= librarySearchMax {
				break
			}
		}
		if len(results) >= librarySearchMax {
			// The merged list is full: whatever is left of this server and of
			// the servers after it was not looked at, and the page must say so
			// rather than present the list as the complete answer.
			partial = true
			break
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Title < results[j].Title })
	writeJSON(w, http.StatusOK, map[string]any{
		"results":    results,
		"registered": len(registered),
		"offline":    missing,
		// partial says the search did NOT cover everything it was asked to.
		// The page needs it to keep an empty answer honest: "we searched a
		// corner of your library and found nothing there" is a different
		// statement from "your library does not contain this".
		"partial": partial,
	})
}

// udnKey normalises a UDN for comparison: the store keeps it without the
// "uuid:" prefix the SSDP answer carries, and case differs between servers.
func udnKey(udn string) string {
	return strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(udn), "uuid:"))
}

// rememberMediaServerLocations keeps the device-description URL that discovery
// resolved for each server, so a later search whose M-SEARCH went unanswered
// can still reach the server directly (see recallMediaServer).
func (s *Server) rememberMediaServerLocations(found []dlna.Server) {
	s.mediaLocMu.Lock()
	if s.mediaLoc == nil {
		s.mediaLoc = make(map[string]string, len(found))
	}
	for _, srv := range found {
		if srv.Location == "" {
			continue
		}
		s.mediaLoc[udnKey(srv.UDN)] = srv.Location
	}
	s.mediaLocMu.Unlock()
	for _, srv := range found {
		if srv.Location != "" {
			s.persistMediaServerLocation(udnKey(srv.UDN), srv.Location)
		}
	}
}

// persistMediaServerLocation writes a resolved address into the media-server
// store when the key belongs to a REGISTERED server, so it survives the agent's
// restarts (#726: the in-memory map alone meant every reboot paid the whole
// discovery chain again, about a minute on a box that cannot hear the server).
// The store only writes on a change, so the steady state costs no NAND writes.
func (s *Server) persistMediaServerLocation(key, loc string) {
	if s.mediaServers == nil || loc == "" {
		return
	}
	for _, reg := range s.mediaServers.List() {
		if udnKey(reg.ID) == key {
			if err := s.mediaServers.SetLocation(reg.ID, loc); err != nil {
				s.logger.Warn("library resolve: could not persist the server's address", "err", err)
			}
			return
		}
	}
}

// storedMediaServerLocation returns the persisted last-known address for a
// registered server, "" when none is on file. The reboot-surviving seed for
// recallMediaServer.
func (s *Server) storedMediaServerLocation(key string) string {
	if s.mediaServers == nil {
		return ""
	}
	for _, reg := range s.mediaServers.List() {
		if udnKey(reg.ID) == key {
			return strings.TrimSpace(reg.Location)
		}
	}
	return ""
}

// rememberMediaServerLocationAs stores a device-description URL under a SPECIFIC
// key rather than the server's own UDN. Used when a UUID-regenerating server
// (WD/Twonky) was recovered by name: the registration, the phone and the app all
// still know it under its ORIGINAL UDN, so recall has to find it under that key
// even though the live server now advertises a different one (#733).
func (s *Server) rememberMediaServerLocationAs(key, loc string) {
	if key == "" || loc == "" {
		return
	}
	s.mediaLocMu.Lock()
	if s.mediaLoc == nil {
		s.mediaLoc = make(map[string]string)
	}
	s.mediaLoc[key] = loc
	s.mediaLocMu.Unlock()
	s.persistMediaServerLocation(key, loc)
}

// registeredName returns the friendly name the user registered a server under,
// looked up by its normalised UDN key, or "" when no registered server matches.
func (s *Server) registeredName(key string) string {
	if s.mediaServers == nil {
		return ""
	}
	for _, reg := range s.mediaServers.List() {
		if udnKey(reg.ID) == key {
			return strings.TrimSpace(reg.Name)
		}
	}
	return ""
}

// serverMatchesKey reports whether a described server IS the one registered under
// key: its UDN still matches, or (for a UUID-regenerating server) it carries the
// registered friendly name. The name path is deliberately used only where the
// candidate is a SINGLE server at a specific address (recall, a peer-named
// location), so a name collision cannot silently pick the wrong one; the
// multi-candidate discovery scan uses rematchByName's stricter unique-match rule.
func (s *Server) serverMatchesKey(srv dlna.Server, key string) bool {
	if udnKey(srv.UDN) == key {
		return true
	}
	name := s.registeredName(key)
	return name != "" && strings.EqualFold(strings.TrimSpace(srv.FriendlyName), name)
}

// rematchByName recovers a registered server whose UDN CHANGED by finding the one
// discovered server that carries its registered name, and remembers that server's
// address under the ORIGINAL key so recall reaches it next time. It requires an
// EXACT single name match: zero or several servers sharing the name are left
// unresolved rather than risk hijacking the wrong device.
func (s *Server) rematchByName(key string, found []dlna.Server) (dlna.Server, bool) {
	name := s.registeredName(key)
	if name == "" {
		return dlna.Server{}, false
	}
	var match dlna.Server
	n := 0
	for _, srv := range found {
		if srv.CDSControlURL != "" && strings.EqualFold(strings.TrimSpace(srv.FriendlyName), name) {
			match, n = srv, n+1
		}
	}
	if n != 1 {
		return dlna.Server{}, false
	}
	s.rememberMediaServerLocationAs(key, match.Location)
	s.logger.Info("library resolve: matched a registered server by name after its UDN changed",
		"name", name, "oldKey", key, "newUDN", udnKey(match.UDN))
	return match, true
}

// recallMediaServer re-probes the last known address of a registered server
// that this search's discovery round did not see. The UDN of the answer is
// checked, because DHCP can have handed that address to something else.
func (s *Server) recallMediaServer(ctx context.Context, key string) (dlna.Server, bool) {
	s.mediaLocMu.Lock()
	loc := s.mediaLoc[key]
	s.mediaLocMu.Unlock()
	if loc == "" {
		// The in-memory map is empty after every agent restart; the store
		// carries the last address across reboots (#726).
		loc = s.storedMediaServerLocation(key)
	}
	if loc == "" {
		return dlna.Server{}, false
	}
	pctx, cancel := context.WithTimeout(ctx, libraryRecallTimeout)
	defer cancel()
	srv, err := dlna.DescribeServer(pctx, loc)
	if err != nil || srv.CDSControlURL == "" {
		return dlna.Server{}, false
	}
	// The UDN check stops a DHCP-reassigned address that now serves a DIFFERENT
	// device from being accepted. A UUID-regenerating server (WD/Twonky) fails
	// that check at its OWN unchanged address, so fall back to the registered
	// name: same address, same name, new UUID is still the same server (#733).
	if !s.serverMatchesKey(srv, key) {
		return dlna.Server{}, false
	}
	s.logger.Info("library search: discovery missed a registered server, re-probed its last known address",
		"server", srv.FriendlyName)
	return srv, true
}

// searchOneServer tries the ContentDirectory Search action first and falls back
// to a bounded breadth-first browse walk. It reports whether the answer is
// partial, i.e. whether it stopped on a budget rather than on exhaustion.
//
// The fast path is only trusted when it actually returns something. A Search
// that answers HTTP 200 with zero hits used to end the search, and that is not
// the same as "not in the library": measured on the FRITZ!Box 6690 media
// server, "Fly FRITZ" returns one hit while "Fly FRITZ!" returns 200 with
// NumberReturned 0 for the same track, a quirk of the server's own index. One
// typed character decided whether the phone found the song, which is exactly
// the "sometimes it finds it" of #666.
func (s *Server) searchOneServer(ctx context.Context, srv dlna.Server, q string) ([]dlna.Item, bool) {
	return s.searchOneServerBy(ctx, srv, q, dlna.SearchAny)
}

// librarySearchField reads the field a search is narrowed to (#1325): album,
// artist or title, anything else is "any", today's title-artist-album match.
func librarySearchField(raw string) string {
	switch f := strings.ToLower(strings.TrimSpace(raw)); f {
	case dlna.SearchAlbum, dlna.SearchArtist, dlna.SearchTitle:
		return f
	}
	return dlna.SearchAny
}

// searchOneServerBy is searchOneServer narrowed to one field. The title-only
// retry only makes sense where the title is part of what was asked: an artist
// search answered with title hits would list the wrong songs.
func (s *Server) searchOneServerBy(ctx context.Context, srv dlna.Server, q, field string) ([]dlna.Item, bool) {
	titleRetry := field == dlna.SearchAny || field == dlna.SearchTitle
	res, err := dlna.SearchBy(ctx, srv, q, field, librarySearchMax)
	if err != nil && !titleRetry {
		s.logger.Info("library search: Search action refused the field, walking the tree (bounded)",
			"server", srv.FriendlyName, "q", q, "field", field, "err", err)
	} else if err != nil {
		// A server that indexes titles only faults on the widened criteria
		// (UPnPError 708) instead of answering it. Ask the narrow way before
		// paying for the walk.
		if narrow, nerr := dlna.SearchTitleOnly(ctx, srv, q, librarySearchMax); nerr == nil {
			res, err = narrow, nil
		} else {
			s.logger.Info("library search: Search action unavailable, walking the tree (bounded)",
				"server", srv.FriendlyName, "q", q, "err", err, "titleOnlyErr", nerr)
		}
	} else if len(res.Items) == 0 && titleRetry {
		// A 200 with zero hits is not proof of absence either: the FRITZ!Box
		// index answers "Fly FRITZ!" with zero and "Fly FRITZ" with one for
		// the same track, and the widened boolean criteria has its own quirks
		// per server. One narrow retry is a single cheap SOAP call next to the
		// walk it can save.
		if narrow, nerr := dlna.SearchTitleOnly(ctx, srv, q, librarySearchMax); nerr == nil && len(narrow.Items) > 0 {
			res = narrow
		}
	}
	if err == nil && len(res.Items) > 0 {
		s.logger.Info("library search: server answered the Search action",
			"server", srv.FriendlyName, "q", q, "hits", len(res.Items), "total", res.TotalMatches)
		return res.Items, res.TotalMatches > len(res.Items)
	}
	if err == nil {
		// The query rides along because a bundle without it cannot separate
		// "the server's index has no such tag" from a search quirk: #666's
		// bundle carried three zero-hit walks and no way to tell which (the
		// search matches TAGS, while the folder view the user compares against
		// shows file names).
		s.logger.Info("library search: Search answered nothing, walking the tree (bounded)",
			"server", srv.FriendlyName, "q", q, "field", field)
	}
	return s.walkOneServerBy(ctx, srv, q, field)
}

// walkOneServer is the fallback: a breadth-first browse of the server's tree,
// matching title, artist and album the way the desktop Library filters a loaded
// folder. Bounded in calls, depth and per-container paging so a huge NAS cannot
// pin the speaker's CPU.
func (s *Server) walkOneServer(ctx context.Context, srv dlna.Server, q string) ([]dlna.Item, bool) {
	return s.walkOneServerBy(ctx, srv, q, dlna.SearchAny)
}

// walkOneServerBy is the walk, matching only the field a search was narrowed
// to (#1325). The walk is dlna.WalkSearch, shared with the desktop app; the
// agent keeps its own log lines around it.
func (s *Server) walkOneServerBy(ctx context.Context, srv dlna.Server, q, field string) ([]dlna.Item, bool) {
	res := dlna.WalkSearch(ctx, srv, q, field, dlna.WalkOptions{
		MaxHits: librarySearchMax,
		OnReadError: func(container string, start int, err error) {
			// A container we could not read is a hole in the coverage, not a
			// container that holds nothing. Log it: the reporter's own server
			// in #666 could not be reproduced here, and a diagnostic taken
			// after a failing search is the only way to learn that his NAS
			// refuses a particular container or page offset.
			s.logger.Info("library search: a container could not be read, coverage is incomplete",
				"server", srv.FriendlyName, "container", container, "start", start, "err", err)
		},
	})
	s.logger.Info("library search: bounded walk finished",
		"server", srv.FriendlyName, "q", q, "browses", res.Browses, "hits", len(res.Items), "partial", res.Partial)
	return res.Items, res.Partial
}
