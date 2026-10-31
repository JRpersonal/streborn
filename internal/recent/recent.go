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
//     GET /api/recent. Merging across boxes and the source-card grouping
//     happen in the desktop app and the phone page, not here.
//
// Entries are stored oldest-first. Consecutive entries that share a CardKey are
// one "source card" (a listening session); a different CardKey starts a new
// card. Each CardKey appears in at most ONE such run: when a station comes back,
// its older session is dropped and the new one is appended at the end, so the
// list shows every station once, at the time it was last heard. Doing it here
// keeps the ring from filling up with the same few stations, and every consumer
// (desktop app, phone page, older desktop builds) gets the one-card-per-station
// view without its own dedup. Load applies the same rule, so a ring written by
// an older agent loses its duplicates on the next boot.
package recent

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/atomicfile"
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
	TS       string `json:"ts"`                 // RFC3339, when this play/track started
	Source   string `json:"source"`             // "radio" | "spotify" | "upnp" | "airplay" | ...
	CardKey  string `json:"cardKey"`            // stable group key (replay target identity)
	CardName string `json:"cardName"`           // station / playlist / folder name
	CardArt  string `json:"cardArt,omitempty"`  // logo / cover URL
	CardURL  string `json:"cardURL,omitempty"`  // replay target: stream URL / spotify URI / NAS location
	Mime     string `json:"mime,omitempty"`     // network-library file MIME, so a replay from history plays direct, not through the radio stream proxy (#817)
	Track    string `json:"track,omitempty"`    // song / track title; empty for stations without ICY
	Account  string `json:"account,omitempty"`  // sourceAccount (e.g. which Spotify account)
	Homepage string `json:"homepage,omitempty"` // station website, for the "website" link (radio)
	// DurationSec is the length of the replay target (CardURL) for a
	// network-library file, 0 when unknown (radio, Spotify, rows written before
	// it existed). A replay hands it to the speaker the way a Library play does;
	// without it the speaker reports no total and the app draws no progress bar
	// (#845).
	DurationSec int `json:"durationSec,omitempty"`
	// ItemType is the ContentItem type of a Pandora station ("stationurl",
	// ...), so a replay from history can rebuild the item when the agent no
	// longer holds the speaker's station list (#1101). Empty for every other
	// source.
	ItemType string `json:"itemType,omitempty"`
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
		s.data = capTail(collapse(heal(wrap.Recent)))
		return s, nil
	}
	var arr []Entry
	if err := json.Unmarshal(b, &arr); err == nil {
		s.data = capTail(collapse(heal(arr)))
		return s, nil
	}
	return s, fmt.Errorf("recent parse: unknown format in %s", path)
}

// heal corrects rows an older agent wrote with the wrong source. A spotify:
// URI is never a radio stream, yet the play-a-URL path used to file one under
// "radio" when a client pushed it there, and the fold then kept that label for
// the real Spotify recalls of the same card. Such a row is a Spotify card.
func heal(in []Entry) []Entry {
	for i := range in {
		if in[i].Source == "radio" && strings.HasPrefix(in[i].CardKey, "spotify:") {
			in[i].Source = "spotify"
		}
	}
	return in
}

// capTail keeps only the newest maxEntries of in (which is oldest-first).
func capTail(in []Entry) []Entry {
	if len(in) <= maxEntries {
		return in
	}
	return append([]Entry(nil), in[len(in)-maxEntries:]...)
}

// collapse keeps, for every CardKey, only its NEWEST run of consecutive entries
// (the last listening session), preserving the order of what is kept. A ring
// that already satisfies the one-run-per-key rule comes back unchanged. Used on
// Load, so a recent.json written before the rule existed loses its duplicates.
func collapse(in []Entry) []Entry {
	// Walk newest-first: the first run seen for a key is its newest session.
	done := map[string]bool{}
	keep := make([]bool, len(in))
	for i := len(in) - 1; i >= 0; {
		key := in[i].CardKey
		j := i
		for j >= 0 && in[j].CardKey == key {
			j--
		}
		if !done[key] {
			done[key] = true
			for k := j + 1; k <= i; k++ {
				keep[k] = true
			}
		}
		i = j
	}
	out := make([]Entry, 0, len(in))
	for i, e := range in {
		if keep[i] {
			out = append(out, e)
		}
	}
	return out
}

