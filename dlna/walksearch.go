package dlna

import (
	"context"
	"strings"
)

// The bounded browse walk is the search fallback for media servers without a
// usable ContentDirectory Search action. Plenty of them exist: Synology and
// Windows Media Player answer Search with UPnPError 501, others answer 200 with
// zero hits for a track they hold (the FRITZ!Box "Fly FRITZ!" quirk of #666).
// The speaker agent's phone page has walked such servers since #666; the
// desktop app did not, and told a user whose Synology and WMP shares both
// refused Search that neither server had answered (#1705). Both now share this
// one implementation.

// Default walk bounds. They were tuned on the speaker, whose CPU is the weak
// end of the two callers, and are a hard ceiling on how long a walk can keep a
// server (and the caller) busy.
const (
	// WalkMaxBrowses bounds the number of Browse calls. At WalkPageSize
	// children per call this is 12000 entries, enough to cover the 2600 track
	// library of #666.
	WalkMaxBrowses = 60
	// WalkMaxDepth keeps the walk out of pathological trees. It was 4, on the
	// assumption that libraries are Artist/Album/Track. The #666 screenshots
	// show a real one seven levels down (Music > Folder > Music > Acoustic >
	// Acoustic Chill > Adrianne Lenker > abysskiss), which the old cap cut off
	// entirely: that song could never be found from the phone.
	WalkMaxDepth = 8
	// WalkPageSize is how many children one Browse asks for. Same page size the
	// desktop Library uses (LIB_PAGE in views/library.js), for the same reason:
	// fewer, bigger SOAP calls beat many small ones on a weak CPU.
	WalkPageSize = 200
	// WalkMaxPerContainer stops the paging of ONE container, so a flat "all
	// songs" folder with 50k rows cannot eat the whole browse budget. 2500
	// mirrors the desktop's LIB_MAX ceiling.
	WalkMaxPerContainer = 2500
)

// WalkOptions bounds one walk. A zero field takes the matching default above.
type WalkOptions struct {
	// MaxHits stops the walk once this many matching tracks are found.
	// Required: a walk without a hit ceiling is a bug in the caller.
	MaxHits         int
	MaxBrowses      int
	MaxDepth        int
	PageSize        int
	MaxPerContainer int
	// OnReadError, when set, is told about every container page the walk could
	// not read. A container that could not be read is a hole in the coverage,
	// not a container that holds nothing, and the callers log it: the #666
	// reporter's server could not be reproduced here, and a diagnostic taken
	// after a failing search is the only way to learn that a NAS refuses a
	// particular container or page offset.
	OnReadError func(container string, start int, err error)
}

func (o WalkOptions) withDefaults() WalkOptions {
	if o.MaxHits <= 0 {
		o.MaxHits = 40
	}
	if o.MaxBrowses <= 0 {
		o.MaxBrowses = WalkMaxBrowses
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = WalkMaxDepth
	}
	if o.PageSize <= 0 {
		o.PageSize = WalkPageSize
	}
	if o.MaxPerContainer <= 0 {
		o.MaxPerContainer = WalkMaxPerContainer
	}
	return o
}

// WalkResult is what a bounded walk found.
type WalkResult struct {
	// Items are the matching audio tracks, deduplicated on the stream URL.
	Items []Item
	// Partial says the walk did NOT cover the whole tree: it stopped on a
	// budget, the deadline, the depth cap, or a container it could not read.
	// "Searched a corner and found nothing there" is a different statement
	// from "the library does not contain this".
	Partial bool
	// Browses is how many Browse calls the walk made.
	Browses int
	// RootErr is set when not even the server's root container could be read
	// (the Browse failed, or the deadline was gone before it could be asked).
	// That, and only that, is a server that did not answer: every other
	// failure still leaves a walk that searched part of the library.
	RootErr error
}

