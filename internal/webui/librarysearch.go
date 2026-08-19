// Library search for the phone remote: search the media servers the user
// registered as music sources, from the speaker itself.
//
// The desktop app browses DLNA servers with the PC's own network stack; the
// phone remote has no such luxury (it is a plain page served by the agent), so
// the agent does the searching. Scope is deliberately the REGISTERED servers
// (the mediaservers store), not everything SSDP can see: those are the servers
// the user chose as music sources, and searching a neighbour's random DLNA
// device from a speaker would surprise more than it helps.

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
	// libraryWalkMaxBrowses bounds the fallback walk for servers without the
	// ContentDirectory Search action: at 50 children per Browse this still
	// covers thousands of entries while keeping the worst case a few seconds.
	libraryWalkMaxBrowses = 40
	// libraryWalkMaxDepth keeps the fallback out of pathological trees; real
	// libraries put tracks 2-3 levels down (Artist/Album/Track).
	libraryWalkMaxDepth = 4
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
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	found, derr := dlna.DiscoverServers(ctx, 3*time.Second)
	if derr != nil {
		s.logger.Info("library search: discovery failed", "err", derr)
	}
	byUDN := make(map[string]dlna.Server, len(found))
	for _, srv := range found {
		byUDN[strings.ToUpper(strings.TrimPrefix(srv.UDN, "uuid:"))] = srv
	}

	var results []librarySearchResult
	var missing []string
	for _, reg := range registered {
		srv, ok := byUDN[strings.ToUpper(strings.TrimPrefix(reg.ID, "uuid:"))]
		if !ok {
			missing = append(missing, reg.Name)
			continue
		}
		items := s.searchOneServer(ctx, srv, q)
		name := srv.FriendlyName
		if name == "" {
			name = reg.Name
		}
		for _, it := range items {
			if it.StreamURL == "" {
				continue
			}
			results = append(results, librarySearchResult{
				Title: it.Title, Artist: it.Artist, Album: it.Album,
				URL: it.StreamURL, Art: it.AlbumArtURL, Mime: it.MimeType,
				DurationSec: it.DurationSec, Server: name,
			})
			if len(results) >= librarySearchMax {
				break
			}
		}
		if len(results) >= librarySearchMax {
			break
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Title < results[j].Title })
	writeJSON(w, http.StatusOK, map[string]any{
		"results":    results,
		"registered": len(registered),
		"offline":    missing,
	})
}

// searchOneServer tries the ContentDirectory Search action first and falls
// back to a bounded breadth-first browse walk where the server does not
// implement it (Search is optional in the spec; the walk is capped in calls
// and depth so a huge NAS cannot pin the speaker's CPU for long).
func (s *Server) searchOneServer(ctx context.Context, srv dlna.Server, q string) []dlna.Item {
	if res, err := dlna.Search(ctx, srv, q, librarySearchMax); err == nil {
		s.logger.Info("library search: server answered the Search action",
			"server", srv.FriendlyName, "hits", len(res.Items))
		return res.Items
	} else {
		s.logger.Info("library search: Search action unavailable, walking the tree (bounded)",
			"server", srv.FriendlyName, "err", err)
	}

	qLower := strings.ToLower(q)
	matches := func(it dlna.Item) bool {
		return strings.Contains(strings.ToLower(it.Title), qLower) ||
			strings.Contains(strings.ToLower(it.Artist), qLower) ||
			strings.Contains(strings.ToLower(it.Album), qLower)
	}
	type node struct {
		id    string
		depth int
	}
	queue := []node{{id: "0"}}
	var out []dlna.Item
	browses := 0
	for len(queue) > 0 && browses < libraryWalkMaxBrowses && len(out) < librarySearchMax {
		if ctx.Err() != nil {
			break
		}
		n := queue[0]
		queue = queue[1:]
		browses++
		res, err := dlna.Browse(ctx, srv, n.id, 0, 100)
		if err != nil {
			continue
		}
		for _, it := range res.Items {
			if matches(it) {
				out = append(out, it)
				if len(out) >= librarySearchMax {
					break
				}
			}
		}
		if n.depth+1 < libraryWalkMaxDepth {
			for _, c := range res.Containers {
				// Folders whose NAME matches are worth descending into even
				// past the breadth budget order (an album named like the
				// query holds the tracks being looked for).
				if strings.Contains(strings.ToLower(c.Title), qLower) {
					queue = append([]node{{id: c.ID, depth: n.depth + 1}}, queue...)
				} else {
					queue = append(queue, node{id: c.ID, depth: n.depth + 1})
				}
			}
		}
	}
	s.logger.Info("library search: bounded walk finished",
		"server", srv.FriendlyName, "browses", browses, "hits", len(out))
	return out
}
