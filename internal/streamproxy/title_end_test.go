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

// genOf reads the CURRENT generation for url. Every test below captures it
// before a successor starts, which is what production does now; capturing it at
// handler END, which is what production used to do, is the bug the mirror-group
// test further down pins.
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

// A mirror group: every member fetches the SAME proxy URL, so several handlers
// share one generation counter. When one member is switched off its handler
// ends while the others are still playing, and the wipe must not touch the
// title they are showing.
//
// This is the case the old code got wrong, because it read the generation when
// the handler ENDED rather than when it started: by then the other members had
// already bumped the counter, so the ending handler matched the live value and
// blanked the track on every remaining member. Every other test here captured
// the generation before the successor, which is why none of them caught it.
func TestOneMemberLeavingAGroupKeepsTheOthersTitle(t *testing.T) {
	s := newTitleServer()
	const url = "http://192.0.2.1:8888/stream/raw?u=abc"

	// Three members start on the same stream, as a group does.
	genFirst := s.noteStreamStart(url)
	s.noteStreamStart(url)
	s.noteStreamStart(url)
	s.setTitle(url, "The Offspring - Want You Bad")

	// The first member is switched off. Its handler ends and its delayed wipe
	// fires with the generation IT was given.
	s.wipeTitleIfUnclaimed(url, genFirst)

	if got := s.CurrentTitle(); got != "The Offspring - Want You Bad" {
		t.Fatalf("title after one member left = %q, want the others' track kept", got)
	}
}

// And the last member leaving still clears it: the fix must not turn the wipe
// off, only aim it correctly.
func TestTheLastMemberLeavingStillClearsTheTitle(t *testing.T) {
	s := newTitleServer()
	const url = "http://192.0.2.1:8888/stream/raw?u=abc"

	s.noteStreamStart(url)
	genLast := s.noteStreamStart(url)
	s.setTitle(url, "Nickelback - Someday")

	// The first member goes; nothing changes.
	s.wipeTitleIfUnclaimed(url, genLast-1)
	if got := s.CurrentTitle(); got == "" {
		t.Fatal("the title went empty while a member was still playing")
	}
	// The last one goes: now it must clear.
	s.wipeTitleIfUnclaimed(url, genLast)
	if got := s.CurrentTitle(); got != "" {
		t.Fatalf("title after the last member left = %q, want empty", got)
	}
}