// WalkSearch is a breadth-first browse of the server's tree for the audio
// tracks whose field contains query (case-insensitive), matching title, artist
// and album the way the desktop Library filters a loaded folder when field is
// SearchAny or unknown. Bounded in calls, depth and per-container paging so a
// huge NAS cannot pin the caller, and it stops at ctx's deadline.
func WalkSearch(ctx context.Context, srv Server, query, field string, opts WalkOptions) WalkResult {
	opts = opts.withDefaults()
	qLower := strings.ToLower(query)
	matches := walkMatcher(qLower, field)
	type node struct {
		id    string
		depth int
	}
	queue := []node{{id: "0"}}
	queued := map[string]bool{"0": true}
	// A track is reachable through several containers on the same server (the
	// flat Songs list, its album, and the folder view all expose the same
	// file), and a deeper walk meets all three. Deduplicate on the stream URL
	// so one song is not listed three times.
	seenStream := map[string]bool{}
	var out WalkResult
	rootRead := false
	for len(queue) > 0 && len(out.Items) < opts.MaxHits {
		if out.Browses >= opts.MaxBrowses || ctx.Err() != nil {
			out.Partial = true
			break
		}
		n := queue[0]
		queue = queue[1:]

		// Page through the container instead of reading only its first page.
		// The old phone walk passed StartingIndex 0 with a count of 100 and
		// never asked again, so on the #666 reporter's flat "Songs" container
		// only the first 100 of 2600 tracks were ever searchable.
		start := 0
		seenChild := map[string]bool{}
		for {
			if out.Browses >= opts.MaxBrowses || ctx.Err() != nil {
				out.Partial = true
				break
			}
			out.Browses++
			res, err := Browse(ctx, srv, n.id, start, opts.PageSize)
			if err != nil {
				if opts.OnReadError != nil {
					opts.OnReadError(n.id, start, err)
				}
				if n.id == "0" && start == 0 {
					out.RootErr = err
				}
				out.Partial = true
				break
			}
			if n.id == "0" {
				rootRead = true
			}
			returned := len(res.Items) + len(res.Containers)
			fresh := 0
			for _, it := range res.Items {
				if seenChild[it.ID] {
					continue
				}
				seenChild[it.ID] = true
				fresh++
				// The walk reaches Video and Pictures branches too, so the
				// class filter is not optional.
				if !it.IsAudioItem() || it.StreamURL == "" || !matches(it) {
					continue
				}
				if seenStream[it.StreamURL] {
					continue
				}
				seenStream[it.StreamURL] = true
				out.Items = append(out.Items, it)
				if len(out.Items) >= opts.MaxHits {
					break
				}
			}
			for _, c := range res.Containers {
				if seenChild[c.ID] {
					continue
				}
				seenChild[c.ID] = true
				fresh++
				if n.depth+1 >= opts.MaxDepth {
					// Deeper than the walk goes: say the answer is incomplete
					// rather than let a nested corner of the library look like
					// a corner that holds nothing.
					out.Partial = true
					continue
				}
				if queued[c.ID] {
					continue
				}
				queued[c.ID] = true
				// Folders whose NAME matches are worth descending into ahead
				// of the breadth order (an album named like the query holds
				// the tracks being looked for).
				if strings.Contains(strings.ToLower(c.Title), qLower) {
					queue = append([]node{{id: c.ID, depth: n.depth + 1}}, queue...)
				} else {
					queue = append(queue, node{id: c.ID, depth: n.depth + 1})
				}
			}
			if len(out.Items) >= opts.MaxHits {
				break
			}
			start += returned
			// Stop on the two ways a server can refuse to page: an empty
			// answer, or one that ignores StartingIndex and hands back the
			// same children forever (fresh == 0).
			if returned == 0 || fresh == 0 {
				break
			}
			// TotalMatches, not the size of the page, decides whether the
			// container is exhausted. A short page is NOT proof: plenty of
			// ContentDirectory servers silently cap RequestedCount (the spec
			// lets them), and treating their cap as the end of the container
			// would leave the tail of a big container silently unsearchable,
			// at whatever offset the server happens to cap at. Only when the
			// server declines to say how many children it has is the short
			// page all there is to go on. The runaway cases are already caught
			// above and by the browse budget, so trusting TotalMatches here
			// cannot loop.
			if res.TotalMatches > 0 {
				if start >= res.TotalMatches {
					break
				}
			} else if returned < opts.PageSize {
				break
			}
			if start >= opts.MaxPerContainer {
				out.Partial = true
				break
			}
		}
	}
	if len(queue) > 0 && len(out.Items) < opts.MaxHits {
		// Containers left unvisited: the answer covers part of the tree only.
		out.Partial = true
	}
	if !rootRead && out.RootErr == nil {
		// The deadline or the budget ended the walk before the root was read
		// (a Search that hung on a sleeping NAS can use up the whole time).
		out.RootErr = ctx.Err()
		if out.RootErr == nil {
			out.RootErr = context.DeadlineExceeded
		}
	}
	return out
}

// walkMatcher returns the test a walked track must pass for the field the
// search was narrowed to (#1325); an empty or unknown field is SearchAny.
func walkMatcher(qLower, field string) func(Item) bool {
	has := func(v string) bool { return strings.Contains(strings.ToLower(v), qLower) }
	switch field {
	case SearchTitle:
		return func(it Item) bool { return has(it.Title) }
	case SearchArtist:
		return func(it Item) bool { return has(it.Artist) }
	case SearchAlbum:
		return func(it Item) bool { return has(it.Album) }
	}
	return func(it Item) bool { return has(it.Title) || has(it.Artist) || has(it.Album) }
}
