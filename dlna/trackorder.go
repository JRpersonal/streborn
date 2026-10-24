package dlna

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Album track order (#1487).
//
// Plenty of media servers hand a folder's tracks over in file-name or title
// order, so an album opened in the Library, played as a folder, or saved on a
// folder key ran "Angel, Black, Come ..." instead of 1, 2, 3. The DIDL carries
// upnp:originalTrackNumber (and on some servers a disc number), so a folder
// that is plainly an album is put back into (disc, track) order.
//
// The rule is deliberately narrow, because the server order is the only order
// STR can be sure somebody meant:
//   - Only when EVERY audio item has a track number above 0 and no two share the
//     same (disc, track) pair. A mixed folder, a folder without numbers, or one
//     holding two "track 1"s keeps the server order.
//   - Never inside a playlist container: its order is the user's.
//   - Only the audio items move, and only among the slots audio items held.
//     Folders and non-audio items keep the positions the server gave them.
//   - Stable: equal keys cannot occur under the rule, but the sort is stable
//     regardless so a later relaxation cannot shuffle anything.

// IsPlaylistClass reports whether a upnp:class names a playlist container
// (object.container.playlistContainer and anything derived from it).
func IsPlaylistClass(class string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(class)), "object.container.playlistcontainer")
}

// AlbumTrackOrder returns items with the audio items in (disc, track) order
// when the set looks like an album, and reports whether anything moved. When
// the set is not album-like, or already in order, it returns items unchanged
// and false. The input slice is never modified.
//
// It knows nothing about the container: a caller that can tell the items come
// from a playlist must not call it (OrderContainerItems does that check).
func AlbumTrackOrder(items []Item) ([]Item, bool) {
	var slots []int
	seen := make(map[[2]int]bool)
	for i, it := range items {
		if !it.IsAudioItem() {
			continue
		}
		if it.TrackNumber <= 0 {
			return items, false
		}
		k := [2]int{it.DiscNumber, it.TrackNumber}
		if seen[k] {
			return items, false
		}
		seen[k] = true
		slots = append(slots, i)
	}
	if len(slots) < 2 {
		return items, false
	}
	audio := make([]Item, len(slots))
	for j, i := range slots {
		audio[j] = items[i]
	}
	less := func(a, b Item) bool {
		if a.DiscNumber != b.DiscNumber {
			return a.DiscNumber < b.DiscNumber
		}
		return a.TrackNumber < b.TrackNumber
	}
	if sort.SliceIsSorted(audio, func(i, j int) bool { return less(audio[i], audio[j]) }) {
		return items, false
	}
	sort.SliceStable(audio, func(i, j int) bool { return less(audio[i], audio[j]) })
	out := make([]Item, len(items))
	copy(out, items)
	for j, i := range slots {
		out[i] = audio[j]
	}
	return out, true
}

// containerClassFn looks a container's upnp:class up; swapped out in tests.
var containerClassFn = ContainerClass

// OrderContainerItems applies AlbumTrackOrder to the complete item list of one
// container and keeps the server order when the container is a playlist. The
// container's class is only asked for when the order would actually change, so
// a folder that is already in order, or not an album, costs no extra request.
// A class lookup that fails keeps the server order: re-ordering a playlist is
// worse than leaving an album alphabetical.
//
// items must be the WHOLE container: sorting one page of a longer folder would
// order that page alone, and the next page would start over at track 1.
func OrderContainerItems(ctx context.Context, srv Server, containerID string, items []Item) []Item {
	sorted, moved := AlbumTrackOrder(items)
	if !moved {
		return items
	}
	class, err := containerClassFn(ctx, srv, containerID)
	if err != nil {
		Logger.Info("dlna: album track order skipped, the folder's class could not be read",
			"container", containerID, "err", err)
		return items
	}
	if IsPlaylistClass(class) {
		return items
	}
	return sorted
}

// OrderBrowsePage re-orders the tracks of a Browse answer by OrderContainerItems
// when that answer is the whole container: the first page, holding every child
// the server counts. A page of a longer container is returned as it came.
func OrderBrowsePage(ctx context.Context, srv Server, containerID string, start int, res BrowseResult) BrowseResult {
	if !res.Complete(start) {
		return res
	}
	res.Items = OrderContainerItems(ctx, srv, containerID, res.Items)
	return res
}

// Complete reports whether a Browse answer fetched at offset start holds every
// child of its container. A server that sends no TotalMatches is taken at its
// word that the page is everything.
func (r BrowseResult) Complete(start int) bool {
	if start != 0 {
		return false
	}
	return r.TotalMatches <= len(r.Containers)+len(r.Items)
}

// ContainerClass returns the upnp:class of one container by a BrowseMetadata
// call. The root ("0" or "") is a plain storage folder by definition.
func ContainerClass(ctx context.Context, srv Server, objectID string) (string, error) {
	if objectID == "" || objectID == "0" {
		return "object.container", nil
	}
	raw, status, err := browseRaw(ctx, srv, objectID, browseMetadata, 0, 0)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("browse metadata failed: %s", soapFaultMessage(raw, status))
	}
	res, err := parseBrowseResponse(raw)
	if err != nil {
		return "", err
	}
	for _, c := range res.Containers {
		if c.ID == objectID || len(res.Containers) == 1 {
			return c.Class, nil
		}
	}
	return "", fmt.Errorf("browse metadata: no container %q in the answer", objectID)
}

// firstNumber returns the first value among the lists that parses to a
// positive number. Servers write a track as "3", " 03 ", or "3/12"; the leading
// digits are what counts.
func firstNumber(lists ...[]string) int {
	for _, l := range lists {
		for _, v := range l {
			v = strings.TrimSpace(v)
			end := 0
			for end < len(v) && v[end] >= '0' && v[end] <= '9' {
				end++
			}
			if end == 0 {
				continue
			}
			if n, err := strconv.Atoi(v[:end]); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}
