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

func newTitleServer() *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestClearTitleOnEndDropsTheServedStreamsTitle(t *testing.T) {
	s := newTitleServer()
	s.setTitle("http://radio/a", "The Beatles - Help!")
	s.clearTitleOnEnd("http://radio/a")
	if got := s.CurrentTitle(); got != "" {
		t.Fatalf("title after the stream ended = %q, want empty", got)
	}
}

func TestClearTitleOnEndLeavesASuccessorAlone(t *testing.T) {
	s := newTitleServer()
	s.setTitle("http://radio/a", "Old Song")
	// The box switched stations: the new handler already owns the title.
	s.clearTitleForNewURL("http://radio/b")
	s.setTitle("http://radio/b", "New Song")
	// The OLD handler returns late; it must not wipe the successor.
	s.clearTitleOnEnd("http://radio/a")
	if got := s.CurrentTitle(); got != "New Song" {
		t.Fatalf("title = %q, want the successor's %q", got, "New Song")
	}
}

func TestReconnectSameURLRefillsAfterEndClear(t *testing.T) {
	s := newTitleServer()
	s.setTitle("http://radio/a", "Song One")
	s.clearTitleOnEnd("http://radio/a")
	// Box re-fetches the same stream (flap): the URL is unchanged, so the
	// pre-metadata window shows nothing rather than a stale song, and the
	// next metadata block refills.
	s.clearTitleForNewURL("http://radio/a")
	if got := s.CurrentTitle(); got != "" {
		t.Fatalf("title in the pre-metadata window = %q, want empty", got)
	}
	s.setTitle("http://radio/a", "Song Two")
	if got := s.CurrentTitle(); got != "Song Two" {
		t.Fatalf("title after refill = %q, want %q", got, "Song Two")
	}
}
