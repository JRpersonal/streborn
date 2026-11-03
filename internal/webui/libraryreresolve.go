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
//   - never a library crawl: one metadata call, at most one folder, or for a
//     folder of folders the bounded walk it was saved from (libraryfoldertree.go);
//   - keys saved before this (no ids) keep exactly their old behaviour.

package webui

import (
	"context"
	"errors"
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
//
// missing is set when nothing was found and the server's own answer to the
// last question it was asked says the track is not there any more: a "no such
// object" (libraryServerAnswered), or the whole folder listed without the
// title. A folder read only in part (a later page failed, or the cap was
// reached) is not that answer: the title may sit in the part that was not
// read. A title that is listed twice is not missing, only ambiguous.
func (s *Server) findLibraryItem(ctx context.Context, srv dlna.Server, ref presets.TrackRef) (it dlna.Item, how string, ok, missing bool) {
	answered := false
	if ref.ObjectID != "" {
		got, err := dlna.ItemMetadata(ctx, srv, ref.ObjectID)
		if err == nil && got.StreamURL != "" {
			return got, "object id", true, false
		}
		answered = libraryServerAnswered(ctx, err)
		s.logger.Info("library key: the server no longer knows the track by its id, looking in its folder",
			"server", srv.FriendlyName, "id", ref.ObjectID, "err", err)
	}
	want := libraryTitleKey(ref.Title)
	if ref.ParentID == "" || want == "" {
		return dlna.Item{}, "", false, answered
	}
	items, whole, err := s.browseContainerItems(ctx, srv, ref.ParentID)
	if err != nil {
		s.logger.Info("library key: the track's folder could not be read", "server", srv.FriendlyName,
			"folder", ref.ParentID, "err", err)
		return dlna.Item{}, "", false, libraryServerAnswered(ctx, err)
	}
	n := 0
	for _, c := range items {
		if libraryTitleKey(c.Title) == want {
			it = c
			n++
		}
	}
	if n != 1 {
		return dlna.Item{}, "", false, n == 0 && whole && ctx.Err() == nil
	}
	return it, "title in its folder", true, false
}

// libraryServerAnswered reports whether a failed library question was the
// server saying the object is not there: a UPnP 701 or 710, or a metadata
// answer without the item (dlna.ErrNoSuchObject). Nothing else counts: a
// connection that failed, a budget that ran out, a server that refuses the
// speaker (401, 403) or is busy rescanning (503, a 501 "Action Failed"), or an
// answer that could not be read say nothing about what the server holds, and
// the press keeps its old behaviour then.
func libraryServerAnswered(ctx context.Context, err error) bool {
	return ctx.Err() == nil && errors.Is(err, dlna.ErrNoSuchObject)
}

// sameHost reports whether two absolute URLs sit on the same host. A cover on
// the file's host is the server's own and renumbered with it.
func sameHost(a, b string) bool {
	h := urlHost(a)
	return h != "" && h == urlHost(b)
}

// libraryKeyUnplayable is the verdict on a library key press whose file did
// not serve, in one of two shapes:
//
//   - the music server was not found either, while nothing answers at the
//     file's address: the server is off (#1339). Handing that file to the
//     speaker only ends in a silent timeout, which is all the reporter got
//     where the old Bose app used to say "server unavailable";
//   - FileGone: the server was found and answers, but no longer has the track
//     (#919, #733, #726): its library was re-indexed, or the folder or drive
//     with the music is not there any more. The speaker answers that file with
//     AUDIO_ERROR_BAD_URL and nobody was told why.
//
// Server is the name to show; empty when the key names none.
type libraryKeyUnplayable struct {
	Server   string
	FileGone bool
}

// libraryKeyFileGone is the FileGone verdict for a key of the server srv,
// named by what the key recorded, else by what the server announces.
func libraryKeyFileGone(source string, srv dlna.Server) *libraryKeyUnplayable {
	name := strings.TrimSpace(source)
	if name == "" {
		name = strings.TrimSpace(srv.FriendlyName)
	}
	return &libraryKeyUnplayable{Server: name, FileGone: true}
}

// libraryKeyOffline decides, once the file did not serve and the server was
// not found, whether that is a server that is off: nothing accepts a
// connection at the file's address. A host that does answer (a server that
// 404s a file it renumbered, but that discovery cannot place) is not off, and
// the key keeps its old behaviour there. Only this failure path pays for the
// connect.
func (s *Server) libraryKeyOffline(ctx context.Context, fileURL, source, udn string) *libraryKeyUnplayable {
	if s.queueTrackHostAnswers(ctx, fileURL) {
		return nil
	}
	name := strings.TrimSpace(source)
	if name == "" {
		name = s.registeredName(udnKey(udn))
	}
	return &libraryKeyUnplayable{Server: name}
}

// reresolveLibraryKey points a single library track on a key at the URL its
// server hands out for the track now, when the stored URL stopped serving and
// the key knows the track's object (presets.TrackRef). It reports whether the
// key changed; a changed key is persisted (storeHealedKey). dead is set when
// the file does not serve because its music server is off (libraryKeyOffline),
// or because the server answers but no longer has the track (FileGone).
//
// A server that still lists the track under the very address that did not
// serve is left alone: that may be a server that only refuses the probe, and
// the press goes ahead the way it always did.
func (s *Server) reresolveLibraryKey(ctx context.Context, slot int, p *presets.Preset) (changed bool, dead *libraryKeyUnplayable) {
	if p == nil || p.Track == nil || p.Source == "" || !isPlainHTTPURL(p.StreamURL) {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(p.Type)) {
	case "spotify", "queue", presets.TypeNative:
		return false, nil
	}
	ref := *p.Track
	if ref.ObjectID == "" && (ref.ParentID == "" || ref.Title == "") {
		return false, nil
	}
	if s.libraryFileServes(ctx, p.StreamURL) {
		return false, nil
	}
	rctx, cancel := context.WithTimeout(ctx, libraryReresolveTimeout)
	defer cancel()
	srv, ok := s.libraryResolveServer(rctx, ref.ServerUDN, p.Source)
	if !ok {
		// ctx, not rctx: the resolve may have spent all of rctx.
		off := s.libraryKeyOffline(ctx, p.StreamURL, p.Source, ref.ServerUDN)
		s.logger.Info("library key: the file no longer serves and its music server was not found",
			"slot", slot, "server", p.Source, "serverOff", off != nil)
		return false, off
	}
	it, how, ok, missing := s.findLibraryItem(rctx, srv, ref)
	if !ok {
		s.logger.Info("library key: the file no longer serves and the server could not find the track again",
			"slot", slot, "server", p.Source, "title", ref.Title, "fileGone", missing)
		if missing {
			return false, libraryKeyFileGone(p.Source, srv)
		}
		return false, nil
	}
	if it.StreamURL == p.StreamURL {
		return false, nil
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
	return true, nil
}

// reresolveQueuePreset is reresolveLibraryKey for a folder key: when its first
// track no longer serves, the folder (presets.FolderRef) is read again once and
// every stored track is matched up with its current URL, by object id where the
// key stored one, else by title (the n-th track of a title to the n-th). Tracks
// that find no match keep their URL. It reports whether the key changed, and
// dead when its music server is off (libraryKeyOffline) or answers without the
// key's music (FileGone): the folder is gone, empty, or holds none of the
// key's tracks any more.
func (s *Server) reresolveQueuePreset(ctx context.Context, slot int, p *presets.Preset) (changed bool, dead *libraryKeyUnplayable) {
	if p == nil || p.Type != "queue" || len(p.Items) == 0 || p.Folder == nil ||
		p.Folder.ContainerID == "" || !isPlainHTTPURL(p.Items[0].URL) {
		return false, nil
	}
	if s.libraryFileServes(ctx, p.Items[0].URL) {
		return false, nil
	}
	rctx, cancel := context.WithTimeout(ctx, libraryFolderReresolveTimeout)
	defer cancel()
	srv, ok := s.libraryResolveServer(rctx, p.Folder.ServerUDN, p.Source)
	if !ok {
		// ctx, not rctx: the resolve may have spent all of rctx.
		off := s.libraryKeyOffline(ctx, p.Items[0].URL, p.Source, p.Folder.ServerUDN)
		s.logger.Info("queue preset: the tracks no longer serve and the music server was not found",
			"slot", slot, "server", p.Source, "serverOff", off != nil)
		return false, off
	}
	// A folder of folders (an artist with one folder per album, #1682) is read
	// with its sub-folders, the way it was walked when it was put on the key.
	tree, err := s.collectFolderTracks(rctx, srv, p.Folder.ContainerID, false)
	fresh := tree.items
	if err != nil || len(fresh) == 0 {
		// Gone only on the server's own word: a "no such container" for the
		// folder, or a walk that read every folder to the end and found no song.
		// A sub-folder that could not be read, a page that failed or a walk that
		// was cut short leaves the music possibly where nobody looked.
		gone := libraryServerAnswered(rctx, err)
		if err == nil {
			gone = rctx.Err() == nil && tree.complete()
		}
		s.logger.Info("queue preset: the tracks no longer serve and the folder could not be read again",
			"slot", slot, "server", p.Source, "folder", p.Folder.ContainerID, "err", err, "fileGone", gone)
		if gone {
			return false, libraryKeyFileGone(p.Source, srv)
		}
		return false, nil
	}
	n := remapQueueItems(p, fresh)
	if n == 0 {
		// None moved. Gone only when the folder lists none of the key's tracks
		// under the address the key has either: a track listed where the key
		// already points is still there, whatever the probe said. And only when
		// the walk read the whole tree: the key's tracks may sit in the part that
		// was not read.
		gone := rctx.Err() == nil && tree.complete() && !queueItemsListed(p.Items, fresh)
		s.logger.Info("queue preset: the tracks no longer serve and none was found in the folder again",
			"slot", slot, "server", p.Source, "folder", p.Folder.ContainerID, "fileGone", gone)
		if gone {
			return false, libraryKeyFileGone(p.Source, srv)
		}
		return false, nil
	}
	s.logger.Info("queue preset: the music server renumbered the files, pointing the key at their new addresses",
		"slot", slot, "server", p.Source, "tracks", n, "of", len(p.Items))
	s.storeHealedKey("queue preset", slot, *p, nil)
	return true, nil
}

// queueItemsListed reports whether any track of a folder key sits in the
// freshly read folder under the address the key already holds for it.
func queueItemsListed(items []presets.PresetItem, fresh []dlna.Item) bool {
	listed := make(map[string]bool, len(fresh))
	for _, it := range fresh {
		if it.StreamURL != "" {
			listed[it.StreamURL] = true
		}
	}
	for _, it := range items {
		if listed[it.URL] {
			return true
		}
	}
	return false
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
