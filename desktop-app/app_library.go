package main

// This file was split out of app.go (wave-1 move-only refactor):
// station logo resolution, box status, and DLNA media library browsing.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/stationhost"
	"github.com/JRpersonal/streborn/sticksetup"
)

// ResolveStationLogo returns the best real logo URL for a station, or ""
// when none exists (the frontend then draws a local monogram instead).
//
// It exists because DuckDuckGo's icon service answers HTTP 404 with a
// grey "no icon" placeholder image (a chevron) for hosts it does not
// know. The Wails webview renders that 404 body instead of firing the
// <img> error handler, so a pure-frontend cascade cannot tell a real
// icon from the placeholder and the grey chevron wins. Here, in Go, we
// can read the HTTP status: 200 means a real icon, 404 means placeholder
// (skip it). Results are cached per (favicon, hosts) for the app run, except a
// media-server cover that failed: that miss is kept only for lanArtMissTTL.
// Neither is an answer reached while a lookup got no reply at all: it is kept
// only for logoSoftTTL (see softLogo).
func (a *App) ResolveStationLogo(faviconURL string, brandHost string, hosts []string) string {
	key := faviconURL + "\x1f" + brandHost + "\x1f" + strings.Join(hosts, ",")
	a.logoMu.Lock()
	if a.logoCache == nil {
		a.logoCache = map[string]string{}
	}
	if v, ok := a.logoCache[key]; ok {
		a.logoMu.Unlock()
		return v
	}
	if until, ok := a.lanArtMiss[key]; ok {
		if time.Now().Before(until) {
			a.logoMu.Unlock()
			return ""
		}
		delete(a.lanArtMiss, key)
	}
	if s, ok := a.logoSoft[key]; ok {
		if time.Now().Before(s.until) {
			a.logoMu.Unlock()
			return s.url
		}
		delete(a.logoSoft, key)
	}
	a.logoMu.Unlock()

	// unanswered records that a lookup got no HTTP reply at all (timeout,
	// network down), so the answer below is not the final word on this logo.
	unanswered := false
	probe := func(u string) (int, string) {
		status, ctype := a.headStatusType(u)
		if status == 0 {
			unanswered = true
		}
		return status, ctype
	}

	best := ""
	// 0. A media server's album art on the LAN (upnp:albumArtURI). Always
	//    plain http, so the webview would not draw it: fetched here and
	//    handed back as a small data: URI (lanart.go, #1301).
	if isLANArtURL(faviconURL) {
		best = a.lanArtDataURI(faviconURL)
	}
	// 1. The station's own HTTPS favicon, if it really serves an image.
	//    HTTP favicons are skipped: the secure webview blocks them as
	//    mixed content anyway.
	//
	//    A logo the check could not confirm, for any reason but a definite
	//    "not there" (404, 410, or a page that is not an image), is still
	//    handed to the window to try first, with the fallback found below
	//    behind it (favTry). The check runs in Go and the phone page's runs in
	//    a browser; they do not reach every host alike. On one PC the station's
	//    own logo failed the check on every restart while the phone page drew
	//    it, and the keys wore the stream host's icon instead (#1898).
	favTry := ""
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(faviconURL)), "https://") {
		status, ctype, err := a.headStatusTypeErr(faviconURL)
		if status == 0 {
			unanswered = true
		}
		switch {
		case status == http.StatusOK && strings.HasPrefix(ctype, "image/"):
			best = faviconURL
		case status == http.StatusNotFound || status == http.StatusGone || status == http.StatusOK:
			// Definitely not a logo at this address.
		default:
			favTry = faviconURL
			a.noteUnconfirmedLogo(faviconURL, status, ctype, err)
		}
	}
	// 2. DuckDuckGo per host. 200 = real icon; 404 = grey placeholder, so
	//    only a 200 counts. Never for a shared or dynamic-DNS host, nor an IP
	//    literal (stationhost.Shared): its icon is the provider's, so a
	//    station on a *.ddns.net stream host wore ddns.net's favicon (#1773).
	//    The frontend leaves them out already; this holds for any caller.
	if best == "" {
		for _, h := range hosts {
			h = strings.TrimSpace(h)
			if h == "" || stationhost.Shared(h) {
				continue
			}
			u := "https://icons.duckduckgo.com/ip3/" + h + ".ico"
			if status, _ := probe(u); status == http.StatusOK {
				best = u
				break
			}
		}
	}
	// 3. The brand site's own /favicon.ico at the standard path. DuckDuckGo
	//    often does not know smaller brand domains (e.g. epic-classical.com)
	//    even though they serve a favicon. Only the homepage host is tried,
	//    never a stream CDN, so a shared provider logo cannot leak in. Last
	//    because brand sites can be slow; this runs only for the minority
	//    that the favicon field and DuckDuckGo both missed.
	if best == "" && strings.TrimSpace(brandHost) != "" {
		u := "https://" + strings.TrimSpace(brandHost) + "/favicon.ico"
		if status, ctype := probe(u); status == http.StatusOK && strings.HasPrefix(ctype, "image/") {
			best = u
		}
	}

	// A media server's cover that did not come back is remembered only
	// briefly: one timeout or a server still waking up otherwise left the key
	// on its letter tile until the app restarted (#1301). The short miss keeps
	// tile redraws from refetching a cover that is really broken; the reason
	// is logged once per URL by lanArtDataURI.
	if best == "" && isLANArtURL(faviconURL) {
		a.logoMu.Lock()
		if a.lanArtMiss == nil {
			a.lanArtMiss = map[string]time.Time{}
		}
		a.lanArtMiss[key] = time.Now().Add(lanArtMissTTL)
		a.logoMu.Unlock()
		return best
	}
	if favTry != "" {
		if best != "" {
			best = favTry + "|" + best
		} else {
			best = favTry
		}
	}
	a.logoMu.Lock()
	defer a.logoMu.Unlock()
	if unanswered {
		// A lookup that timed out says nothing about the logo. Caching its
		// outcome for the run left a key on a lower-ranked icon (or none) until
		// the app restarted, while the phone page showed the station's own logo
		// (#1898: the network was slow right after the app started).
		if a.logoSoft == nil {
			a.logoSoft = map[string]softLogo{}
		}
		if _, seen := a.logoSoft[key]; !seen && a.logger != nil {
			a.logger.Debug("station logo: a lookup got no reply, asking again later",
				"favicon", faviconURL, "brand", brandHost, "got", best)
		}
		a.logoSoft[key] = softLogo{url: best, until: time.Now().Add(logoSoftTTL)}
		return best
	}
	a.logoCache[key] = best
	return best
}

