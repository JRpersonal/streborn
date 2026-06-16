// Package recent keeps a small, capped "recently played" history on the box for
// the desktop app's Recently-played view (#135), a cloud-free replacement for
// the SoundTouch app's Recently Played list.
//
// Design constraints (the boxes are tiny: single/dual-core ARM, little RAM,
// unreliable NAND), see the App-First / box-resource-budget direction:
//
//   - The ring lives in RAM and is hard-capped (maxEntries). No unbounded
//     growth, ever (that is the OOM-reboot class of bug).
//   - NAND is the scarcest resource. We do NOT write on every play/track change
//     (radio ICY titles change every few minutes; writing each would wear the
//     flash). Instead Add() only marks the store dirty and arms a single
//     debounce timer; the actual recent.json write happens at most once per
//     flushDelay, coalescing a burst of track changes into one write. Flush()
//     is also called on graceful shutdown so the tail survives a clean reboot.
//   - The agent only appends in-RAM and serves the list verbatim on
//     GET /api/recent. All merging across boxes, dedup and the source-card
//     grouping happen in the desktop app, not here.
//
// Entries are stored oldest-first. Consecutive entries that share a CardKey are
// one "source card" (a listening session); a different CardKey starts a new
// card. The app groups them; the box just records the sequence.
package recent

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// maxEntries caps the ring. #135 specifies "last 30 tracks across all sources".
// 30 short entries is a few KB of JSON, negligible on the ~31 MB NAND.
const maxEntries = 30

// flushDelay is how long Add() waits before persisting a dirty ring to NAND.
// A burst of track changes within this window collapses into a single write.
const flushDelay = 90 * time.Second

// Entry is one track-play, carrying the metadata of the source card it belongs
// to so the app can group and replay without a second lookup. Track-level rows
// for the same card repeat the card fields; that redundancy keeps the box logic
// trivial and the file self-contained.
type Entry struct {
	TS       string `json:"ts"`                // RFC3339, when this play/track started
	Source   string `json:"source"`            // "radio" | "spotify" | "upnp" | "airplay" | ...
	CardKey  string `json:"cardKey"`           // stable group key (replay target identity)
	CardName string `json:"cardName"`          // station / playlist / folder name
	CardArt  string `json:"cardArt,omitempty"` // logo / cover URL
	CardURL  string `json:"cardURL,omitempty"` // replay target: stream URL / spotify URI / NAS location
	Track    string `json:"track,omitempty"`   // song / track title; empty for stations without ICY
	Account  string `json:"account,omitempty"` // sourceAccount (e.g. which Spotify account)
	Homepage string `json:"homepage,omitempty"` // station website, for the "website" link (radio)
}

// Store is the in-RAM ring plus its debounced NAND backing file.
type Store struct {
	path string

	mu    sync.Mutex
	data  []Entry // oldest-first, len <= maxEntries
	dirty bool
	timer *time.Timer // non-nil while a debounced flush is pending
}

// New returns an empty store with no persistence path (tests / dev builds).
func New() *Store { return &Store{} }

// Load reads recent.json from path. A missing or empty file yields an empty
// store and no error (first boot). A parse error yields an empty store plus the
// error so the caller can log and continue rather than crash the agent.
func Load(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("recent read: %w", err)
	}
	if len(b) == 0 {
		return s, nil
	}
	// Preferred format: {"recent":[...]}. Also accept a bare array so a
	// hand-edited or older file still loads.
	var wrap struct {
		Recent []Entry `json:"recent"`
	}
	if err := json.Unmarshal(b, &wrap); err == nil && wrap.Recent != nil {
		s.data = capTail(wrap.Recent)
		return s, nil
	}
	var arr []Entry
	if err := json.Unmarshal(b, &arr); err == nil {
		s.data = capTail(arr)
		return s, nil
	}
	return s, fmt.Errorf("recent parse: unknown format in %s", path)
}

// capTail keeps only the newest maxEntries of in (which is oldest-first).
func capTail(in []Entry) []Entry {
	if len(in) <= maxEntries {
		return in
	}
	return append([]Entry(nil), in[len(in)-maxEntries:]...)
}

