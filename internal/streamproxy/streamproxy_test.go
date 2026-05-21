package streamproxy

import (
	"io"
	"log/slog"
	"testing"
)

// silentLogger discards all log output so tests stay quiet.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestShouldLogFailDeduplicatesWithinWindow(t *testing.T) {
	s := New(nil, silentLogger())

	const url = "http://stream.example.com/dead.mp3"
	if !s.shouldLogFail(url) {
		t.Fatalf("first call must return true, got false")
	}
	if s.shouldLogFail(url) {
		t.Fatalf("second call within window must return false")
	}
	if s.shouldLogFail(url) {
		t.Fatalf("third call within window must return false")
	}

	other := "http://stream.example.com/another.mp3"
	if !s.shouldLogFail(other) {
		t.Fatalf("a different url within the window must still log")
	}
}

func TestShouldLogFailResetsAfterSuccessfulReachClear(t *testing.T) {
	s := New(nil, silentLogger())

	const url = "http://stream.example.com/sometimes.mp3"
	if !s.shouldLogFail(url) {
		t.Fatalf("first call must return true")
	}
	if s.shouldLogFail(url) {
		t.Fatalf("repeat must return false")
	}

	// Simulate a successful reach that clears the dedup entry, the
	// same code path streamOne uses just before forwarding headers.
	s.failMu.Lock()
	delete(s.lastFail, url)
	s.failMu.Unlock()

	if !s.shouldLogFail(url) {
		t.Fatalf("after clear, the next failure must WARN again")
	}
}
