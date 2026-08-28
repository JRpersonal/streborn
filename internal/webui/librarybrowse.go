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
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/internal/boxapi"
)

const (
	// libraryBrowsePage is one Browse page. The same size the desktop Library
	// uses, for the same reason: one bigger SOAP call beats many small ones.
	libraryBrowsePage = 200
	// libraryBrowseTimeout bounds one page fetch, resolution included. A slow
	// NAS answering a first page can take a while (the #666 QNAP let one
	// container time out at 15 s), so this is deliberately looser than the
	// search's per-server share; it is one user tap, not a fan-out. 55s: the
	// resolution chain below can, in its worst case (recall miss, then a fresh
	// discovery, then the box-cache probe) spend recall + browse-discovery +
	// unicast-probe describing a pathologically slow WD Twonky before the
	// Browse SOAP even starts (#733). The common case still returns in a couple
	// of seconds; this only keeps the tap from failing outright on that NAS.
	// 70s, not 55: the chain can now end in a peer round (below), where a box
	// that cannot see the server itself borrows the address from a sibling that
	// can (#726). That peer round only runs after the local paths miss, and for
	// the box it helps those local paths fail fast (empty firmware cache), so
	// the realistic total stays well under this ceiling.
	libraryBrowseTimeout = 70 * time.Second
	// libraryPeerLocateTimeout bounds one query to a peer STR agent's
	// /api/library/locate. Wide enough to let the peer run its own fresh
	// discovery if it has not cached the server yet, since the whole point is
	// that SOME box on the LAN can reach the server even when this one cannot.
	libraryPeerLocateTimeout = 12 * time.Second
	// libraryBrowseDiscovery bounds the fresh SSDP round the browse path runs
	// when recall misses. It is deliberately far looser than the search's
	// shared librarySearchDiscovery (5 s): a browse tap resolves ONE server and
	// the user is waiting, so it can afford to let a slow server's device
	// description actually complete. 15s covers dlna.DiscoverServers' own 12 s
	// per-device fetch plus the SSDP listen window. #733: UlrichSzy's WD Twonky
	// WAS seen by the fresh SSDP round, but the old 5 s budget was too short to
	// describe it, so resolution fell through to the box cache and failed there.
	libraryBrowseDiscovery = 15 * time.Second
	// libraryUnicastProbe bounds the direct unicast M-SEARCH at the address the
	// firmware's discovery cache names (#726). One host, but the answer still
	// carries a device description that a slow WD Twonky serves slowly, so this
	// has to be wide enough to let dlna.SearchHost's own 15 s describe finish
	// (#733); the old 3 s guaranteed a miss on that NAS.
	libraryUnicastProbe = 18 * time.Second
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
//
// The recall shortcut is only a HINT, not a promise. A server that got a new
// DHCP lease can still answer its OLD address's device description (the box's
// own discovery cache, or a lingering second interface, keeps it alive) with a
// matching UDN, so recall "succeeds" and hands back a control URL that the
// Browse SOAP then cannot reach. That regressed the moment the per-fetch
// timeout grew (#739): the stale address, which used to time out and fall
// through to a fresh round, now describes in time and shadows it (#733, #726).
// So the browse handler treats a Browse failure as a signal to re-resolve
// FRESH (recall skipped) and retry once; see handleLibraryBrowse.
func (s *Server) resolveMediaServer(ctx context.Context, udn string) (dlna.Server, bool) {
	key := udnKey(udn)
	if srv, ok := s.recallMediaServer(ctx, key); ok {
		return srv, true
	}
	return s.resolveMediaServerFresh(ctx, udn)
}

// resolveMediaServerFresh resolves a server WITHOUT the recall shortcut: a fresh
// SSDP round, then the box's own discovery cache, then the fleet. It does NOT
// drop the stored last-known location first: on a subnet where the box cannot
// self-discover the server (multicast filtered, firmware cache empty, #726) that
// stored address is the ONLY one the box can reach, and a transient Browse error
// must not destroy it. rememberMediaServerLocations below OVERWRITES the stored
// location when the fresh round actually finds the server at a NEW address (the
// genuinely-moved-server case #749 targeted), so a real move is still corrected
// without wiping the one reachable address when discovery comes back empty.
func (s *Server) resolveMediaServerFresh(ctx context.Context, udn string) (dlna.Server, bool) {
	key := udnKey(udn)
	found, err := dlna.DiscoverServers(ctx, libraryBrowseDiscovery)
	if err != nil {
		s.logger.Info("library resolve: discovery failed", "err", err)
	}
	s.rememberMediaServerLocations(found)
	for _, srv := range found {
		if udnKey(srv.UDN) == key {
			return srv, true
		}
	}
	// Forensic (#733): the fresh round returned servers but none carried the
	// registered UDN. That separates "the slow NAS still could not be described
	// in time" (found is short or empty) from "the server now advertises a
	// different UDN" (found is non-empty without this key) in the next bundle,
	// so a persistent miss points at the real cause instead of a guess.
	if len(found) > 0 {
		s.logger.Info("library resolve: fresh discovery saw servers but none matched the registered UDN",
			"want", key, "discovered", len(found))
		// A registered server whose UDN no longer matches any live one is the
		// UUID-regeneration case: WD/Twonky servers mint a new UPnP UUID on a
		// restart or reconfigure, so a strict UDN resolve can never find them
		// again even though the server is right there and other apps browse it
		// fine (#733, #726). Recover it by its friendly NAME when exactly one
		// discovered server carries the registered name.
		if srv, ok := s.rematchByName(key, found); ok {
			return srv, true
		}
	}
	// The multicast round came back without this server. On networks whose AP
	// or router filters multicast between Wi-Fi and wire, the agent's own
	// M-SEARCH (or its answers) never crosses, while the desktop app on the
	// wire sees the server fine (#726). The firmware keeps its own discovery
	// cache fed by the server's NOTIFY announcements; a unicast search at the
	// address it names passes every multicast filter.
	if srv, ok := s.resolveViaBoxCache(ctx, key, len(found)); ok {
		return srv, true
	}
	// Last resort: ask the other STR agents on the LAN. A box whose own network
	// filters the server's multicast AND whose firmware cache is empty (a
	// SoundTouch Wireless Link Adapter behind a mesh node saw zero servers while
	// its siblings had the same server registered, #726) can still browse it by
	// borrowing the address from a sibling that can reach it, then describing it
	// directly.
	return s.resolveViaPeers(ctx, udn)
}

// resolveViaPeers asks the other STR agents on the LAN where a registered media
// server is, then reaches it directly. This closes the gap where ONE box cannot
// discover a server its siblings can: a fixed-IP NAS is perfectly reachable by
// unicast, the box just never learns its address because the multicast never
// crosses to it (#726). The desktop app is not always running, so the fleet
// itself has to carry the knowledge.
func (s *Server) resolveViaPeers(ctx context.Context, udn string) (dlna.Server, bool) {
	if s.peersFn == nil {
		return dlna.Server{}, false
	}
	key := udnKey(udn)
	peers := s.peersFn(ctx)

	// tryPeer asks one peer where the server is and reaches it directly. ok is
	// true only when it actually described the registered server.
	tryPeer := func(p PeerLink) (dlna.Server, bool) {
		loc, ip := s.askPeerLocate(ctx, p.URL, udn)
		if loc != "" {
			// A peer names one specific device-description URL, so a name match is
			// safe here: it is the address a sibling that CAN see the server just
			// resolved. serverMatchesKey also accepts a UUID-regenerated server by
			// its registered name (#733), the same tolerance recall uses.
			if srv, err := dlna.DescribeServer(ctx, loc); err == nil && srv.CDSControlURL != "" && s.serverMatchesKey(srv, key) {
				s.rememberMediaServerLocationAs(key, srv.Location)
				s.logger.Info("library resolve: found via a peer agent's location", "peer", p.Name)
				return srv, true
			}
		}
		if ip != "" {
			if found, err := dlna.SearchHost(ctx, ip, libraryUnicastProbe); err == nil {
				for _, srv := range found {
					if s.serverMatchesKey(srv, key) {
						s.rememberMediaServerLocationAs(key, srv.Location)
						s.logger.Info("library resolve: found via a peer agent's ip", "peer", p.Name, "ip", ip)
						return srv, true
					}
				}
			}
		}
		return dlna.Server{}, false
	}

	// Ask reachable peers first, then dimmed ones, with SEPARATE budgets. A
	// dimmed peer is still worth asking: the box reaches a sibling on another
	// segment by routed unicast, and /api/library/locate is exactly that unicast
	// GET; the roster dims a sibling only because its mDNS sightings went stale
	// (peerDimAfter), not because it is unreachable. #726: the ST-10 that CAN see
	// the server sat dimmed on the ST-20's roster and the old reachable-only
	// guard skipped it, so the peer-assist never fired. Peers are name-sorted, so
	// one shared cap would let earlier-sorted useless peers starve the one useful
	// dimmed sibling; a per-class cap guarantees the dimmed set its own turn.
	reachTried, dimTried := 0, 0
	for _, p := range peers {
		if p.URL == "" || !p.Reachable {
			continue
		}
		if reachTried >= 3 { // a small fleet answers on the first reachable sibling
			break
		}
		reachTried++
		if srv, ok := tryPeer(p); ok {
			return srv, true
		}
	}
	for _, p := range peers {
		if p.URL == "" || p.Reachable {
			continue
		}
		if dimTried >= 3 {
			break
		}
		dimTried++
		if srv, ok := tryPeer(p); ok {
			return srv, true
		}
	}
	// Silent before (#726: the bundle showed zero peer lines even though this
	// ran, because every peer was dimmed and skipped). Log which branch we hit so
	// the next bundle proves the path taken.
	if reachTried == 0 && dimTried == 0 {
		s.logger.Info("library resolve: no peer agents available to ask for the server", "peers", len(peers))
	} else {
		s.logger.Info("library resolve: asked peers for the server but none could locate it", "reachAsked", reachTried, "dimAsked", dimTried)
	}
	return dlna.Server{}, false
}

// askPeerLocate queries one peer agent's /api/library/locate for where it knows
// the server. Returns the device-description location and/or an IP, empty on any
// failure (a peer that does not know the server, an older build without the
// endpoint, or one briefly unreachable).
func (s *Server) askPeerLocate(ctx context.Context, peerURL, udn string) (string, string) {
	u := strings.TrimRight(peerURL, "/") + "/api/library/locate?udn=" + url.QueryEscape(udn)
	pctx, cancel := context.WithTimeout(ctx, libraryPeerLocateTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, u, nil)
	if err != nil {
		return "", ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ""
	}
	var out libraryLocateResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return "", ""
	}
	return out.Location, out.IP
}

