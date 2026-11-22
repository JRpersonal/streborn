package dlna

import (
	"strconv"
	"strings"
)

// Windows Media Player's network sharing, as the library search meets it.
//
// The #1705 reporter shares music from two Windows PCs with Windows Media
// Player. On both, the ContentDirectory Search action never answered within
// the 12 s the desktop gives it, nor within the 15 s of the speaker (diagnostic
// bundles of 2026-11-21 and 2026-11-22), and while the share was still busy
// with that query the folder reads that followed queued behind it and timed
// out too: the first search after every app start, and every search from the
// phone, came back without that PC. Its folders answered (60 reads in 17.6 s
// on one of the PCs). So such a share is not asked to search at all; it is
// walked straight away.
//
// The walk itself was the second half of the problem. Windows Media Player
// lays the same track out under every view (All Music, Genre, Artist, Album,
// Folders, Contributing Artists, ...), and a breadth-first walk spent its
// budget in those views and found the song only by luck, while each view hands
// the track out under its own object id and stream URL: the URL of a hit played
// from the Album view decodes to "7_<track>", the 7 being the Album container.
// One track then came back twice, and both copies played. The share's
// "All Music" container (object id "4", fixed since the Xbox 360 era and not
// localised) lists every music track once, so the walk reads that first and is
// done when it has read it to the end.

// WMPAllMusicID is the object id of Windows Media Player's "All Music" view.
const WMPAllMusicID = "4"

// IsWindowsMediaShare reports whether srv is Windows Media Player's network
// sharing. The control URL is the reliable mark: the share is hosted by the
// Windows UPnP Device Host, whose control URLs run through udhisapi.dll. The
// manufacturer and model are the backstop for a description that names them.
func IsWindowsMediaShare(srv Server) bool {
	if strings.Contains(strings.ToLower(srv.CDSControlURL), "/upnphost/udhisapi.dll") {
		return true
	}
	return strings.Contains(strings.ToLower(srv.Manufacturer), "microsoft") &&
		strings.Contains(strings.ToLower(srv.ModelName), "windows media")
}

// SkipsSearchAction reports whether the library search should walk srv without
// asking its Search action first (see IsWindowsMediaShare).
func SkipsSearchAction(srv Server) bool { return IsWindowsMediaShare(srv) }

// TrackKey is what makes two hits from ONE server the same track. The stream
// URL alone is not enough: Windows Media Player builds it from the object id of
// the view the track was reached through, so the same file has a different URL
// under Album than under All Music. Tracks with the same title, artist, album,
// length, track and disc number and format on one server are one track. With
// too little metadata to tell two songs apart (no title, or neither album nor
// length) the stream URL stays the key, so distinct songs are never merged.
func TrackKey(it Item) string {
	title := normKey(it.Title)
	if title == "" || (normKey(it.Album) == "" && it.DurationSec <= 0) {
		return "url\x1f" + it.StreamURL
	}
	return strings.Join([]string{"meta", title, normKey(it.Artist), normKey(it.Album),
		strconv.Itoa(it.DurationSec), strconv.Itoa(it.TrackNumber), strconv.Itoa(it.DiscNumber),
		strings.ToLower(strings.TrimSpace(it.MimeType))}, "\x1f")
}

func normKey(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// DedupTracks drops the hits of one server that repeat an earlier one, by
// stream URL or by TrackKey, keeping the first in order.
func DedupTracks(items []Item) []Item {
	seen := make(map[string]bool, 2*len(items))
	out := items[:0:0]
	for _, it := range items {
		url, key := "url\x1f"+it.StreamURL, TrackKey(it)
		if (it.StreamURL != "" && seen[url]) || seen[key] {
			continue
		}
		if it.StreamURL != "" {
			seen[url] = true
		}
		seen[key] = true
		out = append(out, it)
	}
	return out
}