// Add records a play/track. It coalesces against the newest entry so STR's own
// re-points and repeated ICY titles do not create duplicate rows:
//
//   - same card + same (or empty) track: refresh the timestamp and fill in any
//     newly-known metadata in place; no new row.
//   - same card + a genuinely new track: append a track row.
//   - a different card: append (starts a new card).
//
// It never writes to NAND synchronously; it only marks dirty and arms the
// debounce timer (see flushDelay). Safe to call from the WebSocket reader / the
// ICY title callback: it does not block on I/O.
func (s *Store) Add(e Entry) {
	if e.CardKey == "" || e.Source == "" {
		return // nothing identifiable to group on; ignore transient/blank states
	}
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if n := len(s.data); n > 0 {
		last := &s.data[n-1]
		if last.CardKey == e.CardKey {
			// Fold into the current row unless this is a genuinely new, distinct
			// track: a card-start placeholder (last.Track == "") gets filled, a
			// repeat or a metadata-only refresh updates in place. Only when both
			// tracks are non-empty and differ do we record a new track row.
			if last.Track == "" || e.Track == "" || e.Track == last.Track {
				last.TS = e.TS
				if e.Track != "" {
					last.Track = e.Track
				}
				if last.CardArt == "" && e.CardArt != "" {
					last.CardArt = e.CardArt
				}
				if last.CardName == "" && e.CardName != "" {
					last.CardName = e.CardName
				}
				if last.CardURL == "" && e.CardURL != "" {
					last.CardURL = e.CardURL
				}
				if last.Account == "" && e.Account != "" {
					last.Account = e.Account
				}
				s.markDirtyLocked()
				return
			}
			// Same session, a new distinct track within it: fall through, append.
		}
	}

	s.data = append(s.data, e)
	if len(s.data) > maxEntries {
		s.data = s.data[len(s.data)-maxEntries:]
	}
	s.markDirtyLocked()
}

// markDirtyLocked flags the ring as needing a write and arms a single debounce
// timer if one is not already pending. Caller must hold s.mu.
func (s *Store) markDirtyLocked() {
	s.dirty = true
	if s.path == "" || s.timer != nil {
		return
	}
	s.timer = time.AfterFunc(flushDelay, func() {
		s.mu.Lock()
		s.timer = nil
		s.mu.Unlock()
		_ = s.Flush()
	})
}

// Clear empties the ring. The desktop app's "clear recently played" action calls
// this; it marks dirty so the empty state is persisted (the caller Flush()es so a
// user-initiated clear survives an immediate reboot rather than waiting out the
// debounce). No-op on an already-empty ring.
func (s *Store) Clear() {
	s.mu.Lock()
	if len(s.data) > 0 {
		s.data = nil
		s.markDirtyLocked()
	}
	s.mu.Unlock()
}

// DeleteCard removes every entry whose CardKey matches key, i.e. one card /
// listening session in the app's grouped view, and returns how many rows were
// removed. Marks dirty when something changed (caller Flush()es). No-op for an
// empty key or no match.
func (s *Store) DeleteCard(key string) int {
	if key == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]Entry, 0, len(s.data))
	removed := 0
	for _, e := range s.data {
		if e.CardKey == key {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	if removed > 0 {
		s.data = kept
		s.markDirtyLocked()
	}
	return removed
}

// DeleteCardAt removes the ONE card the user clicked: the maximal run of
// consecutive entries with CardKey == key that contains the entry timestamped ts
// (cards in the app's view are exactly such consecutive runs). It deletes only
// that single listening session, NOT every other session of the same station
// elsewhere in the ring, which is what DeleteCard(key) did and why deleting one
// entry wiped older same-station entries too. A non-matching (key, ts) is a no-op,
// so a stale/empty id can never fall through to clearing the whole ring. Returns
// the number of rows removed.
func (s *Store) DeleteCardAt(key, ts string) int {
	if key == "" || ts == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, e := range s.data {
		if e.CardKey == key && e.TS == ts {
			idx = i
			break
		}
	}
	if idx < 0 {
		return 0
	}
	lo, hi := idx, idx
	for lo > 0 && s.data[lo-1].CardKey == key {
		lo--
	}
	for hi < len(s.data)-1 && s.data[hi+1].CardKey == key {
		hi++
	}
	s.data = append(s.data[:lo:lo], s.data[hi+1:]...)
	s.markDirtyLocked()
	return hi - lo + 1
}

// All returns a copy of the ring, oldest-first. The /api/recent handler serves
// this; the app reverses/groups it.
func (s *Store) All() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.data))
	copy(out, s.data)
	return out
}

// Flush writes the ring to NAND if dirty, atomically (temp file + rename). It is
// a no-op when clean or path-less. Called by the debounce timer and once more on
// graceful shutdown so the tail survives a clean reboot.
func (s *Store) Flush() error {
	s.mu.Lock()
	if !s.dirty || s.path == "" {
		s.mu.Unlock()
		return nil
	}
	b, err := json.MarshalIndent(struct {
		Recent []Entry `json:"recent"`
	}{Recent: s.data}, "", "  ")
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("recent marshal: %w", err)
	}
	s.dirty = false
	s.mu.Unlock()

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		s.mu.Lock()
		s.dirty = true // write failed; re-arm on the next Add
		s.mu.Unlock()
		return fmt.Errorf("recent write: %w", err)
	}
	return os.Rename(tmp, s.path)
}