// Add records a play/track. It coalesces against the newest entry so STR's own
// re-points and repeated ICY titles do not create duplicate rows:
//
//   - same card + same (or empty) track: refresh the timestamp and fill in any
//     newly-known metadata in place; no new row.
//   - same card + a genuinely new track: append a track row.
//   - a different card: append (starts a new card). An older session of that
//     same card is removed first, so a station is listed once, at its newest
//     play, with the tracks of that newest session only.
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
		// A row only folds into the newest one when both the card AND its
		// source match. The same key under another source is a new session
		// that replaces the old one: a Spotify playlist once filed as "radio"
		// (a spotify: URI pushed through the play-a-URL path) otherwise kept
		// that label for every later key recall, because the fold never
		// touched Source.
		if last.CardKey == e.CardKey && last.Source == e.Source {
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
				// Mime and Homepage were the two the fold never filled, and Mime
				// is the one that decides how a replay PLAYS: without it the
				// track goes through the endless-radio relay instead of being
				// fetched from the media server. A card written once without it
				// therefore stayed broken for good, even when the very next play
				// of the same card carried it (#817).
				if last.Mime == "" && e.Mime != "" {
					last.Mime = e.Mime
				}
				if last.Homepage == "" && e.Homepage != "" {
					last.Homepage = e.Homepage
				}
				if last.DurationSec == 0 && e.DurationSec > 0 {
					last.DurationSec = e.DurationSec
				}
				if last.ItemType == "" && e.ItemType != "" {
					last.ItemType = e.ItemType
				}
				s.markDirtyLocked()
				return
			}
			// Same session, a new distinct track within it: fall through, append.
		} else {
			// A new session starts. Drop the station's older session so it is
			// listed once; those track rows belonged to the older listening.
			s.dropKeyLocked(e.CardKey)
		}
	}

	s.data = append(s.data, e)
	if len(s.data) > maxEntries {
		s.data = s.data[len(s.data)-maxEntries:]
	}
	s.markDirtyLocked()
}

// dropKeyLocked removes every entry with CardKey == key and reports how many
// went. Caller must hold s.mu; it does not mark dirty, the caller does.
func (s *Store) dropKeyLocked(key string) int {
	kept := make([]Entry, 0, len(s.data))
	for _, e := range s.data {
		if e.CardKey != key {
			kept = append(kept, e)
		}
	}
	removed := len(s.data) - len(kept)
	if removed > 0 {
		s.data = kept
	}
	return removed
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

// DeleteCard removes every entry whose CardKey matches key, i.e. one station's
// card in the app's view, and returns how many rows were removed. Marks dirty
// when something changed (caller Flush()es). No-op for an empty key or no match.
func (s *Store) DeleteCard(key string) int {
	if key == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := s.dropKeyLocked(key)
	if removed > 0 {
		s.markDirtyLocked()
	}
	return removed
}

// DeleteCardAt removes the card the user clicked: the station whose CardKey is
// key, identified by the timestamp ts of one of its rows. A station is stored as
// a single run (see collapse and Add), so that run is the whole station and the
// station leaves the list. The (key, ts) pair must still match a stored row: a
// stale or empty id is a no-op and never falls through to clearing the ring or
// touching another station (the bug where deleting one entry wiped others).
// Returns the number of rows removed.
func (s *Store) DeleteCardAt(key, ts string) int {
	if key == "" || ts == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, e := range s.data {
		if e.CardKey == key && e.TS == ts {
			found = true
			break
		}
	}
	if !found {
		return 0
	}
	removed := s.dropKeyLocked(key)
	s.markDirtyLocked()
	return removed
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

	// Durable write (fsync + rename): a plain write+rename can leave the file at
	// 0 bytes after a speaker's standby power-cut.
	if err := atomicfile.WriteFile(s.path, b, 0o644); err != nil {
		s.mu.Lock()
		s.dirty = true // write failed; re-arm on the next Add
		s.mu.Unlock()
		return fmt.Errorf("recent write: %w", err)
	}
	return nil
}