// libraryLocateResult is what /api/library/locate answers a peer with.
type libraryLocateResult struct {
	UDN      string `json:"udn"`
	Location string `json:"location,omitempty"`
	IP       string `json:"ip,omitempty"`
}

// handleLibraryLocate answers a peer STR agent asking where a REGISTERED media
// server is, so a box that cannot discover the server on its own network can
// borrow the address (#726). LAN peers only, registered servers only: this must
// not turn a speaker into a locator for arbitrary UPnP devices. Answers from the
// cached location first, then a bounded fresh discovery, since this box is only
// asked because it might be the one that CAN reach the server.
func (s *Server) handleLibraryLocate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "LAN only", http.StatusForbidden)
		return
	}
	udn := r.URL.Query().Get("udn")
	if udn == "" || s.mediaServers == nil {
		writeJSON(w, http.StatusOK, libraryLocateResult{})
		return
	}
	key := udnKey(udn)
	registered := false
	for _, reg := range s.mediaServers.List() {
		if udnKey(reg.ID) == key {
			registered = true
			break
		}
	}
	if !registered {
		writeJSON(w, http.StatusOK, libraryLocateResult{})
		return
	}
	s.mediaLocMu.Lock()
	loc := s.mediaLoc[key]
	s.mediaLocMu.Unlock()
	if loc != "" {
		writeJSON(w, http.StatusOK, libraryLocateResult{UDN: udn, Location: loc})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), libraryBrowseDiscovery)
	defer cancel()
	found, _ := dlna.DiscoverServers(ctx, libraryBrowseDiscovery)
	s.rememberMediaServerLocations(found)
	for _, srv := range found {
		if udnKey(srv.UDN) == key && srv.Location != "" {
			writeJSON(w, http.StatusOK, libraryLocateResult{UDN: udn, Location: srv.Location})
			return
		}
	}
	writeJSON(w, http.StatusOK, libraryLocateResult{})
}

