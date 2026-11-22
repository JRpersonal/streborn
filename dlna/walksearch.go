package dlna

import (
	"context"
	"strings"
	"time"
)

// The bounded browse walk is the search fallback for media servers without a
// usable ContentDirectory Search action. Plenty of them exist: Synology answers
// Search with UPnPError 501, Windows Media Player's share not at all (see
// IsWindowsMediaShare), others answer 200 with zero hits for a track they hold
// (the FRITZ!Box "Fly FRITZ!" quirk of #666).
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
	// Items are the matching audio tracks, deduplicated on the stream URL and
	// on TrackKey (one server can hand the same track out under several URLs).
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
	// Shortcut is the container read ahead of the tree for a server whose
	// layout is known (Windows Media Player's All Music), "" when there is
	// none. ShortcutComplete says it was read to its end, which is then the
	// whole answer; ShortcutErr is why it could not be read, when it could not.
	Shortcut         string
	ShortcutComplete bool
	ShortcutErr      error
	// SlowestBrowse is the longest single Browse of the walk, and
	// SlowestContainer the container it read: a diagnostic bundle then shows
	// whether a server is slow throughout or stalls on one container (#1705,
	// where one PC managed 6 to 9 reads in 20 s and the next PC 60).
	SlowestBrowse    time.Duration
	SlowestContainer string
}

// walkNode is one container waiting to be read.
type walkNode struct {
	id    string
	depth int
}

// walker is the state of one WalkSearch.
type walker struct {
	ctx     context.Context
	srv     Server
	opts    WalkOptions
	qLower  string
	matches func(Item) bool
	queue   []walkNode
	queued  map[string]bool
	// seen holds the stream URLs and TrackKeys of the hits so far. A track is
	// reachable through several containers on the same server (the flat Songs
	// list, its album, and the folder view all expose the same file), and a
	// deeper walk meets all of them.
	seen     map[string]bool
	answered bool
	out      WalkResult
}

// WalkSearch is a breadth-first browse of the server's tree for the audio
// tracks whose field contains query (case-insensitive), matching title, artist
// and album the way the desktop Library filters a loaded folder when field is
// SearchAny or unknown. Bounded in calls, depth and per-container paging so a
// huge NAS cannot pin the caller, and it stops at ctx's deadline.
//
// On a Windows Media Player share the walk reads the share's All Music list
// first and stops there when it has read it to the end (see
// IsWindowsMediaShare); the tree is only walked when that list cannot be read.
func WalkSearch(ctx context.Context, srv Server, query, field string, opts WalkOptions) WalkResult {
	opts = opts.withDefaults()
	qLower := strings.ToLower(query)
	w := &walker{
		ctx: ctx, srv: srv, opts: opts, qLower: qLower,
		matches: walkMatcher(qLower, field),
		queued:  map[string]bool{"0": true},
		seen:    map[string]bool{},
	}
	if IsWindowsMediaShare(srv) && ctx.Err() == nil {
		w.queued[WMPAllMusicID] = true
		w.out.Shortcut = WMPAllMusicID
		exhausted, audio, err := w.read(walkNode{id: WMPAllMusicID, depth: 1}, true)
		w.out.ShortcutErr = err
		// A list that held no track at all is not trusted to be All Music:
		// the tree gets its chance then.
		if exhausted && audio > 0 {
			w.out.ShortcutComplete = true
			return w.out
		}
		if len(w.out.Items) >= opts.MaxHits {
			return w.out
		}
		// Whatever cut the list short (the budget, the deadline) still holds
		// for the tree; a read error or an empty list hands over to it.
		if !exhausted && err == nil {
			w.out.Partial = true
		}
	}
	w.queue = []walkNode{{id: "0"}}
	for len(w.queue) > 0 && len(w.out.Items) < opts.MaxHits {
		if w.out.Browses >= opts.MaxBrowses || ctx.Err() != nil {
			w.out.Partial = true
			break
		}
		n := w.queue[0]
		w.queue = w.queue[1:]
		w.read(n, false)
	}
	if len(w.queue) > 0 && len(w.out.Items) < opts.MaxHits {
		// Containers left unvisited: the answer covers part of the tree only.
		w.out.Partial = true
	}
	if !w.answered && w.out.RootErr == nil {
		// The deadline or the budget ended the walk before the root was read
		// (a Search that hung on a sleeping NAS can use up the whole time).
		w.out.RootErr = ctx.Err()
		if w.out.RootErr == nil {
			w.out.RootErr = context.DeadlineExceeded
		}
	}
	return w.out
}

