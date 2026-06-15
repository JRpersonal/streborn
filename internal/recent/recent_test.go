package recent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// names returns the per-entry "CardKey/Track" so assertions read clearly.
func rows(s *Store) []string {
	out := []string{}
	for _, e := range s.All() {
		out = append(out, e.CardKey+"/"+e.Track)
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len=%d %v, want len=%d %v", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("row %d = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestAddCoalescesSameCardSameTrack guards the core box-light rule: STR's own
// re-points and repeated ICY titles for the current station must NOT create new
// rows (each row is a potential NAND write and clutters the user's history).
func TestAddCoalescesSameCardSameTrack(t *testing.T) {
	s := New()
	s.Add(Entry{Source: "radio", CardKey: "swr3", CardName: "SWR3"})                // card start, no track yet
	s.Add(Entry{Source: "radio", CardKey: "swr3", CardName: "SWR3"})                // STR re-point, identical
	s.Add(Entry{Source: "radio", CardKey: "swr3", CardName: "SWR3", Track: "Epic"}) // ICY title arrives
	s.Add(Entry{Source: "radio", CardKey: "swr3", CardName: "SWR3", Track: "Epic"}) // same title repeats
	// One card, one track row (the empty-track start was filled in place).
	eq(t, rows(s), []string{"swr3/Epic"})
}

// TestClearEmptiesRing: the user "clear list" action drops everything.
func TestClearEmptiesRing(t *testing.T) {
	s := New()
	s.Add(Entry{Source: "radio", CardKey: "swr3", Track: "Epic"})
	s.Add(Entry{Source: "spotify", CardKey: "spotify:playlist:x", Track: "Song"})
	s.Clear()
	eq(t, rows(s), []string{})
	// Clear on an already-empty ring is a no-op (no panic).
	s.Clear()
	eq(t, rows(s), []string{})
}

// TestDeleteCardRemovesOnlyThatCard: deleting one card drops all of its rows and
// leaves the rest, and returns the number removed.
func TestDeleteCardRemovesOnlyThatCard(t *testing.T) {
	s := New()
	s.Add(Entry{Source: "radio", CardKey: "swr3", Track: "Epic"})
	s.Add(Entry{Source: "radio", CardKey: "swr3", Track: "Yellow"})
	s.Add(Entry{Source: "radio", CardKey: "1live", Track: "Other"})
	if n := s.DeleteCard("swr3"); n != 2 {
		t.Fatalf("DeleteCard removed %d, want 2", n)
	}
	eq(t, rows(s), []string{"1live/Other"})
	if n := s.DeleteCard("nope"); n != 0 {
		t.Fatalf("DeleteCard(unknown) removed %d, want 0", n)
	}
	if n := s.DeleteCard(""); n != 0 {
		t.Fatalf("DeleteCard(empty) removed %d, want 0", n)
	}
}

// TestAddAppendsNewTrackSameCard: a genuinely new song under the same station
// is its own row, newest last.
func TestAddAppendsNewTrackSameCard(t *testing.T) {
	s := New()
	s.Add(Entry{Source: "radio", CardKey: "swr3", Track: "Epic"})
	s.Add(Entry{Source: "radio", CardKey: "swr3", Track: "Yellow"})
	eq(t, rows(s), []string{"swr3/Epic", "swr3/Yellow"})
}

// TestAddNewCardOnSourceSwitch: switching source starts a new card, and
// switching back is a third card (cards group CONSECUTIVE runs, per #135).
func TestAddNewCardOnSourceSwitch(t *testing.T) {
	s := New()
	s.Add(Entry{Source: "radio", CardKey: "swr3", Track: "Epic"})
	s.Add(Entry{Source: "spotify", CardKey: "spotify:playlist:chill", Track: "Orange Blossoms"})
	s.Add(Entry{Source: "radio", CardKey: "swr3", Track: "Yellow"})
	eq(t, rows(s), []string{"swr3/Epic", "spotify:playlist:chill/Orange Blossoms", "swr3/Yellow"})
}

// TestAddFillsMetadataInPlace: a card started with sparse metadata gets enriched
// (art/name/track) without spawning a new row.
func TestAddFillsMetadataInPlace(t *testing.T) {
	s := New()
	s.Add(Entry{Source: "spotify", CardKey: "ctx", CardName: ""})
	s.Add(Entry{Source: "spotify", CardKey: "ctx", CardName: "Jens Chill", CardArt: "cover.jpg", Track: "Orange Blossoms"})
	all := s.All()
	if len(all) != 1 {
		t.Fatalf("want 1 row, got %d", len(all))
	}
	if all[0].CardName != "Jens Chill" || all[0].CardArt != "cover.jpg" || all[0].Track != "Orange Blossoms" {
		t.Fatalf("metadata not filled in place: %+v", all[0])
	}
}

// TestAddIgnoresBlank: entries with no CardKey or no Source are transient/blank
// box states and must be dropped.
func TestAddIgnoresBlank(t *testing.T) {
	s := New()
	s.Add(Entry{Source: "radio", CardKey: ""})
	s.Add(Entry{Source: "", CardKey: "x"})
	if len(s.All()) != 0 {
		t.Fatalf("blank entries were recorded: %v", rows(s))
	}
}

// TestAddCapsRing: the ring never exceeds maxEntries; oldest drop off the front.
func TestAddCapsRing(t *testing.T) {
	s := New()
	for i := 0; i < maxEntries+15; i++ {
		// distinct card each time so nothing coalesces
		s.Add(Entry{Source: "radio", CardKey: "k" + itoa(i), Track: "t" + itoa(i)})
	}
	all := s.All()
	if len(all) != maxEntries {
		t.Fatalf("ring not capped: len=%d want %d", len(all), maxEntries)
	}
	// Oldest kept is index 15 (0..14 evicted).
	if all[0].CardKey != "k15" {
		t.Fatalf("wrong tail kept: front=%q want k15", all[0].CardKey)
	}
}

// TestLoadAndFlushRoundTrip: Flush writes {"recent":[...]} atomically and Load
// reads it back, capping to the newest maxEntries.
func TestLoadAndFlushRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "recent.json")
	s := &Store{path: path}
	s.Add(Entry{Source: "radio", CardKey: "swr3", Track: "Epic"})
	s.Add(Entry{Source: "radio", CardKey: "swr3", Track: "Yellow"})
	if err := s.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	// File is the {"recent":[...]} shape.
	b, _ := os.ReadFile(path)
	var wrap struct {
		Recent []Entry `json:"recent"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil || len(wrap.Recent) != 2 {
		t.Fatalf("unexpected file shape: %s", string(b))
	}
	// Load reads it back.
	s2, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	eq(t, rows(s2), []string{"swr3/Epic", "swr3/Yellow"})
}

// TestLoadMissingFileIsEmpty: first boot (no file) is not an error.
func TestLoadMissingFileIsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || len(s.All()) != 0 {
		t.Fatalf("missing file should be empty + nil err, got %d entries err=%v", len(s.All()), err)
	}
}

// TestFlushIsNoOpWhenClean: Flush without a prior Add writes nothing.
func TestFlushIsNoOpWhenClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recent.json")
	s := &Store{path: path}
	if err := s.Flush(); err != nil {
		t.Fatalf("flush clean: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("clean flush created a file")
	}
}

// itoa avoids strconv import noise in the cap test.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
