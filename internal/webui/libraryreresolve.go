// A library key must survive the media server renumbering its file URLs.
//
// A key saved from a media server stores the file URL the server handed out at
// save time. Some servers build that URL around ids that only live as long as
// the server's session: Windows' own media sharing (WMPNSSv4) answers
// "http://<pc>:10243/WMPNSSv4/<n>/<id>.mp3", and after the PC restarts the same
// track has another address. The key then points at nothing while the music has
// not moved, and re-saving the key was the only cure (#917, tracked as #919).
// The host heal (librarykeyheal.go, queuepresetheal.go) cannot help: the host
// is the same, only the path changed.
//
// So a library key also stores what the track IS on the server: the server's
// UDN and the item's ContentDirectory object id (presets.TrackRef), and a folder
// key its container id (presets.FolderRef) with each track's object id. On a
// press whose stored URL no longer serves, the server is asked once for the
// item's current URL (Browse, BrowseMetadata on the object id), or, when the
// object id is gone too, the item is found by title in the folder it was saved
// from. The corrected key is persisted, so the next press is a straight play.
//
// Deliberately narrow, as #919 asks:
//   - only a URL that has actually failed is re-resolved, once, with a short
//     budget; a key whose URL serves costs one ranged GET and nothing else;
//   - never a library crawl: one metadata call, at most one folder;
//   - keys saved before this (no ids) keep exactly their old behaviour.

package webui

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/internal/presets"
)

const (
	// libraryURLProbeTimeout bounds the ranged GET that asks whether a key's
	// stored file URL still serves. A LAN server that is there answers in
	// milliseconds.
	libraryURLProbeTimeout = 2 * time.Second
	// libraryReresolveTimeout bounds locating the server and asking it for a
	// single track's current URL. A key press is waiting on it.
	libraryReresolveTimeout = 5 * time.Second
	// libraryFolderReresolveTimeout bounds the same for a folder key, which
	// reads its one folder again.
	libraryFolderReresolveTimeout = 10 * time.Second
)