// read pages through one container, collecting its matching tracks and, unless
// it is the shortcut list, queueing its sub-containers. exhausted is true when
// the container was read to its end; audio counts the audio tracks it held. A
// failed read of a tree container is reported to OnReadError and marks the
// answer partial; a failed shortcut is only returned, the tree covers it.
func (w *walker) read(n walkNode, shortcut bool) (exhausted bool, audio int, err error) {
	opts := w.opts
	// Page through the container instead of reading only its first page.
	// The old phone walk passed StartingIndex 0 with a count of 100 and
	// never asked again, so on the #666 reporter's flat "Songs" container
	// only the first 100 of 2600 tracks were ever searchable.
	start := 0
	seenChild := map[string]bool{}
	for {
		if w.out.Browses >= opts.MaxBrowses || w.ctx.Err() != nil {
			w.out.Partial = true
			return false, audio, nil
		}
		w.out.Browses++
		began := time.Now()
		res, berr := Browse(w.ctx, w.srv, n.id, start, opts.PageSize)
		if took := time.Since(began); took > w.out.SlowestBrowse {
			w.out.SlowestBrowse, w.out.SlowestContainer = took, n.id
		}
		if berr != nil {
			if shortcut {
				return false, audio, berr
			}
			if opts.OnReadError != nil {
				opts.OnReadError(n.id, start, berr)
			}
			if n.id == "0" && start == 0 {
				w.out.RootErr = berr
			}
			w.out.Partial = true
			return false, audio, berr
		}
		w.answered = true
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
			if !it.IsAudioItem() || it.StreamURL == "" {
				continue
			}
			audio++
			if !w.matches(it) {
				continue
			}
			url, key := "url\x1f"+it.StreamURL, TrackKey(it)
			if w.seen[url] || w.seen[key] {
				continue
			}
			w.seen[url], w.seen[key] = true, true
			w.out.Items = append(w.out.Items, it)
			if len(w.out.Items) >= opts.MaxHits {
				break
			}
		}
		for _, c := range res.Containers {
			if seenChild[c.ID] {
				continue
			}
			seenChild[c.ID] = true
			fresh++
			if shortcut {
				// The flat list is read for its tracks only.
				continue
			}
			if n.depth+1 >= opts.MaxDepth {
				// Deeper than the walk goes: say the answer is incomplete
				// rather than let a nested corner of the library look like
				// a corner that holds nothing.
				w.out.Partial = true
				continue
			}
			if w.queued[c.ID] {
				continue
			}
			w.queued[c.ID] = true
			// Folders whose NAME matches are worth descending into ahead
			// of the breadth order (an album named like the query holds
			// the tracks being looked for).
			if strings.Contains(strings.ToLower(c.Title), w.qLower) {
				w.queue = append([]walkNode{{id: c.ID, depth: n.depth + 1}}, w.queue...)
			} else {
				w.queue = append(w.queue, walkNode{id: c.ID, depth: n.depth + 1})
			}
		}
		if len(w.out.Items) >= opts.MaxHits {
			return false, audio, nil
		}
		start += returned
		// Stop on the two ways a server can refuse to page: an empty
		// answer, or one that ignores StartingIndex and hands back the
		// same children forever (fresh == 0).
		if returned == 0 || fresh == 0 {
			return true, audio, nil
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
				return true, audio, nil
			}
		} else if returned < opts.PageSize {
			return true, audio, nil
		}
		// The shortcut list is every track of the share, so it is paged to
		// its end within the browse budget rather than stopped like a flat
		// folder that would crowd out the rest of the tree.
		if !shortcut && start >= opts.MaxPerContainer {
			w.out.Partial = true
			return false, audio, nil
		}
	}
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
