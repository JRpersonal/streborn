package main

// This file was split out of app.go (wave-1 move-only refactor):
// station logo resolution, box status, and DLNA media library browsing.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/dlna"
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
// (skip it). Results are cached per (favicon, hosts) for the app run.
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
	a.logoMu.Unlock()

	best := ""
	// 1. The station's own HTTPS favicon, if it really serves an image.
	//    HTTP favicons are skipped: the secure webview blocks them as
	//    mixed content anyway.
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(faviconURL)), "https://") {
		if status, ctype := a.headStatusType(faviconURL); status == http.StatusOK && strings.HasPrefix(ctype, "image/") {
			best = faviconURL
		}
	}
	// 2. DuckDuckGo per host. 200 = real icon; 404 = grey placeholder, so
	//    only a 200 counts.
	if best == "" {
		for _, h := range hosts {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			u := "https://icons.duckduckgo.com/ip3/" + h + ".ico"
			if status, _ := a.headStatusType(u); status == http.StatusOK {
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
		if status, ctype := a.headStatusType(u); status == http.StatusOK && strings.HasPrefix(ctype, "image/") {
			best = u
		}
	}

	a.logoMu.Lock()
	a.logoCache[key] = best
	a.logoMu.Unlock()
	return best
}

// headStatusType fetches just enough of url to learn the HTTP status and
// Content-Type, with a short timeout so a slow host never stalls logo
// resolution. The body is not read. A GET (not HEAD) is used because
// some icon hosts mishandle HEAD; the response is closed immediately.
func (a *App) headStatusType(url string) (int, string) {
	ctx, cancel := context.WithTimeout(a.appCtx(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, ""
	}
	req.Header.Set("User-Agent", "STReborn-Desktop")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return 0, ""
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Content-Type")
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
}

// LibraryPage is one page of a browse call.
type LibraryPage struct {
	Containers   []LibraryContainer `json:"containers"`
	Items        []LibraryItem      `json:"items"`
	TotalMatches int                `json:"totalMatches"`
	Returned     int                `json:"returned"`
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
	)
	wg.Add(3)
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
	a.logger.Info("library: media server scan done",
		"ssdp", len(ssdp), "known", len(known), "local", len(local),
		"total", len(entries), "notAnswering", dark)

	a.libraryMu.Lock()
	a.libraryServers = map[string]dlna.Server{}
	for _, e := range entries {
		a.libraryServers[e.Server.UDN] = e.Server
	}
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
		})
	}
	return out, nil
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
	page := LibraryPage{
		TotalMatches: res.TotalMatches,
		Returned:     res.Returned,
	}
	for _, c := range res.Containers {
		page.Containers = append(page.Containers, LibraryContainer{
			ID: c.ID, ParentID: c.ParentID, Title: c.Title,
			ChildCount: c.ChildCount,
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
		})
	}
	return page, nil
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
