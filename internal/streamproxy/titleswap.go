// titleswap.go: the per-station "artist and title are the wrong way round"
// flag.
//
// Most stations send their ICY StreamTitle as "Artist - Title", and everything
// downstream (the speaker panel, the app's now-playing line, Recently played,
// the phone remote) reads it that way. Some send "Title - Artist" instead (the
// case that asked for this: NDR 1 Radio MV, "Sultans of swing - Dire
// Straits"), and there is no reliable way to tell the two apart from the text.
// So the listener tells us, per station, and the proxy turns that station's
// titles round right where it parses them. Every consumer reads the title from
// here (the /api/stream/title endpoint and the onTitle callback), so this is
// the one place the correction has to happen.
//
// The flag is keyed on the station URL the proxy files the title under (the
// stream the user picked, not the edge it resolved to), kept in a small JSON
// file on NAND and only written when it actually changes.

package streamproxy

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/JRpersonal/streborn/internal/atomicfile"
)

// defaultTitleSwapPath is the NAND file holding the flagged stations.
const defaultTitleSwapPath = "/mnt/nv/streborn/title-swap.json"

// maxTitleSwapStations caps the list so a broken client cannot grow a file on
// the speaker's flash without limit. A listener flags a handful of stations.
const maxTitleSwapStations = 200

// maxTitleSwapURL bounds one station key.
const maxTitleSwapURL = 2048

// ErrTitleSwapNoStation is returned when no station was named and nothing
// is being proxied right now.
var ErrTitleSwapNoStation = errors.New("no station playing")

// ErrTitleSwapBadStation is returned for a station key that is too long.
var ErrTitleSwapBadStation = errors.New("station url too long")

// ErrTitleSwapFull is returned when the list is at its cap.
var ErrTitleSwapFull = errors.New("too many stations flagged")

// titleSwapStore is the in-memory copy of the flag file, loaded on first use.
type titleSwapStore struct {
	mu     sync.Mutex
	path   string // "" = defaultTitleSwapPath; a test points it elsewhere
	loaded bool
	set    map[string]bool
}

type titleSwapFile struct {
	Stations []string `json:"stations"`
}

func (t *titleSwapStore) filePath() string {
	if t.path != "" {
		return t.path
	}
	return defaultTitleSwapPath
}

// loadLocked reads the file once. A missing or damaged file reads as empty:
// a cosmetic setting must never get in the way of playback.
func (t *titleSwapStore) loadLocked() {
	if t.loaded {
		return
	}
	t.loaded = true
	t.set = map[string]bool{}
	b, err := os.ReadFile(t.filePath())
	if err != nil {
		return
	}
	var f titleSwapFile
	if json.Unmarshal(b, &f) != nil {
		return
	}
	for _, u := range f.Stations {
		if u = strings.TrimSpace(u); u != "" && len(t.set) < maxTitleSwapStations {
			t.set[u] = true
		}
	}
}

func (t *titleSwapStore) has(station string) bool {
	if station == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.loadLocked()
	return t.set[station]
}