// softLogo is a ResolveStationLogo answer that is not final, because one of
// its lookups got no reply. It is reused until until, then looked up again.
type softLogo struct {
	url   string
	until time.Time
}

// logoSoftTTL is how long a softLogo stands. Long enough that the tiles redrawn
// while the network is down do not each wait for the timeouts again, short
// enough that the next redraw after it is back shows the right logo.
const logoSoftTTL = 60 * time.Second

// headStatusType fetches just enough of url to learn the HTTP status and
// Content-Type, with a short timeout so a slow host never stalls logo
// resolution. The body is not read. A GET (not HEAD) is used because
// some icon hosts mishandle HEAD; the response is closed immediately.
func (a *App) headStatusType(url string) (int, string) {
	status, ctype, _ := a.headStatusTypeErr(url)
	return status, ctype
}

// headStatusTypeErr is headStatusType with the error of a request that got no
// HTTP answer, for the log.
func (a *App) headStatusTypeErr(url string) (int, string, error) {
	ctx, cancel := context.WithTimeout(a.appCtx(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", "STReborn-Desktop")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Content-Type"), nil
}

// noteUnconfirmedLogo logs, once per URL and run, why a station's own logo did
// not pass the check, so a bundle says what the window had to try (#1898).
func (a *App) noteUnconfirmedLogo(url string, status int, ctype string, err error) {
	a.logoMu.Lock()
	if a.logoUnconfirmed == nil {
		a.logoUnconfirmed = map[string]bool{}
	}
	seen := a.logoUnconfirmed[url]
	a.logoUnconfirmed[url] = true
	a.logoMu.Unlock()
	if seen || a.logger == nil {
		return
	}
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	a.logger.Info("station logo: the station's own logo did not pass the check, the window tries it first",
		"url", url, "status", status, "contentType", ctype, "err", reason)
}

// EjectDrive ejects the stick so the user can remove it.
func (a *App) EjectDrive(path string) error {
	return sticksetup.Eject(path)
}

// Status returns the now_playing XML as a string. The frontend can
// regex-parse it itself.
func (a *App) Status(host string, port int) (string, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/status", "", "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	return string(b), nil
}

// === Library (DLNA / UPnP MediaServer browsing) ===
//
// MVP scope: discover MediaServers on the LAN, browse one server's
// ContentDirectory tree, play a track via the existing PlayURL path,
// optionally save the track as one of the six STR presets via the
// existing SetPreset path. No queue, no search, no transcoding.
// Audio items only; the frontend filters the rest out.

// LibraryServer is the flat DTO sent to the frontend dropdown.
// Mirrors dlna.Server but trims it to JSON-friendly fields.
type LibraryServer struct {
	UDN          string `json:"udn"`
	FriendlyName string `json:"friendlyName"`
	Manufacturer string `json:"manufacturer"`
	ModelName    string `json:"modelName"`
	IconURL      string `json:"iconURL"`
	Address      string `json:"address"`
	// Manual is true for servers the user added by address
	// (AddMediaServerByURL); the frontend shows a remove control for
	// these.
	Manual bool `json:"manual"`
	// NotAnswering is true for an entry this scan could only take from
	// memory: nothing answered at its address, and the list shows it
	// from the stored snapshot. The #733 reporter enabled such an entry
	// on all three of his speakers while the NAS was switched off, every
	// browse then failed, and nothing in the app had said the server was
	// never seen alive (#770).
	NotAnswering bool `json:"notAnswering"`
	// USBSpeaker names the speaker this server is the music stick of (#1136);
	// empty for every other server. The Library labels it in the user's
	// language and offers no "put on the speakers" for it.
	USBSpeaker string `json:"usbSpeaker,omitempty"`
}

// libraryEntry is one server on its way to the frontend list: the
// described server plus how this scan learned about it.
type libraryEntry struct {
	Server       dlna.Server
	Manual       bool
	NotAnswering bool
}

// mergeLibraryEntries builds the Library list from this scan's live
// sightings and the user's manually pinned servers, deduped by UDN.
//
// A pinned server is listed even when it is dark right now (that is the
// point of pinning it, #341), so it is the one entry that can reach the
// list without anything having answered. It is flagged here so the UI can
// say so; a pinned server that DID answer this scan, whether through its
// own probe or the SSDP sweep, is a live entry that merely keeps its
// remove control.
func mergeLibraryEntries(live []dlna.Server, manual []manualServerProbe) []libraryEntry {
	at := make(map[string]int, len(live)+len(manual))
	out := make([]libraryEntry, 0, len(live)+len(manual))
	for _, s := range live {
		if s.UDN == "" {
			continue
		}
		if _, dup := at[s.UDN]; dup {
			continue
		}
		at[s.UDN] = len(out)
		out = append(out, libraryEntry{Server: s})
	}
	for _, m := range manual {
		if m.Server.UDN == "" {
			continue
		}
		if i, dup := at[m.Server.UDN]; dup {
			out[i].Manual = true
			continue
		}
		at[m.Server.UDN] = len(out)
		out = append(out, libraryEntry{
			Server: m.Server, Manual: true, NotAnswering: !m.Answering,
		})
	}
	return out
}

// LibraryContainer is a folder / album node in the browse view.
type LibraryContainer struct {
	ID         string `json:"id"`
	ParentID   string `json:"parentID"`
	Title      string `json:"title"`
	ChildCount int    `json:"childCount"`
	// AlbumArtURL is the folder's own cover (upnp:albumArtURI). A folder key
	// falls back to it when none of its tracks carries a cover (#1301).
	AlbumArtURL string `json:"albumArtURL"`
	// Class is the folder's upnp:class (a playlist is
	// object.container.playlistContainer).
	Class string `json:"class,omitempty"`
}

// LibraryItem is a single playable track.
type LibraryItem struct {
	ID          string `json:"id"`
	ParentID    string `json:"parentID"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	MimeType    string `json:"mimeType"`
	StreamURL   string `json:"streamURL"`
	AlbumArtURL string `json:"albumArtURL"`
	DurationSec int    `json:"durationSec"`
	// Server names the media server a whole-library search hit came from;
	// empty for a browse, where the open server is plain to see.
	Server string `json:"server,omitempty"`
	// TrackNumber and DiscNumber come from upnp:originalTrackNumber and the
	// server's disc field; 0 when the server sends none. The Library puts an
	// album folder in track order by them (#1487).
	TrackNumber int `json:"trackNumber,omitempty"`
	DiscNumber  int `json:"discNumber,omitempty"`
}

// LibraryPage is one page of a browse call.
type LibraryPage struct {
	Containers   []LibraryContainer `json:"containers"`
	Items        []LibraryItem      `json:"items"`
	TotalMatches int                `json:"totalMatches"`
	Returned     int                `json:"returned"`
	// ContainerClass is the open folder's own upnp:class. It is only looked up
	// on the first page of a folder that spans several pages: a single-page
	// folder already comes back in album track order from here, while a longer
	// one can only be ordered once the frontend holds every page, and the
	// frontend must not re-order a playlist (#1487). Empty when not looked up
	// or when the server did not say.
	ContainerClass string `json:"containerClass,omitempty"`
}

// ListMediaServers finds the DLNA MediaServers the Library tab can
// browse. Four sources run in parallel and are merged, deduped by UDN:
// the SSDP M-SEARCH sweep, the servers remembered from earlier scans
// (known-servers.json, which removes the cold-start blind window where
// a same-PC server was invisible until its next periodic NOTIFY,
// #341), the well-known description endpoints probed against this
// host's own addresses, and the user's manually added servers. Result
// is cached so BrowseLibrary can look up the server by UDN without
// rediscovering.
//
// Each entry carries whether anything answered for it in THIS scan
// (NotAnswering), because only the first three sources prove liveness
// and the list has to be able to say which entries it is showing from
// memory (#770).
func (a *App) ListMediaServers(timeoutSec int) ([]LibraryServer, error) {
	// The M-SEARCH goes out with MX=3: a compliant server may wait up
	// to 3 s before answering, so the previous 3 s window regularly cut
	// off stragglers (slow NAS boxes). Enforce at least 5 s; the direct
	// probes below finish well inside that budget.
	if timeoutSec < 5 {
		timeoutSec = 5
	}
	var (
		wg      sync.WaitGroup
		ssdp    []dlna.Server
		ssdpErr error
		known   []dlna.Server
		local   []dlna.Server
		sticks  []usbStick
		notices []LibraryUSBNotice
	)
	wg.Add(4)
	go func() {
		defer wg.Done()
		ssdp, ssdpErr = dlna.DiscoverServers(a.appCtx(), time.Duration(timeoutSec)*time.Second)
	}()
	go func() {
		defer wg.Done()
		known = a.probeKnownServers(a.appCtx())
	}()
	go func() {
		defer wg.Done()
		local = a.probeWellKnownLocalServers(a.appCtx())
	}()
	// The music sticks in the speakers: no SSDP, each agent lists its own.
	go func() {
		defer wg.Done()
		sticks, notices = a.speakerUSBSticks(a.appCtx())
	}()
	wg.Wait()

	seen := map[string]bool{}
	var servers []dlna.Server
	add := func(list []dlna.Server) {
		for _, s := range list {
			if s.UDN == "" || seen[s.UDN] {
				continue
			}
			seen[s.UDN] = true
			servers = append(servers, s)
		}
	}
	add(ssdp)
	add(known)
	add(local)
	if ssdpErr != nil {
		// The sweep failing outright (no usable sockets) must not blank
		// the list when the direct probes still answered.
		if len(servers) == 0 {
			return nil, ssdpErr
		}
		a.logger.Warn("library: SSDP sweep failed, listing direct-probe results only", "err", ssdpErr.Error())
	}
	// Everything above was successfully described this scan: remember it
	// so the NEXT scan (or the next app start) finds it without waiting
	// for an announcement.
	a.rememberKnownServers(servers)

	// Merge in the user's manually added servers (#341), which is also
	// where an entry can enter the list without having answered.
	entries := mergeLibraryEntries(servers, a.refreshedManualServers())
	dark := 0
	for _, e := range entries {
		if e.NotAnswering {
			dark++
		}
	}
	// A stick is listed after the network's servers and never remembered.
	usbAt := map[string]string{}
	for _, st := range sticks {
		if _, dup := usbAt[st.Server.UDN]; dup {
			continue
		}
		usbAt[st.Server.UDN] = st.Speaker
		entries = append(entries, libraryEntry{Server: st.Server})
	}
	a.logger.Info("library: media server scan done",
		"ssdp", len(ssdp), "known", len(known), "local", len(local),
		"usbSticks", len(usbAt), "usbRefused", len(notices),
		"total", len(entries), "notAnswering", dark)

	a.libraryMu.Lock()
	a.libraryServers = map[string]dlna.Server{}
	for _, e := range entries {
		a.libraryServers[e.Server.UDN] = e.Server
	}
	a.usbNotices = notices
	a.libraryMu.Unlock()

	out := make([]LibraryServer, 0, len(entries))
	for _, e := range entries {
		out = append(out, LibraryServer{
			UDN:          e.Server.UDN,
			FriendlyName: e.Server.FriendlyName,
			Manufacturer: e.Server.Manufacturer,
			ModelName:    e.Server.ModelName,
			IconURL:      e.Server.IconURL,
			Address:      e.Server.Address,
			Manual:       e.Manual,
			NotAnswering: e.NotAnswering,
			USBSpeaker:   usbAt[e.Server.UDN],
		})
	}
	return out, nil
}

// LibraryRefusal is a media server that answered this PC's device
// description request with a SOAP fault instead of the description, so it
// cannot be listed. The Library names it rather than leaving the user to
// wonder why a server the speaker sees is missing here.
type LibraryRefusal struct {
	Address string `json:"address"`
	Detail  string `json:"detail"`
}

// LibraryRefusedServers lists the media servers that refused to describe
// themselves to this PC during the recent scans. Field case (QNAP NAS): the
// NAS media server served its description to the speaker but answered every
// fetch from the PC with a SOAP envelope, most likely because the PC was not
// on the server's list of allowed media receivers. The only QNAP entry left in
// the Library was then the NAS's admin device, which cannot be browsed.
func (a *App) LibraryRefusedServers() []LibraryRefusal {
	refs := dlna.RecentRefusals()
	out := make([]LibraryRefusal, 0, len(refs))
	for _, r := range refs {
		out = append(out, LibraryRefusal{Address: r.Address, Detail: r.Detail})
	}
	return out
}

// BrowseLibrary returns one page of children under objectID on the
// server identified by udn. objectID "0" or empty is the server root.
// Items that are not audio are filtered out so the Library tab only
// shows things the SoundTouch can actually play.
func (a *App) BrowseLibrary(udn, objectID string, start, count int) (LibraryPage, error) {
	a.libraryMu.Lock()
	srv, ok := a.libraryServers[udn]
	a.libraryMu.Unlock()
	if !ok {
		return LibraryPage{}, fmt.Errorf("unknown media server %q, call ListMediaServers first", udn)
	}
	ctx, cancel := context.WithTimeout(a.appCtx(), 12*time.Second)
	defer cancel()
	res, err := dlna.Browse(ctx, srv, objectID, start, count)
	if err != nil {
		return LibraryPage{}, err
	}
	// An album the server lists alphabetically comes back in track order
	// (#1487); dlna.OrderBrowsePage has the rule and leaves playlists alone.
	res = dlna.OrderBrowsePage(ctx, srv, objectID, start, res)
	page := libraryPageFrom(res)
	if start == 0 && !res.Complete(start) && len(page.Items) > 0 {
		if class, err := dlna.ContainerClass(ctx, srv, objectID); err == nil {
			page.ContainerClass = class
		}
	}
	return page, nil
}

// LibraryFolder describes one folder on the server identified by udn, as the
// server itself names it: title, class and cover, without its children. The
// UI 2.0 player leads to the folder a queue or a track came from by its id
// alone, and when no trail walked this session passes through it, a heading
// guessed from the playing track named a genre folder or a playlist after one
// album, and A to Z sorted a playlist it could not tell from a plain folder.
func (a *App) LibraryFolder(udn, objectID string) (LibraryContainer, error) {
	a.libraryMu.Lock()
	srv, ok := a.libraryServers[udn]
	a.libraryMu.Unlock()
	if !ok {
		return LibraryContainer{}, fmt.Errorf("unknown media server %q, call ListMediaServers first", udn)
	}
	if objectID == "" || objectID == "0" {
		return LibraryContainer{}, fmt.Errorf("browse metadata: the server root is no folder")
	}
	ctx, cancel := context.WithTimeout(a.appCtx(), 8*time.Second)
	defer cancel()
	c, err := dlna.ContainerMetadata(ctx, srv, objectID)
	if err != nil {
		return LibraryContainer{}, err
	}
	return LibraryContainer{
		ID: c.ID, ParentID: c.ParentID, Title: c.Title,
		ChildCount: c.ChildCount, AlbumArtURL: c.AlbumArtURL, Class: c.Class,
	}, nil
}

// LibrarySearchResult is one whole-library search across every media server
// the app knows: the merged tracks, plus the servers that could not answer.
type LibrarySearchResult struct {
	Items []LibraryItem `json:"items"`
	// Searched is how many servers were asked.
	Searched int `json:"searched"`
	// Unanswered names the servers that could not be searched at all: not even
	// the root of their library could be read in time. A server whose Search
	// action is missing or broken is NOT in here; it is walked instead (#1705).
	Unanswered []string `json:"unanswered,omitempty"`
	// Incomplete names the servers whose folders had to be walked (they cannot
	// search themselves) and whose walk could not cover the whole library in
	// the time and reads a search has. Their hits are in Items; more of their
	// library may match.
	Incomplete []string `json:"incomplete,omitempty"`
	// Partial is true when the list is not the whole answer: a server had more
	// hits than it handed over (a more exact search helps), or a server is in
	// Incomplete. The view tells the two apart by Incomplete (#1705: a walk cut
	// short used to be explained as a server with too many hits).
	Partial bool `json:"partial"`
}

const (
	// librarySearchPerServer caps one server's whole search, Search action and
	// browse walk together. A NAS that is asleep or switched off must not hold
	// the other servers' hits back. It was 12 s while only the Search action
	// ran; the walk for servers that refuse Search needs room of its own (the
	// #1705 reporter's speaker walked the same NAS in 2 to 16 s, inside the
	// phone page's 15 s), so it is 20 s now, with the Search action held to
	// its old 12 s inside it. A walk the deadline cuts short is marked Partial.
	librarySearchPerServer = 20 * time.Second
	// librarySearchMaxPerServer caps the hits asked from one server; a longer
	// list is a query to narrow, not a page to scroll.
	librarySearchMaxPerServer = 200
	// librarySearchMaxReadErrors caps how many unreadable containers one
	// server's log line names.
	librarySearchMaxReadErrors = 5
	// librarySearchMaxBrowses is the desktop's browse budget for one server's
	// walk. The shared default (dlna.WalkMaxBrowses, 60) was tuned on the
	// speaker's CPU; the #1705 reporter's Synology used it up in under 3 s of
	// its 20 s in every search of 2026-11-22, so every answer came back marked
	// incomplete with most of the time unused. The PC has the CPU for more, and
	// the per-server deadline still bounds the walk.
	librarySearchMaxBrowses = 250
)

// librarySearchActionBudget is the Search action's share (both attempts): a
// Search that hangs still leaves the walk at least 8 s. A var for the tests.
var librarySearchActionBudget = 12 * time.Second

// The ways a server's hits can have come back, as logged per server.
const (
	searchPathSearch    = "search"
	searchPathTitleOnly = "title-only"
	searchPathWalk      = "walk"
)

// serverSearch is one server's answer to a whole-library search, and how it
// was reached: the per-server log line is built from it, so a diagnostic bundle
// shows why a server came back empty or unanswered (#1705 had nothing to go on).
type serverSearch struct {
	Items []dlna.Item
	// Truncated is a server that had more hits than it handed over; Incomplete
	// a walk that could not cover the whole library. Either makes the merged
	// answer partial, and each has its own note.
	Truncated, Incomplete bool
	// Path is searchPathSearch, searchPathTitleOnly or searchPathWalk.
	Path string
	// SearchErr and TitleOnlyErr are why the Search action failed, when it did.
	SearchErr, TitleOnlyErr error
	// Browses and ReadErrors describe the walk, when one ran.
	Browses    int
	ReadErrors []string
	// Shortcut, ShortcutComplete and ShortcutErr are the list a walk read
	// ahead of the tree (dlna.WalkResult); Slowest and SlowestContainer its
	// slowest single read, so the next bundle shows where a slow server stalls.
	Shortcut         string
	ShortcutComplete bool
	ShortcutErr      error
	Slowest          time.Duration
	SlowestContainer string
	// Err is set when the server could not be searched at all (the walk could
	// not read its root). Only then is the server unanswered.
	Err error
}

// librarySearchFn is the per-server search, swapped out in tests.
var librarySearchFn = librarySearchOne

// librarySearchOne asks one server the same way the phone page's search does
// (internal/webui/librarysearch.go): the Search action first; for a search
// that includes the title, one title-only retry when the widened criteria
// faults or answers nothing (servers that index titles only reject it with
// UPnPError 708, and the FRITZ!Box index answers some queries with zero for a
// track it holds); and when that still brings nothing, the bounded browse walk
// of dlna.WalkSearch. Synology answers Search with UPnPError 501, and Windows
// Media Player's share does not answer it at all (it is walked without being
// asked), so without the walk the app found nothing on them and called them
// unanswered (#1705).
func librarySearchOne(ctx context.Context, srv dlna.Server, query, field string) serverSearch {
	field = librarySearchField(field)
	titleRetry := field == dlna.SearchAny || field == dlna.SearchTitle
	var out serverSearch

	// A Windows Media Player share is never asked: its Search action hangs
	// and keeps the share busy for the folder reads after it (dlna
	// SkipsSearchAction). Learning that per run, as searchHungServers does,
	// cost the first search after every app start that PC (#1705).
	if dlna.SkipsSearchAction(srv) {
		out.SearchErr = errSearchSkipped
		return librarySearchWalk(ctx, srv, query, field, out)
	}
	// A server whose Search action hung before is walked straight away: it
	// would hang again and leave the walk no time (searchHungServers).
	if searchHungBefore(srv) {
		out.SearchErr = errSearchHungBefore
		return librarySearchWalk(ctx, srv, query, field, out)
	}

	actx, acancel := context.WithTimeout(ctx, librarySearchActionBudget)
	res, err := dlna.SearchBy(actx, srv, query, field, librarySearchMaxPerServer)
	path := searchPathSearch
	if err != nil && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		// The action's own budget ran out with no answer, not a refusal: the
		// server is still busy with the query, so a title-only retry would
		// only queue behind it.
		out.SearchErr = err
		noteSearchHung(srv)
		acancel()
		return librarySearchWalk(ctx, srv, query, field, out)
	}
	if err != nil {
		out.SearchErr = err
		if titleRetry {
			if narrow, nerr := dlna.SearchTitleOnly(actx, srv, query, librarySearchMaxPerServer); nerr == nil {
				res, err, path = narrow, nil, searchPathTitleOnly
			} else {
				out.TitleOnlyErr = nerr
			}
		}
	} else if len(res.Items) == 0 && titleRetry {
		if narrow, nerr := dlna.SearchTitleOnly(actx, srv, query, librarySearchMaxPerServer); nerr == nil && len(narrow.Items) > 0 {
			res, path = narrow, searchPathTitleOnly
		}
	}
	acancel()
	if err == nil && len(res.Items) > 0 {
		out.Items, out.Path = res.Items, path
		// dlna pages a server that caps its answers and corrects a count that
		// is not one; what HasMore still reports is hits really left behind.
		out.Truncated = res.HasMore()
		return out
	}

	return librarySearchWalk(ctx, srv, query, field, out)
}

// errSearchHungBefore is the SearchErr of a server walked without asking,
// because its Search action hung earlier in this session.
var errSearchHungBefore = errors.New("not asked: its Search action hung earlier in this session, walking its folders instead")

// errSearchSkipped is the SearchErr of a Windows Media Player share, which is
// walked without asking.
var errSearchSkipped = errors.New("not asked: Windows Media Player sharing does not answer Search, walking its folders instead")

// searchHungServers remembers, for this run of the app, the servers whose
// Search action ran out of time without any answer. Windows Media Player's
// share did that on the #1705 reporter's PC (2026-11-21): it never answered
// the query, the first folder read of the walk then queued behind it and timed
// out too, and the PC was reported as not answering although browsing it
// worked. Such a server is walked straight away from then on. Kept in memory
// only: a restart of the app, or of the server, asks again.
var searchHungServers sync.Map

func searchServerKey(srv dlna.Server) string {
	if srv.UDN != "" {
		return srv.UDN
	}
	return srv.CDSControlURL
}

func searchHungBefore(srv dlna.Server) bool {
	_, ok := searchHungServers.Load(searchServerKey(srv))
	return ok
}

func noteSearchHung(srv dlna.Server) { searchHungServers.Store(searchServerKey(srv), true) }

// librarySearchWalk is the bounded browse walk of dlna.WalkSearch, for a
// server whose Search action failed, found nothing or is not asked. Its root
// answering is what tells a server that refuses Search from one that is not
// there at all.
func librarySearchWalk(ctx context.Context, srv dlna.Server, query, field string, out serverSearch) serverSearch {
	walk := dlna.WalkSearch(ctx, srv, query, field, dlna.WalkOptions{
		MaxHits:    librarySearchMaxPerServer,
		MaxBrowses: librarySearchMaxBrowses,
		OnReadError: func(container string, start int, err error) {
			if len(out.ReadErrors) < librarySearchMaxReadErrors {
				out.ReadErrors = append(out.ReadErrors, fmt.Sprintf("%s@%d: %v", container, start, err))
			}
		},
	})
	out.Path, out.Items, out.Browses = searchPathWalk, walk.Items, walk.Browses
	out.Shortcut, out.ShortcutComplete, out.ShortcutErr = walk.Shortcut, walk.ShortcutComplete, walk.ShortcutErr
	out.Slowest, out.SlowestContainer = walk.SlowestBrowse, walk.SlowestContainer
	// A walk that stopped at the hit cap left more hits behind: a more exact
	// search helps. Any other cut is a library the walk could not cover.
	out.Truncated = len(walk.Items) >= librarySearchMaxPerServer
	out.Incomplete = walk.Partial && !out.Truncated
	out.Err = walk.RootErr
	return out
}

// librarySearchField reads the field a search is narrowed to (#1325): album,
// artist or title; anything else is any, the title-artist-album match.
func librarySearchField(raw string) string {
	switch f := strings.ToLower(strings.TrimSpace(raw)); f {
	case dlna.SearchAlbum, dlna.SearchArtist, dlna.SearchTitle:
		return f
	}
	return dlna.SearchAny
}

// SearchLibrary searches the WHOLE library of EVERY media server the app
// knows, not only the folder that is open (#1325), for tracks whose field
// contains query. field is any, title, artist or album (dlna.SearchAny and
// friends). The servers are asked in parallel, each with its own timeout, and
// the hits come back merged, each naming its server.
func (a *App) SearchLibrary(query, field string) (LibrarySearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return LibrarySearchResult{}, nil
	}
	a.libraryMu.Lock()
	servers := make([]dlna.Server, 0, len(a.libraryServers))
	for _, s := range a.libraryServers {
		servers = append(servers, s)
	}
	a.libraryMu.Unlock()
	if len(servers) == 0 {
		return LibrarySearchResult{}, fmt.Errorf("no media server known, call ListMediaServers first")
	}
	out := searchServers(a.appCtx(), a.logger, servers, query, field)
	if a.logger != nil {
		// The server lists are joined with ", " rather than logged as a slice:
		// a Windows Media Player name holds spaces ("<PC>: <account>:"), and
		// the bundle's anonymiser can only mask each name when it can tell
		// where one ends.
		a.logger.Info("library search", "field", field, "servers", out.Searched,
			"hits", len(out.Items), "unanswered", strings.Join(out.Unanswered, ", "),
			"incomplete", strings.Join(out.Incomplete, ", "), "partial", out.Partial)
	}
	return out, nil
}

// searchServers runs the per-server searches in parallel and merges them in a
// stable order: by server name, then in the order each server answered. Each
// server's outcome gets its own log line when log is set.
func searchServers(parent context.Context, log *slog.Logger, servers []dlna.Server, query, field string) LibrarySearchResult {
	type answer struct {
		name  string
		items []LibraryItem
		res   serverSearch
	}
	answers := make([]answer, len(servers))
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Add(1)
		go func(i int, srv dlna.Server) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parent, librarySearchPerServer)
			defer cancel()
			name := srv.FriendlyName
			if name == "" {
				name = srv.UDN
			}
			began := time.Now()
			res := librarySearchFn(ctx, srv, query, field)
			answers[i] = answer{name: name, res: res}
			if res.Err == nil {
				// One track a server hands out twice (two views, two URLs) is
				// one row; the same song on two servers stays two (#1705).
				for _, it := range libraryPageFrom(dlna.BrowseResult{Items: dlna.DedupTracks(res.Items)}).Items {
					it.Server = name
					answers[i].items = append(answers[i].items, it)
				}
			}
			logServerSearch(log, name, srv, res, len(answers[i].items), time.Since(began))
		}(i, srv)
	}
	wg.Wait()
	sort.SliceStable(answers, func(i, j int) bool {
		return strings.ToLower(answers[i].name) < strings.ToLower(answers[j].name)
	})
	out := LibrarySearchResult{Searched: len(servers), Items: []LibraryItem{}}
	for _, a := range answers {
		if a.res.Err != nil {
			out.Unanswered = append(out.Unanswered, a.name)
			continue
		}
		if a.res.Incomplete {
			out.Incomplete = append(out.Incomplete, a.name)
		}
		out.Partial = out.Partial || a.res.Truncated || a.res.Incomplete
		out.Items = append(out.Items, a.items...)
	}
	return out
}

// logServerSearch writes one server's outcome: which path answered, how many
// hits, and why the Search action failed when it did. Before #1705 a server
// that refused Search left nothing in the log but its name under "unanswered".
func logServerSearch(log *slog.Logger, name string, srv dlna.Server, res serverSearch, hits int, took time.Duration) {
	if log == nil {
		return
	}
	args := []any{"server", name, "address", srv.Address, "path", res.Path, "hits", hits,
		"truncated", res.Truncated, "incomplete", res.Incomplete, "ms", took.Milliseconds()}
	if res.SearchErr != nil {
		args = append(args, "searchErr", res.SearchErr.Error())
	}
	if res.TitleOnlyErr != nil {
		args = append(args, "titleOnlyErr", res.TitleOnlyErr.Error())
	}
	if res.Path == searchPathWalk {
		args = append(args, "browses", res.Browses,
			"slowestMs", res.Slowest.Milliseconds(), "slowestContainer", res.SlowestContainer)
		if res.Shortcut != "" {
			args = append(args, "shortcut", res.Shortcut, "shortcutComplete", res.ShortcutComplete)
		}
		if res.ShortcutErr != nil {
			args = append(args, "shortcutErr", res.ShortcutErr.Error())
		}
	}
	if len(res.ReadErrors) > 0 {
		args = append(args, "readErrors", res.ReadErrors)
	}
	if res.Err != nil {
		log.Warn("library search: server could not be searched", append(args, "err", res.Err.Error())...)
		return
	}
	log.Info("library search: server answered", args...)
}

// libraryPageFrom turns a browse or search answer into the page the Library
// shows, without the items the SoundTouch cannot play.
func libraryPageFrom(res dlna.BrowseResult) LibraryPage {
	page := LibraryPage{
		TotalMatches: res.TotalMatches,
		Returned:     res.Returned,
	}
	for _, c := range res.Containers {
		page.Containers = append(page.Containers, LibraryContainer{
			ID: c.ID, ParentID: c.ParentID, Title: c.Title,
			ChildCount: c.ChildCount, AlbumArtURL: c.AlbumArtURL,
			Class: c.Class,
		})
	}
	for _, it := range res.Items {
		if !it.IsAudioItem() {
			continue
		}
		page.Items = append(page.Items, LibraryItem{
			ID: it.ID, ParentID: it.ParentID, Title: it.Title,
			Artist: it.Artist, Album: it.Album, MimeType: it.MimeType,
			StreamURL: it.StreamURL, AlbumArtURL: it.AlbumArtURL,
			DurationSec: it.DurationSec,
			TrackNumber: it.TrackNumber, DiscNumber: it.DiscNumber,
		})
	}
	return page
}

// TrackDelivery is the answer to "did the media server hand over the file".
//
// Three states, not two, and the third one is the point: a probe that could not
// be taken says nothing about the server, and must not be reported as one that
// failed. See ProbeTrackDelivery.
type TrackDelivery struct {
	// Delivered is true when bytes actually came back.
	Delivered bool `json:"delivered"`
	// Known is false when the probe itself could not be taken (no URL, the
	// request never got an answer). The caller then keeps whatever it would
	// have said without a probe.
	Known  bool   `json:"known"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// ProbeTrackDelivery asks the media server for the first bytes of a track the
// speaker could not play. It is the one measurement that separates a file the
// speaker cannot decode from a server that is not serving.
//
// It lives in Go, and that is the whole reason this exists rather than a fetch()
// in the view. The Wails page is served from its own origin and a DLNA server
// sends no Access-Control-Allow-Origin, so a fetch from the frontend is refused
// before the app can read the answer, however healthy the server is. Measured on
// 2026-09-11 against two servers on one LAN: both answered 206 with the
// requested range and carried no access-control header at all. A probe that
// always fails is worse than no probe, because it turns into a confident wrong
// diagnosis.
//
// A range request rather than a full GET, so a 40 MB file is not pulled to
// answer a yes/no question. A server that does not do ranges answers 200 with
// the whole body, which still proves it serves the file, and one that refuses
// the range with 416 is also serving; both count as delivered.
func (a *App) ProbeTrackDelivery(url string) TrackDelivery {
	if strings.TrimSpace(url) == "" {
		return TrackDelivery{Known: false, Detail: "no url"}
	}
	ctx, cancel := context.WithTimeout(a.appCtx(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return TrackDelivery{Known: false, Detail: "bad url"}
	}
	req.Header.Set("User-Agent", "STReborn-Desktop")
	req.Header.Set("Range", "bytes=0-2047")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		// The app could not reach the server. It may still be reachable from the
		// speaker, which sits on the same network with different rules, so this
		// is unknown rather than a verdict against the server.
		return TrackDelivery{Known: false, Detail: shortErr(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return TrackDelivery{Delivered: true, Known: true, Status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TrackDelivery{Delivered: false, Known: true, Status: resp.StatusCode}
	}
	n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
	return TrackDelivery{Delivered: n > 0, Known: true, Status: resp.StatusCode}
}

// StreamURLKind classifies a pasted URL so the search view can refuse a
// website before it becomes a preset that can never play.
//
// Field case (2026-08-02): a user pasted station HOMEPAGE addresses
// (www.radiohamburg.de/) into the search box, saved the resulting
// play-this-URL card to three hardware keys, and then reported "presets do
// not work". Nothing downstream could tell him why, because a homepage
// answers 200 and only the Content-Type reveals that it is HTML, not audio.
type StreamURLKind struct {
	// Kind is "stream" (playable audio), "playlist" (m3u/pls, the agent
	// resolves it), "website" (HTML - the station's page, not its stream),
	// or "unknown" (unreachable or no usable Content-Type; never blocks).
	Kind        string `json:"kind"`
	ContentType string `json:"contentType"`
	Status      int    `json:"status"`
}

// ClassifyStreamURL fetches just the headers of url and reports whether it
// looks like audio, a playlist, or a website. Deliberately permissive: any
// failure returns "unknown" so an offline station or an odd server never
// stops the user from saving a URL that actually works.
func (a *App) ClassifyStreamURL(url string) StreamURLKind {
	status, ctype := a.headStatusType(url)
	out := StreamURLKind{Kind: "unknown", ContentType: ctype, Status: status}
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(ctype, ";", 2)[0]))
	switch {
	case ct == "":
		// No Content-Type at all: many SHOUTcast servers answer this way.
		// Treat as unknown, never as a website.
	case strings.HasPrefix(ct, "audio/"), ct == "application/ogg", ct == "application/octet-stream":
		out.Kind = "stream"
	case ct == "application/vnd.apple.mpegurl", ct == "application/x-mpegurl",
		ct == "audio/x-mpegurl", ct == "audio/x-scpls", ct == "application/pls+xml":
		out.Kind = "playlist"
	case strings.HasPrefix(ct, "text/html"), strings.HasPrefix(ct, "application/xhtml"):
		out.Kind = "website"
	}
	return out
}
