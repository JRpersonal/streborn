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

// TestSlotPulledSinceLiveness pins the recall verify's success signal against
// the field failure: the box's re-login source bounce opens the slot stream
// for 36ms-2.4s and drops it, which must NOT count as playback, while a
// still-open connection or a sustained (>= minSustainedFetch) session must.
func TestSlotPulledSinceLiveness(t *testing.T) {
	s := New(presets.New(), silentLogger())
	anchor := time.Now().Add(-time.Minute)

	if s.SlotPulledSince(2, anchor) {
		t.Fatal("no fetch at all must not read as pulled")
	}

	// Dead fetch: opened and closed within the bounce (well under sustain).
	s.noteSlotFetch(2)
	s.noteSlotFetchDone(2)
	if s.SlotPulledSince(2, anchor) {
		t.Fatal("a fetch that died right after opening must not certify the recall")
	}

	// Open connection: playback in progress.
	s.noteSlotFetch(3)
	if !s.SlotPulledSince(3, anchor) {
		t.Fatal("an open connection after the press must count as playback")
	}
	// Fetch opened BEFORE the press proves nothing about this recall.
	if s.SlotPulledSince(3, time.Now().Add(time.Second)) {
		t.Fatal("a fetch opened before the anchor must not count")
	}
	s.noteSlotFetchDone(3)

	// Sustained session that already ended still counts (box played, then
	// reconnect churn closed the socket moments before the verify tick).
	s.fetchMu.Lock()
	s.slotFetch[4] = time.Now().Add(-10 * time.Second)
	s.slotFetchEnd[4] = time.Now()
	s.fetchMu.Unlock()
	if !s.SlotPulledSince(4, time.Now().Add(-time.Minute)) {
		t.Fatal("a sustained finished session must count as playback")
	}
}

// TestUpstreamStallForcesReconnect pins the #510 stall watchdog: an upstream
// that goes silent WITHOUT closing (no FIN, no RST) used to block the copy
// loop in Read() forever, starving the box on an open, byte-less connection
// (live 2026-08-18: an ST20 in BUFFERING_STATE for minutes). The watchdog has
// to close the upstream after a few silent seconds so the normal reconnect
// path takes over while the box's own connection stays open.
func TestUpstreamStallForcesReconnect(t *testing.T) {
	stallRelease := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 4096))
		w.(http.Flusher).Flush()
		<-stallRelease // stall: connection stays open, no further bytes
	}))
	defer func() { close(stallRelease); up.Close() }()

	s := New(presets.New(), silentLogger())
	// The production client refuses loopback dials (SSRF guard); the watchdog
	// under test sits below the client, so a plain client keeps the test local.
	s.client = &http.Client{}

	req := httptest.NewRequest(http.MethodGet, "/raw", nil)
	rw := httptest.NewRecorder()
	start := time.Now()
	boseAlive, err := s.streamOne(req.Context(), rw, req, up.URL, true)
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("stall took %s to detect; the watchdog should fire after ~5s", elapsed)
	}
	if !boseAlive {
		t.Fatalf("streamOne reported the box gone; a stalled UPSTREAM must ask for a reconnect instead")
	}
	if err == nil {
		t.Fatalf("expected the stall-closed read error, got nil")
	}
	if rw.Body.Len() == 0 {
		t.Fatalf("the bytes before the stall never reached the client")
	}
}
