package webui

import (
	"testing"
)

// The queue watcher decides to advance from a now_playing poll, then blocks on
// boxCmdMu. A user starting a NEW queue holds that lock through wake+push and
// bumps queueGen; the stale advance must then abort once it gets the lock, or
// it advances the NEW queue and cuts off the first track the user just chose.
func TestStaleQueueAdvanceStandsDown(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.queue.load([]queueItem{
		{URL: "http://192.0.2.10:50002/a.mp3", Title: "A", Mime: "audio/mpeg"},
		{URL: "http://192.0.2.10:50002/b.mp3", Title: "B", Mime: "audio/mpeg"},
	}, 0, false, repeatOff)
	s.setQueueTiming(0) // the freshly pushed first track's generation

	// An advance decided against a PREVIOUS generation (the watcher's poll
	// predates the new queue) must do nothing: no push, no position move.
	base := rec.count()
	s.advanceAndPlay(true, 0)
	if got := rec.count(); got != base {
		t.Fatalf("stale advance pushed a stream to the box (%d SOAP calls)", got-base)
	}
	if pos := s.queue.snapshot().Pos; pos != 0 {
		t.Fatalf("stale advance moved the queue to pos %d, want 0", pos)
	}

	// The same advance with the CURRENT generation proceeds.
	s.queueMu.Lock()
	gen := s.queueGen
	s.queueMu.Unlock()
	s.advanceAndPlay(true, gen)
	if pos := s.queue.snapshot().Pos; pos != 1 {
		t.Fatalf("current-generation advance: queue pos = %d, want 1", pos)
	}
	if rec.count() == base {
		t.Error("current-generation advance never pushed the next track")
	}
}
