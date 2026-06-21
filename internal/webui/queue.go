package webui

import (
	"math/rand"
	"sync"
	"time"
)

// queueItem is one entry in the DLNA library play queue: the source URL the box
// should play plus the metadata needed to push it and to know when it ends.
// URL is the original (NAS/HTTP) URL exactly as handed to /api/play; the play
// path decides direct-vs-proxy from it, so the queue stores the same value a
// single play would.
type queueItem struct {
	URL      string
	Title    string
	Art      string
	Mime     string
	Duration time.Duration // 0 when the DLNA server did not report one
}

// repeatMode controls what the queue does at the end of a track / the list.
type repeatMode int

const (
	repeatOff repeatMode = iota // stop after the last track
	repeatAll                   // wrap to the start after the last track
	repeatOne                   // replay the current track on natural end
)

func parseRepeat(s string) repeatMode {
	switch s {
	case "all":
		return repeatAll
	case "one":
		return repeatOne
	default:
		return repeatOff
	}
}

func (m repeatMode) String() string {
	switch m {
	case repeatAll:
		return "all"
	case repeatOne:
		return "one"
	default:
		return "off"
	}
}

// playQueue is the agent-side play queue for the DLNA library: an ordered list
// the agent advances through on track end, the way the original SoundTouch
// box-side queue did, so it keeps going with the desktop app closed. All the
// index / order / shuffle / repeat math lives here and is unit-tested; the
// Server wires the actual playback and end-of-track detection around it.
//
// order is a permutation of item indices that defines playback order (identity
// for sequential, a shuffled permutation for shuffle). pos indexes into order.
type playQueue struct {
	mu      sync.Mutex
	items   []queueItem
	order   []int
	pos     int
	repeat  repeatMode
	shuffle bool
	active  bool
	rnd     *rand.Rand
}

func newPlayQueue() *playQueue {
	return &playQueue{rnd: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// load replaces the queue with items and positions it at start. With shuffle
// on, start is played first and the rest follow in random order.
func (q *playQueue) load(items []queueItem, start int, shuffle bool, rep repeatMode) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append([]queueItem(nil), items...)
	q.repeat = rep
	q.shuffle = shuffle
	if len(q.items) == 0 {
		q.order, q.pos, q.active = nil, 0, false
		return
	}
	if start < 0 || start >= len(q.items) {
		start = 0
	}
	q.buildOrder(start)
	// With shuffle, buildOrder moved start to order[0]; sequential order is the
	// identity, so the chosen start sits at order[start].
	if q.shuffle {
		q.pos = 0
	} else {
		q.pos = start
	}
	q.active = true
}

// buildOrder (re)builds order so the item at index keepFirst plays next at
// order[0]. Caller holds the lock.
func (q *playQueue) buildOrder(keepFirst int) {
	n := len(q.items)
	q.order = make([]int, n)
	for i := range q.order {
		q.order[i] = i
	}
	if q.shuffle {
		q.rnd.Shuffle(n, func(i, j int) { q.order[i], q.order[j] = q.order[j], q.order[i] })
		// Move keepFirst to the front so the chosen track plays first.
		for i, v := range q.order {
			if v == keepFirst {
				q.order[0], q.order[i] = q.order[i], q.order[0]
				break
			}
		}
	}
}

func (q *playQueue) currentLocked() (queueItem, bool) {
	if !q.active || q.pos < 0 || q.pos >= len(q.order) {
		return queueItem{}, false
	}
	return q.items[q.order[q.pos]], true
}

// current returns the track that should be playing now.
func (q *playQueue) current() (queueItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.currentLocked()
}

func (q *playQueue) isActive() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.active
}

// advanceNatural moves the queue on a track that finished on its own and
// returns the next track to play. repeatOne replays the same track. At the end
// of the list, repeatAll wraps (reshuffling first when shuffle is on) and any
// other mode deactivates the queue.
func (q *playQueue) advanceNatural() (queueItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.active {
		return queueItem{}, false
	}
	if q.repeat == repeatOne {
		return q.currentLocked()
	}
	return q.stepForwardLocked(q.repeat == repeatAll)
}

// next is a manual skip forward; unlike a natural end it ignores repeatOne.
func (q *playQueue) next() (queueItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.active {
		return queueItem{}, false
	}
	return q.stepForwardLocked(q.repeat == repeatAll)
}

// stepForwardLocked advances pos by one. wrap controls whether running past the
// last track wraps to the front (and reshuffles) or stops the queue.
func (q *playQueue) stepForwardLocked(wrap bool) (queueItem, bool) {
	q.pos++
	if q.pos >= len(q.order) {
		if wrap {
			if q.shuffle {
				q.buildOrder(q.order[0]) // reshuffle; first track is arbitrary
				// buildOrder kept the previous first at front; rotate off it so a
				// wrapped lap does not always restart on the same track.
				if len(q.order) > 1 {
					q.order = append(q.order[1:], q.order[0])
				}
			}
			q.pos = 0
		} else {
			q.active = false
			q.pos = len(q.order) - 1
			return queueItem{}, false
		}
	}
	return q.currentLocked()
}

// prev is a manual skip backward. At the front it wraps to the end when
// repeatAll is set, otherwise stays on the first track.
func (q *playQueue) prev() (queueItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.active {
		return queueItem{}, false
	}
	q.pos--
	if q.pos < 0 {
		if q.repeat == repeatAll {
			q.pos = len(q.order) - 1
		} else {
			q.pos = 0
		}
	}
	return q.currentLocked()
}

// setShuffle turns shuffle on or off, keeping the current track playing. The
// not-yet-played remainder is reshuffled (on) or restored to source order (off).
func (q *playQueue) setShuffle(on bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.shuffle == on || len(q.items) == 0 {
		q.shuffle = on
		return
	}
	cur := 0
	if q.active && q.pos >= 0 && q.pos < len(q.order) {
		cur = q.order[q.pos]
	}
	q.shuffle = on
	q.buildOrder(cur)
	if !on {
		q.pos = cur // sequential order is identity, so pos == item index
	} else {
		q.pos = 0
	}
}

func (q *playQueue) setRepeat(m repeatMode) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.repeat = m
}

func (q *playQueue) clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items, q.order, q.pos, q.active = nil, nil, 0, false
}

// queueSnapshot is the JSON-friendly view of the queue for GET /api/queue and
// the desktop UI.
type queueSnapshot struct {
	Active  bool            `json:"active"`
	Pos     int             `json:"pos"` // index of the current item within Items
	Shuffle bool            `json:"shuffle"`
	Repeat  string          `json:"repeat"`
	Items   []queueSnapItem `json:"items"`
}

type queueSnapItem struct {
	Title string `json:"title"`
	Art   string `json:"art,omitempty"`
}

func (q *playQueue) snapshot() queueSnapshot {
	q.mu.Lock()
	defer q.mu.Unlock()
	snap := queueSnapshot{
		Active:  q.active,
		Shuffle: q.shuffle,
		Repeat:  q.repeat.String(),
		Pos:     -1,
	}
	snap.Items = make([]queueSnapItem, len(q.items))
	for i, it := range q.items {
		snap.Items[i] = queueSnapItem{Title: it.Title, Art: it.Art}
	}
	if q.active && q.pos >= 0 && q.pos < len(q.order) {
		snap.Pos = q.order[q.pos]
	}
	return snap
}