func (t *titleSwapStore) list() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.loadLocked()
	out := make([]string, 0, len(t.set))
	for u := range t.set {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// put sets or clears the flag and writes the file, only when that changed
// something.
func (t *titleSwapStore) put(station string, on bool) (changed bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.loadLocked()
	if t.set[station] == on {
		return false, nil
	}
	if on && len(t.set) >= maxTitleSwapStations {
		return false, ErrTitleSwapFull
	}
	next := make([]string, 0, len(t.set)+1)
	for u := range t.set {
		if u != station {
			next = append(next, u)
		}
	}
	if on {
		next = append(next, station)
	}
	sort.Strings(next)
	b, err := json.Marshal(titleSwapFile{Stations: next})
	if err != nil {
		return false, err
	}
	if err := atomicfile.WriteFile(t.filePath(), b, 0o644); err != nil {
		return false, err
	}
	if on {
		t.set[station] = true
	} else {
		delete(t.set, station)
	}
	return true, nil
}

// swapStreamTitle turns a "Title - Artist" StreamTitle into the "Artist -
// Title" form every consumer reads. It cuts at the LAST dash: a song title
// carries " - " far more often than an artist name does ("Song - Remastered
// 2011 - Artist"), so the tail is the artist. The readers take " / " as
// "Title / Artist" when the title has no dash; on a flagged station it is the
// other way round, so "Artist / Title" comes back as "Artist - Title". A dash
// wins over a slash, as in the readers: a slash is also part of artist names
// ("More Than You Know - Axwell / Ingrosso"). A title with no separator is
// returned as it is.
func swapStreamTitle(s string) string {
	s = strings.TrimSpace(s)
	best, bestLen := -1, 0
	for _, sep := range []string{" - ", " – ", " — "} {
		if i := strings.LastIndex(s, sep); i > best {
			best, bestLen = i, len(sep)
		}
	}
	if best > 0 {
		left, right := strings.TrimSpace(s[:best]), strings.TrimSpace(s[best+bestLen:])
		if left != "" && right != "" {
			return right + " - " + left
		}
		return s
	}
	if i := strings.Index(s, " / "); i > 0 {
		left, right := strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+3:])
		if left != "" && right != "" {
			return left + " - " + right
		}
	}
	return s
}

// SetTitleSwapPath points the flag store at path instead of the NAND file.
// For tests; call before the first title arrives.
func (s *Server) SetTitleSwapPath(path string) {
	s.swap.mu.Lock()
	s.swap.path, s.swap.loaded, s.swap.set = path, false, nil
	s.swap.mu.Unlock()
}

// shownTitle is what consumers get for station's raw StreamTitle.
func (s *Server) shownTitle(station, raw string) string {
	if raw == "" || !s.swap.has(station) {
		return raw
	}
	return swapStreamTitle(raw)
}

// TitleSwapped reports whether station's titles are turned round. An empty
// station means the one being proxied right now.
func (s *Server) TitleSwapped(station string) bool {
	if station == "" {
		station = s.currentTitleStation()
	}
	return s.swap.has(station)
}

// TitleSwapStations lists the flagged stations, sorted.
func (s *Server) TitleSwapStations() []string { return s.swap.list() }

func (s *Server) currentTitleStation() string {
	s.titleMu.Lock()
	defer s.titleMu.Unlock()
	return s.curTitleURL
}

// SetTitleSwap flags (on) or unflags station. An empty station means the one
// being proxied right now. When that is the station playing, the live title is
// re-read at once, so the app and the speaker show the corrected song without
// waiting for the next one. Returns the station it acted on and the title now
// shown.
func (s *Server) SetTitleSwap(station string, on bool) (string, string, error) {
	station = strings.TrimSpace(station)
	if station == "" {
		station = s.currentTitleStation()
	}
	if station == "" {
		return "", "", ErrTitleSwapNoStation
	}
	if len(station) > maxTitleSwapURL {
		return "", "", ErrTitleSwapBadStation
	}
	changed, err := s.swap.put(station, on)
	if err != nil {
		return station, s.CurrentTitle(), err
	}
	if changed {
		s.logger.Info("stream proxy title swap", "station", station, "swap", on)
		s.reapplyTitle(station)
	}
	return station, s.CurrentTitle(), nil
}

// reapplyTitle re-derives the live title from the raw one after station's
// flag changed, and hands a changed title to onTitle like a new song.
func (s *Server) reapplyTitle(station string) {
	s.titleMu.Lock()
	if s.curTitleURL != station || s.curRawTitle == "" {
		s.titleMu.Unlock()
		return
	}
	raw := s.curRawTitle
	s.titleMu.Unlock()
	shown := s.shownTitle(station, raw)
	s.titleMu.Lock()
	if s.curTitleURL != station || s.curRawTitle != raw || s.curTitle == shown {
		s.titleMu.Unlock()
		return
	}
	s.curTitle = shown
	cb := s.onTitle
	s.titleMu.Unlock()
	if cb != nil {
		cb(shown)
	}
}