// libraryFileServes reports whether a library file URL still delivers. One
// ranged GET for the first byte; the body is never read. A 404, a 410, a
// server error or no answer at all count as dead. Anything else (a 2xx, a
// redirect, a server that refuses the range or the method) is taken as alive:
// it is not the renumbered-URL case, and a re-resolve would not change it.
func (s *Server) libraryFileServes(ctx context.Context, rawURL string) bool {
	if s.libraryURLServes != nil {
		return s.libraryURLServes(ctx, rawURL)
	}
	pctx, cancel := context.WithTimeout(ctx, libraryURLProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return libraryURLStatusServes(resp.StatusCode)
}

// libraryURLStatusServes is libraryFileServes' verdict on a status code.
func libraryURLStatusServes(code int) bool {
	switch {
	case code == http.StatusNotFound, code == http.StatusGone:
		return false
	case code >= 500:
		return false
	}
	return true
}

// libraryResolveServer finds the media server a key names: by the UDN the key
// recorded, else by the registered server carrying the key's server name (a
// server that minted a new UDN, #733's rule).
func (s *Server) libraryResolveServer(ctx context.Context, udn, source string) (dlna.Server, bool) {
	resolve := func(id string) (dlna.Server, bool) {
		if s.libraryServerResolve != nil {
			return s.libraryServerResolve(ctx, id)
		}
		srv, _, ok := s.resolveMediaServer(ctx, id)
		return srv, ok
	}
	if udnKey(udn) != "" {
		if srv, ok := resolve(udn); ok {
			return srv, true
		}
	}
	if s.mediaServers == nil || strings.TrimSpace(source) == "" {
		return dlna.Server{}, false
	}
	reg, ok := matchRegisteredServer(s.mediaServers.List(), source)
	if !ok || udnKey(reg.ID) == udnKey(udn) {
		return dlna.Server{}, false
	}
	return resolve(reg.ID)
}

// libraryTitleKey folds a title for matching: case and surrounding space do
// not tell two tracks apart.
func libraryTitleKey(t string) string {
	return strings.ToLower(strings.TrimSpace(t))
}

// findLibraryItem asks the server for the item a key stored: by its object id
// first, then by title in the folder it was listed in. how names the way it
// was found, for the log line. A title that is not unique in that folder is
// not certain enough, so nothing is found then.
func (s *Server) findLibraryItem(ctx context.Context, srv dlna.Server, ref presets.TrackRef) (it dlna.Item, how string, ok bool) {
	if ref.ObjectID != "" {
		got, err := dlna.ItemMetadata(ctx, srv, ref.ObjectID)
		if err == nil && got.StreamURL != "" {
			return got, "object id", true
		}
		s.logger.Info("library key: the server no longer knows the track by its id, looking in its folder",
			"server", srv.FriendlyName, "id", ref.ObjectID, "err", err)
	}
	want := libraryTitleKey(ref.Title)
	if ref.ParentID == "" || want == "" {
		return dlna.Item{}, "", false
	}
	items, _, err := s.browseContainerItems(ctx, srv, ref.ParentID)
	if err != nil {
		s.logger.Info("library key: the track's folder could not be read", "server", srv.FriendlyName,
			"folder", ref.ParentID, "err", err)
		return dlna.Item{}, "", false
	}
	n := 0
	for _, c := range items {
		if libraryTitleKey(c.Title) == want {
			it = c
			n++
		}
	}
	if n != 1 {
		return dlna.Item{}, "", false
	}
	return it, "title in its folder", true
}

// sameHost reports whether two absolute URLs sit on the same host. A cover on
// the file's host is the server's own and renumbered with it.
func sameHost(a, b string) bool {
	h := urlHost(a)
	return h != "" && h == urlHost(b)
}

// reresolveLibraryKey points a single library track on a key at the URL its
// server hands out for the track now, when the stored URL stopped serving and
// the key knows the track's object (presets.TrackRef). It reports whether the
// key changed; a changed key is persisted (storeHealedKey).
func (s *Server) reresolveLibraryKey(ctx context.Context, slot int, p *presets.Preset) bool {
	if p == nil || p.Track == nil || p.Source == "" || !isPlainHTTPURL(p.StreamURL) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(p.Type)) {
	case "spotify", "queue", presets.TypeNative:
		return false
	}
	ref := *p.Track
	if ref.ObjectID == "" && (ref.ParentID == "" || ref.Title == "") {
		return false
	}
	if s.libraryFileServes(ctx, p.StreamURL) {
		return false
	}
	rctx, cancel := context.WithTimeout(ctx, libraryReresolveTimeout)
	defer cancel()
	srv, ok := s.libraryResolveServer(rctx, ref.ServerUDN, p.Source)
	if !ok {
		s.logger.Info("library key: the file no longer serves and its music server was not found",
			"slot", slot, "server", p.Source)
		return false
	}
	it, how, ok := s.findLibraryItem(rctx, srv, ref)
	if !ok {
		s.logger.Info("library key: the file no longer serves and the server could not find the track again",
			"slot", slot, "server", p.Source, "title", ref.Title)
		return false
	}
	if it.StreamURL == p.StreamURL {
		return false
	}
	old := p.StreamURL
	if it.AlbumArtURL != "" && (p.Art == "" || sameHost(p.Art, old)) {
		p.Art = it.AlbumArtURL
	}
	p.StreamURL = it.StreamURL
	if p.DurationSec == 0 && it.DurationSec > 0 {
		p.DurationSec = it.DurationSec
	}
	// A new pointer: the store hands out presets that share its own TrackRef.
	if it.ID != "" {
		ref.ObjectID = it.ID
	}
	p.Track = &ref
	s.logger.Info("library key: the music server renumbered the file, pointing the key at its new address",
		"slot", slot, "server", p.Source, "found", how, "was", old, "now", p.StreamURL)
	s.storeHealedKey("library key", slot, *p, nil)
	return true
}

// reresolveQueuePreset is reresolveLibraryKey for a folder key: when its first
// track no longer serves, the folder (presets.FolderRef) is read again once and
// every stored track is matched up with its current URL, by object id where the
// key stored one, else by title (the n-th track of a title to the n-th). Tracks
// that find no match keep their URL. It reports whether the key changed.
func (s *Server) reresolveQueuePreset(ctx context.Context, slot int, p *presets.Preset) bool {
	if p == nil || p.Type != "queue" || len(p.Items) == 0 || p.Folder == nil ||
		p.Folder.ContainerID == "" || !isPlainHTTPURL(p.Items[0].URL) {
		return false
	}
	if s.libraryFileServes(ctx, p.Items[0].URL) {
		return false
	}
	rctx, cancel := context.WithTimeout(ctx, libraryFolderReresolveTimeout)
	defer cancel()
	srv, ok := s.libraryResolveServer(rctx, p.Folder.ServerUDN, p.Source)
	if !ok {
		s.logger.Info("queue preset: the tracks no longer serve and the music server was not found",
			"slot", slot, "server", p.Source)
		return false
	}
	fresh, _, err := s.browseContainerItems(rctx, srv, p.Folder.ContainerID)
	if err != nil || len(fresh) == 0 {
		s.logger.Info("queue preset: the tracks no longer serve and the folder could not be read again",
			"slot", slot, "server", p.Source, "folder", p.Folder.ContainerID, "err", err)
		return false
	}
	n := remapQueueItems(p, fresh)
	if n == 0 {
		s.logger.Info("queue preset: the tracks no longer serve and none was found in the folder again",
			"slot", slot, "server", p.Source, "folder", p.Folder.ContainerID)
		return false
	}
	s.logger.Info("queue preset: the music server renumbered the files, pointing the key at their new addresses",
		"slot", slot, "server", p.Source, "tracks", n, "of", len(p.Items))
	s.storeHealedKey("queue preset", slot, *p, nil)
	return true
}

