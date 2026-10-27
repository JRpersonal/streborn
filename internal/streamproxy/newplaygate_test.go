package streamproxy

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

func rawReq(ctx context.Context, url string) *http.Request {
	u := "/stream/raw?u=" + base64.RawURLEncoding.EncodeToString([]byte(url))
	return httptest.NewRequest(http.MethodGet, u, nil).WithContext(ctx)
}

const (
	oldStationURL = "https://old.example/live.mp3"
	newStationURL = "https://new.example/live.mp3"
)

// Arming closes the held station connection at once: handing it back is what
// made the resumed station audible the instant the speaker came up (#1557,
// "continuing on the held connection").
func TestNewPlayGateArmDropsTheHeldConnection(t *testing.T) {
	t.Parallel()
	s := New(presets.New(), silentLogger())
	var closed atomic.Bool
	s.held = &heldConn{station: oldStationURL, closeFn: func() { closed.Store(true) }, done: make(chan struct{})}
	s.ArmNewPlayGate(NewPlayTarget{URLs: []string{newStationURL}}, time.Minute)
	if !closed.Load() || s.held != nil {
		t.Fatal("the held station connection survived the arm")
	}
}

// While the gate is closed, the station the speaker resumed by itself gets
// silent audio, not its stream and not an error.
func TestNewPlayGateSilencesAFetchThatIsNotTheTarget(t *testing.T) {
	t.Parallel()
	s := New(presets.New(), silentLogger())
	s.ArmNewPlayGate(NewPlayTarget{URLs: []string{newStationURL}}, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	w := httptest.NewRecorder()
	s.handleRaw(w, rawReq(ctx, oldStationURL))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("status %d, type %q, want 200 audio/mpeg", w.Code, w.Header().Get("Content-Type"))
	}
	body := w.Body.Bytes()
	if len(body) < 8*len(silenceFrame) || len(body)%len(silenceFrame) != 0 {
		t.Fatalf("body is %d bytes, want whole silent frames", len(body))
	}
	if !s.NewPlayGateClosed() {
		t.Fatal("a fetch of the old station opened the gate")
	}
}

// A preset slot the speaker resumes natively is silenced the same way.
func TestNewPlayGateSilencesANonTargetSlot(t *testing.T) {
	t.Parallel()
	store := presets.New()
	s := New(store, silentLogger())
	s.ArmNewPlayGate(NewPlayTarget{Slot: 3, URLs: []string{newStationURL}}, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	w := httptest.NewRecorder()
	s.handle(w, httptest.NewRequest(http.MethodGet, "/stream/1", nil).WithContext(ctx))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("slot 1: status %d, type %q, want silence", w.Code, w.Header().Get("Content-Type"))
	}
}

// The new play's own fetch opens the gate and takes its normal path (here an
// empty slot, so the normal 404 rather than silence).
func TestNewPlayGatePassesTheTargetAndOpens(t *testing.T) {
	t.Parallel()
	s := New(presets.New(), silentLogger())
	s.ArmNewPlayGate(NewPlayTarget{Slot: 3}, time.Minute)
	w := httptest.NewRecorder()
	s.handle(w, httptest.NewRequest(http.MethodGet, "/stream/3", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("target slot: status %d, want the normal path's 404", w.Code)
	}
	if s.NewPlayGateClosed() {
		t.Fatal("the target's fetch did not open the gate")
	}
}

func TestNewPlayTargetMatches(t *testing.T) {
	t.Parallel()
	tg := NewPlayTarget{Slot: 2, URLs: []string{" " + newStationURL + " "}}
	for _, c := range []struct {
		slot int
		url  string
		want bool
	}{
		{2, "", true},
		{0, newStationURL, true},
		{5, newStationURL, true},
		{1, oldStationURL, false},
		{0, "", false},
	} {
		if got := tg.matches(c.slot, c.url); got != c.want {
			t.Errorf("matches(%d, %q) = %v, want %v", c.slot, c.url, got, c.want)
		}
	}
	if !(NewPlayTarget{URLs: []string{" "}}).Empty() || (NewPlayTarget{Slot: 1}).Empty() {
		t.Error("Empty misjudges a target")
	}
}

// Opening the gate (a failed play) ends the silence it is feeding, so the
// speaker is not left silent behind a play that did not happen.
func TestNewPlayGateReleaseEndsTheSilence(t *testing.T) {
	t.Parallel()
	s := New(presets.New(), silentLogger())
	gen := s.ArmNewPlayGate(NewPlayTarget{URLs: []string{newStationURL}}, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.handleRaw(httptest.NewRecorder(), rawReq(ctx, oldStationURL))
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	s.ReleaseNewPlayGate(gen, "the new play failed")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the silence ran on after the gate opened")
	}
}

// Nothing keeps the gate closed for ever: the hold runs out, and after the
// play went out the grace for its fetch does.
func TestNewPlayGateTimesOut(t *testing.T) {
	t.Parallel()
	s := New(presets.New(), silentLogger())
	s.ArmNewPlayGate(NewPlayTarget{URLs: []string{newStationURL}}, 30*time.Millisecond)
	waitGateOpen(t, s)

	gen := s.ArmNewPlayGate(NewPlayTarget{URLs: []string{newStationURL}}, time.Minute)
	s.NewPlaySent(gen, 30*time.Millisecond)
	waitGateOpen(t, s)
}

func waitGateOpen(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for s.NewPlayGateClosed() {
		if time.Now().After(deadline) {
			t.Fatal("the gate never opened")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// An earlier play's caller cannot open a later play's gate, and a play on an
// awake speaker opens only a gate whose play already went out.
func TestNewPlayGateGenerationsAndSentRelease(t *testing.T) {
	t.Parallel()
	s := New(presets.New(), silentLogger())
	old := s.ArmNewPlayGate(NewPlayTarget{Slot: 1}, time.Minute)
	s.ArmNewPlayGate(NewPlayTarget{Slot: 2}, time.Minute)
	s.ReleaseNewPlayGate(old, "stale")
	s.NewPlaySent(old, time.Millisecond)
	if !s.NewPlayGateClosed() {
		t.Fatal("a stale generation opened the newer gate")
	}
	s.ReleaseSentNewPlayGate("another play")
	if !s.NewPlayGateClosed() {
		t.Fatal("a gate whose play is still starting was opened by another play")
	}
	s.AddNewPlayTarget(newStationURL)
	if hold, _ := s.newPlayGateHolds(0, newStationURL); hold {
		t.Fatal("an added target was held")
	}
	gen := s.ArmNewPlayGate(NewPlayTarget{Slot: 2}, time.Minute)
	s.NewPlaySent(gen, time.Minute)
	s.ReleaseSentNewPlayGate("another play")
	if s.NewPlayGateClosed() {
		t.Fatal("a gate left waiting for its fetch silenced the next play")
	}
}