// resolveViaBoxCache asks the speaker's own /listMediaServers cache where the
// server was last seen and probes that address with a unicast M-SEARCH. Every
// exit logs its reason: this path only runs when the phone page is about to
// show "server not answering", and a silent miss here cannot be told apart
// from a server that is genuinely off (#726).
func (s *Server) resolveViaBoxCache(ctx context.Context, key string, discovered int) (dlna.Server, bool) {
	if s.boxHost == "" {
		return dlna.Server{}, false
	}
	known, err := boxapi.New(s.boxHost).ListMediaServers(ctx)
	if err != nil {
		s.logger.Warn("library resolve: server unresolved and the speaker's own list could not be read",
			"discovered", discovered, "err", err)
		return dlna.Server{}, false
	}
	ip := ""
	for _, m := range known {
		if udnKey(m.ID) == key {
			ip = m.IP
			break
		}
	}
	if ip == "" {
		s.logger.Warn("library resolve: server unresolved, the speaker's own discovery does not see it either",
			"discovered", discovered, "boxKnows", len(known))
		return dlna.Server{}, false
	}
	found, err := dlna.SearchHost(ctx, ip, libraryUnicastProbe)
	if err != nil {
		s.logger.Warn("library resolve: unicast probe failed", "ip", ip, "err", err)
		return dlna.Server{}, false
	}
	s.rememberMediaServerLocations(found)
	for _, srv := range found {
		if udnKey(srv.UDN) == key {
			s.logger.Info("library resolve: server found via the speaker's discovery cache and a unicast probe", "ip", ip)
			return srv, true
		}
	}
	s.logger.Warn("library resolve: unicast probe answered, but not with this server", "ip", ip, "answers", len(found))
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
		// The resolved address may be stale: recall can hand back an OLD address
		// that still answers its device.xml with a matching UDN but whose
		// control URL is dead (a moved server, #733/#726). A Browse failure is
		// the signal that the address is wrong, so re-resolve FRESH (recall
		// skipped) and retry once at the current address before giving up. The
		// fresh round only REPLACES the stored address when it positively finds
		// the server elsewhere, so a fresh miss leaves the reachable address
		// intact (#749 no longer wipes it).
		if fresh, ok2 := s.resolveMediaServerFresh(ctx, udn); ok2 && fresh.CDSControlURL != srv.CDSControlURL {
			s.logger.Info("library browse: retrying at a freshly resolved address",
				"server", fresh.FriendlyName, "wasControl", srv.CDSControlURL, "nowControl", fresh.CDSControlURL)
			if res2, err2 := dlna.Browse(ctx, fresh, id, start, libraryBrowsePage); err2 == nil {
				res, err = res2, nil
			}
		}
	}
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
