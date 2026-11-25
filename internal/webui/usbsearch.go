package webui

import (
	"context"
	"time"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/internal/usbmedia"
)

// The phone page's library search, for the music stick in this speaker
// (discussion #1136, 2026-11-22: "searching music servers does not display any
// results for the USB stick").
//
// The search used to ask only the servers in the mediaservers store, and the
// stick is never in there (it is not announced, usbmusic.go), so the phone
// found nothing on it, and with no other server registered it even said that
// no music server was set up. The stick is local, so it is searched here
// directly (usbmedia.Library.Search: directory reads, no file opened), not
// over SOAP. A hit is the track a Browse of its folder lists: the same
// loopback file URL the speaker plays, the cover at the address the phone
// reached the agent at, and its object id noted for a key saved from it.

// usbSearchBudget bounds the stick's share of a search. Directory reads on a
// USB stick are fast; a stick with thousands of folders is cut short here and
// the answer marked partial.
const usbSearchBudget = 8 * time.Second

// searchUSBStick searches the stick in this speaker, when one is served. stick
// says one is; partial that the search could not cover all of it.
func (s *Server) searchUSBStick(ctx context.Context, host, q, field string) (results []librarySearchResult, stick, partial bool) {
	// No mount for a search: the phone page searches the libraries with
	// every radio search, and the agent mounts a stick nothing mounted (the
	// Portable) only when a user opens the Library (usbmusic.go). A firmware
	// mount, and one the agent made earlier, are searched.
	lib, info, ok := s.usbServed(false)
	if !ok {
		return nil, false, false
	}
	sctx, cancel := context.WithTimeout(ctx, usbSearchBudget)
	defer cancel()
	began := time.Now()
	res, err := lib.Search(sctx, "0", usbmedia.TermsFor(q, field), librarySearchMax)
	if err != nil {
		s.logger.Info("library search: the USB stick could not be read", "err", err)
		return nil, true, true
	}
	s.logger.Info("library search: searched the USB stick",
		"q", q, "field", field, "hits", len(res.Hits), "folders", res.Folders,
		"partial", res.Partial, "ms", time.Since(began).Milliseconds())
	// The songs found are labelled from their tags, as a Browse page is: the
	// walk matched on paths and opened no file.
	hits := res.Hits
	lib.AddTags(hits, time.Now().Add(usbmedia.ListingTagBudget))
	artBase := usbLoopbackBase
	if host != "" {
		artBase = "http://" + host
	}
	items := make([]dlna.Item, 0, len(res.Hits))
	for _, e := range hits {
		album := e.Album
		if album == "" {
			album = usbmedia.FileAlbum(e.ID)
		}
		it := dlna.Item{
			ID: e.ID, ParentID: usbmedia.Parent(e.ID), Title: e.Title,
			Artist: e.Artist, Album: album, Class: "object.item.audioItem.musicTrack",
			MimeType: e.MIME, StreamURL: usbmedia.FileURL(usbLoopbackBase, e.Path),
		}
		items = append(items, it)
		art := ""
		if e.Cover != "" {
			art = usbmedia.ArtURL(artBase, e.Cover)
		}
		results = append(results, librarySearchResult{
			Title: it.Title, Artist: it.Artist, Album: it.Album, URL: it.StreamURL, Art: art,
			Mime: it.MimeType, Server: info.FriendlyName,
		})
	}
	// A key saved from these hits carries each track's object id (#919).
	s.noteLibraryItems(info.UDN, items)
	return results, true, res.Partial
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}
