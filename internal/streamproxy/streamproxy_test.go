package streamproxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
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

// TestSlotFetchStampsOnlyValidSlots covers the slot-scoped success signal for
// the hardware recall verify: a fetch of an invalid slot or of a slot with no
// playable preset must NOT stamp the per-slot time (it used to stamp the
// global one before validation, which let a 404 certify a failed recall as
// healthy, #252), while the global wedge-detector stamp keeps counting every
// box contact.
func TestSlotFetchStampsOnlyValidSlots(t *testing.T) {
	s := New(presets.New(), silentLogger())

	req := httptest.NewRequest(http.MethodGet, "/stream/2", nil)
	rw := httptest.NewRecorder()
	s.handle(rw, req)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("slot with no preset must 404, got %d", rw.Code)
	}
	if !s.LastFetchForSlot(2).IsZero() {
		t.Fatal("a 404 slot fetch must not stamp the per-slot time")
	}
	if lf, _ := s.LastActivity(); lf.IsZero() {
		t.Fatal("the global wedge stamp must still record the box contact")
	}

	req = httptest.NewRequest(http.MethodGet, "/stream/9", nil)
	rw = httptest.NewRecorder()
	s.handle(rw, req)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("invalid slot must 400, got %d", rw.Code)
	}
	if !s.LastFetchForSlot(9).IsZero() || !s.LastFetchForSlot(0).IsZero() {
		t.Fatal("out-of-range slots must never stamp")
	}

	// The direct stamp path used by handle() after validation.
	before := time.Now()
	s.noteSlotFetch(4)
	if lf := s.LastFetchForSlot(4); lf.IsZero() || lf.Before(before) {
		t.Fatal("a valid slot fetch must stamp the per-slot time")
	}
	if !s.LastFetchForSlot(3).IsZero() {
		t.Fatal("other slots must stay unstamped")
	}
}
