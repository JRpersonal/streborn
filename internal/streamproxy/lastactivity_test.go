package streamproxy

import (
	"testing"
	"time"
)

// A radio stream holds one long GET for its entire playback, so the bare
// open-time stamp read as stale after minutes of flawless audio and the
// spontaneous-off recovery stood down (#491 Cinemate, 2026-08-01). While a
// connection is open, activity must read as "now"; after it closes, as the
// close moment; and only a connection-less proxy may look stale.
func TestLastActivityTracksOpenConnections(t *testing.T) {
	s := &Server{}

	done := s.noteFetchOpen()
	// Simulate a connection opened long ago and still serving.
	s.fetchMu.Lock()
	s.lastFetch = time.Now().Add(-10 * time.Minute)
	s.fetchMu.Unlock()
	if got, _ := s.LastActivity(); time.Since(got) > time.Second {
		t.Fatalf("open connection must read as fresh activity, got %v ago", time.Since(got))
	}

	done()
	if got, _ := s.LastActivity(); time.Since(got) > time.Second {
		t.Fatalf("a just-closed connection must read as fresh activity, got %v ago", time.Since(got))
	}

	// With nothing open, an old stamp must stay old.
	s.fetchMu.Lock()
	s.lastFetch = time.Now().Add(-10 * time.Minute)
	s.fetchMu.Unlock()
	if got, _ := s.LastActivity(); time.Since(got) < 5*time.Minute {
		t.Fatalf("idle proxy must not fake freshness, got %v ago", time.Since(got))
	}
}