// remapQueueItems gives each track of a folder key the URL the freshly read
// folder lists for it and returns how many changed. The items are copied first:
// the store hands out presets that share its backing array.
func remapQueueItems(p *presets.Preset, fresh []dlna.Item) int {
	byID := make(map[string]dlna.Item, len(fresh))
	byTitle := make(map[string][]dlna.Item, len(fresh))
	for _, it := range fresh {
		if it.ID != "" {
			byID[it.ID] = it
		}
		if k := libraryTitleKey(it.Title); k != "" {
			byTitle[k] = append(byTitle[k], it)
		}
	}
	items := append([]presets.PresetItem(nil), p.Items...)
	seen := map[string]int{}
	n := 0
	for i := range items {
		cur := items[i]
		var it dlna.Item
		found := false
		if cur.ObjectID != "" {
			it, found = byID[cur.ObjectID]
		}
		if !found {
			k := libraryTitleKey(cur.Title)
			if k == "" {
				continue
			}
			list := byTitle[k]
			idx := seen[k]
			seen[k]++
			if idx >= len(list) {
				continue
			}
			it = list[idx]
		}
		if it.StreamURL == "" || it.StreamURL == cur.URL {
			continue
		}
		if it.AlbumArtURL != "" && (cur.Art == "" || sameHost(cur.Art, cur.URL)) {
			items[i].Art = it.AlbumArtURL
		}
		items[i].URL = it.StreamURL
		if it.ID != "" {
			items[i].ObjectID = it.ID
		}
		n++
	}
	if n > 0 {
		p.Items = items
	}
	return n
}

// libraryRefCacheCap bounds how many listed tracks the agent remembers. A
// phone page lists 50 at a time; this covers the browsing of a session.
const libraryRefCacheCap = 1024

// libraryRefCache maps a track URL the agent listed (a phone page browse or
// search) to the object it belongs to, so a key saved from that listing
// carries the object id even though the phone sends only the URL. The zero
// value is ready to use.
type libraryRefCache struct {
	mu    sync.Mutex
	byURL map[string]presets.TrackRef
	order []string
}

func (c *libraryRefCache) put(url string, ref presets.TrackRef) {
	if url == "" || ref.ServerUDN == "" || ref.ObjectID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byURL == nil {
		c.byURL = make(map[string]presets.TrackRef)
	}
	if _, have := c.byURL[url]; !have {
		c.order = append(c.order, url)
		if len(c.order) > libraryRefCacheCap {
			delete(c.byURL, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.byURL[url] = ref
}

func (c *libraryRefCache) get(url string) (presets.TrackRef, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ref, ok := c.byURL[url]
	return ref, ok
}

// noteLibraryItems remembers the object of every track a library listing of
// server udn handed out.
func (s *Server) noteLibraryItems(udn string, items []dlna.Item) {
	for _, it := range items {
		s.libRefs.put(it.StreamURL, presets.TrackRef{
			ServerUDN: udn, ObjectID: it.ID, ParentID: it.ParentID, Title: it.Title,
		})
	}
}

// fillLibraryRefs adds the object ids the agent knows from its own listings to
// a library key being saved without them: a single track gets its TrackRef, a
// folder's tracks their object ids. What the saver sent always wins.
func (s *Server) fillLibraryRefs(p *presets.Preset) {
	if p == nil {
		return
	}
	if p.Type == "queue" {
		var items []presets.PresetItem
		for i, it := range p.Items {
			if it.ObjectID != "" {
				continue
			}
			ref, ok := s.libRefs.get(it.URL)
			if !ok {
				continue
			}
			if items == nil {
				items = append([]presets.PresetItem(nil), p.Items...)
			}
			items[i].ObjectID = ref.ObjectID
		}
		if items != nil {
			p.Items = items
		}
		return
	}
	if p.Track != nil || libraryFileMime(*p) == "" {
		return
	}
	if ref, ok := s.libRefs.get(p.StreamURL); ok {
		p.Track = &ref
	}
}
