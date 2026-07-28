// Tests for the silent-stream detector: a box that holds the Ogg connection
// open but never receives an audio page must NOT count as streaming, because
// that state used to satisfy the recall verify while the user saw a spinner
// and the box gave up 30 s later (field 2026-07-27).

package spotify

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func newStallTestManager() *Manager {
	return &Manager{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestStreamStalledNoSink(t *testing.T) {
	m := newStallTestManager()
	if m.StreamStalled() {
		t.Fatal("no attached box can never be a stall")
	}
}

func TestStreamStalledAfterSilence(t *testing.T) {
	m := newStallTestManager()
	m.sink = io.Discard
	m.sinkAttachedAt = time.Now().Add(-stallAfter - time.Second)
	if !m.StreamStalled() {
		t.Fatal("an attached box with no audio page past the window is a stall")
	}
}

func TestStreamNotStalledWhenAudioFlowed(t *testing.T) {
	m := newStallTestManager()
	m.sink = io.Discard
	m.sinkAttachedAt = time.Now().Add(-time.Minute)
	m.sinkFirstAudioAt = time.Now().Add(-59 * time.Second)
	if m.StreamStalled() {
		t.Fatal("audio did flow on this attachment, so it is not a stall")
	}
}

func TestStreamNotStalledWithinGrace(t *testing.T) {
	m := newStallTestManager()
	m.sink = io.Discard
	m.sinkAttachedAt = time.Now()
	if m.StreamStalled() {
		t.Fatal("a fresh attachment must be given its start-up window")
	}
}

func TestOggGranule(t *testing.T) {
	page := make([]byte, 32)
	if got := oggGranule(page); got != 0 {
		t.Fatalf("header page granule = %d, want 0", got)
	}
	page[6] = 0x40 // low byte of the granule position
	if got := oggGranule(page); got != 0x40 {
		t.Fatalf("audio page granule = %d, want 64", got)
	}
	if got := oggGranule([]byte{1, 2, 3}); got != 0 {
		t.Fatalf("short page granule = %d, want 0", got)
	}
}
