package streamproxy

import (
	"io"
	"log/slog"
	"testing"
)

// The proxy's CurrentTitle must go empty once the proxy stops carrying the
// stream. It used to report the LAST stream's song forever, and a client that
// cannot tell radio-via-proxy from other playback showed it under whatever
// came next: a NAS track plays over the same UPnP source as proxied radio, so
// the old radio song sat under it (SA-5, #274, still with v0.9.52).
//
// The wipe is DELAYED and takeover-aware since the v0.9.53 stutter loop
// (#119): tests exercise wipeTitleIfUnclaimed, the synchronous half behind
// clearTitleOnEnd's grace timer.

func newTitleServer() *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// genOf snapshots what clearTitleOnEnd would capture for url at handler end.
func genOf(s *Server, url string) uint64 {
	s.titleMu.Lock()
	defer s.titleMu.Unlock()
	return s.titleGens[url]
}

func TestClearTitleOnEndDropsTheServedStreamsTitle(t *testing.T) {
	s := newTitleServer()
	s.noteStreamStart("http://radio/a")
	s.setTitle("http://radio/a", "The Beatles - Help!")
	// The handler ends and nothing takes the stream over: the delayed wipe
	// must land.
	s.wipeTitleIfUnclaimed("http://radio/a", genOf(s, "http://radio/a"))
	if got := s.CurrentTitle(); got != "" {
		t.Fatalf("title after the stream ended = %q, want empty", got)
	}
}

func TestClearTitleOnEndLeavesASuccessorAlone(t *testing.T) {
	s := newTitleServer()
	s.noteStreamStart("http://radio/a")
	s.setTitle("http://radio/a", "Old Song")
	gen := genOf(s, "http://radio/a")
	// The box switched stations: the new handler already owns the title.
	s.noteStreamStart("http://radio/b")
	s.clearTitleForNewURL("http://radio/b")
	s.setTitle("http://radio/b", "New Song")
	// The OLD handler's delayed wipe fires; it must not touch the successor.
	s.wipeTitleIfUnclaimed("http://radio/a", gen)
	if got := s.CurrentTitle(); got != "New Song" {
		t.Fatalf("title = %q, want the successor's %q", got, "New Song")
	}
}

// The v0.9.53 stutter loop (#119): with the on-display track push enabled the
// box re-fetches the SAME stream after every push. The old handler's wipe must
// be cancelled by the successor's takeover, so the unchanged title does NOT
// count as a change and cannot re-fire the push (which would re-buffer the box
// and start the cycle again).
func TestSameURLTakeoverCancelsTheWipeAndDoesNotRefire(t *testing.T) {
	s := newTitleServer()
	fired := 0
	s.onTitle = func(string) { fired++ }
	s.noteStreamStart("http://radio/a")
	s.setTitle("http://radio/a", "Song A")
	if fired != 1 {
		t.Fatalf("first title fired %d times, want 1", fired)
	}
	gen := genOf(s, "http://radio/a")
	// Box drops and re-fetches the same stream: successor starts, old ends.
	s.noteStreamStart("http://radio/a")
	s.wipeTitleIfUnclaimed("http://radio/a", gen) // stale wipe must be a no-op
	if got := s.CurrentTitle(); got != "Song A" {
		t.Fatalf("title after takeover = %q, want it kept", got)
	}
	// The successor's first metadata block carries the same song.
	s.setTitle("http://radio/a", "Song A")
	if fired != 1 {
		t.Fatalf("unchanged title re-fired the push (%d times), the stutter loop is back", fired)
	}
}

func TestReconnectSameURLRefillsAfterEndClear(t *testing.T) {
	s := newTitleServer()
	s.noteStreamStart("http://radio/a")
	s.setTitle("http://radio/a", "Song One")
	// The stream truly ended (no successor), the wipe landed.
	s.wipeTitleIfUnclaimed("http://radio/a", genOf(s, "http://radio/a"))
	// Much later the box fetches the same stream again: the pre-metadata
	// window shows nothing rather than a stale song, then metadata refills.
	s.noteStreamStart("http://radio/a")
	s.clearTitleForNewURL("http://radio/a")
	if got := s.CurrentTitle(); got != "" {
		t.Fatalf("title in the pre-metadata window = %q, want empty", got)
	}
	s.setTitle("http://radio/a", "Song Two")
	if got := s.CurrentTitle(); got != "Song Two" {
		t.Fatalf("title after refill = %q, want %q", got, "Song Two")
	}
}
