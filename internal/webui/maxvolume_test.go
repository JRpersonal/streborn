package webui

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// maxVolRecorder is a speaker that records every level written to it and, like
// the real one, reports each write back as a volumeUpdated frame.
type maxVolRecorder struct {
	mu     sync.Mutex
	writes []int
}

func (r *maxVolRecorder) got() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.writes...)
}

func maxVolServer(t *testing.T) (*Server, *maxVolRecorder) {
	t.Helper()
	rec := &maxVolRecorder{}
	s := &Server{
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		maxVolumePath:    filepath.Join(t.TempDir(), "max-volume"),
		maxVolumeRecheck: 20 * time.Millisecond,
	}
	s.ownLevelSetFn = func(_ context.Context, vol int) error {
		rec.mu.Lock()
		rec.writes = append(rec.writes, vol)
		rec.mu.Unlock()
		go s.enforceMaxVolume(vol) // the speaker's own frame for the write
		return nil
	}
	return s, rec
}

func postMaxVol(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/box/max-volume", strings.NewReader(body))
	r.RemoteAddr = "192.168.1.20:5555"
	w := httptest.NewRecorder()
	s.handleMaxVolume(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func waitIdle(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.maxVolumeMu.Lock()
		busy := s.maxVolumeBusy
		s.maxVolumeMu.Unlock()
		if !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the turn-down loop did not finish")
}

// Off by default: a speaker that changes its own volume unasked is worse than
// one that does not, so nothing is capped until the owner sets a ceiling.
func TestMaxVolumeIsOffUntilItIsSet(t *testing.T) {
	s, rec := maxVolServer(t)
	if _, ok := s.maxVolume(); ok {
		t.Fatal("no file must mean off")
	}
	if got := s.capVolume(100); got != 100 {
		t.Errorf("without a ceiling 100 must stay 100, got %d", got)
	}
	s.enforceMaxVolume(100)
	waitIdle(t, s)
	if w := rec.got(); len(w) != 0 {
		t.Errorf("without a ceiling nothing may be written, got %v", w)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/box/max-volume", nil)
	r.RemoteAddr = "192.168.1.20:5555"
	w := httptest.NewRecorder()
	s.handleMaxVolume(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["volume"] != float64(0) || out["supported"] != true {
		t.Errorf("off must read as supported with volume 0, got %v", out)
	}
}

func TestMaxVolumeRoundTripsAndRejectsNonsense(t *testing.T) {
	s, _ := maxVolServer(t)
	if code, _ := postMaxVol(t, s, `{"volume":40}`); code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if v, ok := s.maxVolume(); !ok || v != 40 {
		t.Fatalf("want 40, got %d %v", v, ok)
	}
	for _, bad := range []string{`{"volume":-1}`, `{"volume":101}`} {
		if code, _ := postMaxVol(t, s, bad); code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", bad, code)
		}
	}
	if code, _ := postMaxVol(t, s, `{"volume":0}`); code != http.StatusOK {
		t.Fatalf("off: want 200, got %d", code)
	}
	if _, ok := s.maxVolume(); ok {
		t.Error("0 must switch the ceiling off")
	}
}

// Every level STR writes for a person stops at the ceiling before it reaches
// the speaker; levels below it pass untouched.
func TestCapVolumeStopsAtTheCeiling(t *testing.T) {
	s, _ := maxVolServer(t)
	postMaxVol(t, s, `{"volume":35}`)
	for in, want := range map[int]int{0: 0, 20: 20, 35: 35, 36: 35, 100: 35} {
		if got := s.capVolume(in); got != want {
			t.Errorf("capVolume(%d) = %d, want %d", in, got, want)
		}
	}
}

// A level reported above the ceiling (the speaker's own keys, the IR remote,
// the Bose app) is turned straight back down; one at or below it is left alone.
func TestMaxVolumeTurnsTheSpeakerBackDown(t *testing.T) {
	s, rec := maxVolServer(t)
	postMaxVol(t, s, `{"volume":30}`)
	s.enforceMaxVolume(30)
	s.enforceMaxVolume(12)
	waitIdle(t, s)
	if w := rec.got(); len(w) != 0 {
		t.Fatalf("levels at or below the ceiling must be left alone, got %v", w)
	}
	s.enforceMaxVolume(44)
	waitIdle(t, s)
	if w := rec.got(); len(w) != 1 || w[0] != 30 {
		t.Errorf("want one write of 30, got %v", w)
	}
}

// A held volume key is a burst of frames. They must not queue one write each
// behind the speaker's command lock, and a step that lands AFTER the write
// (the key is still held) must be caught by the re-check.
func TestMaxVolumeBurstEndsAtTheCeiling(t *testing.T) {
	s, rec := maxVolServer(t)
	postMaxVol(t, s, `{"volume":30}`)
	release := make(chan struct{})
	first := true
	s.ownLevelSetFn = func(_ context.Context, vol int) error {
		rec.mu.Lock()
		rec.writes = append(rec.writes, vol)
		wasFirst := first
		first = false
		rec.mu.Unlock()
		if wasFirst {
			<-release
			// the speaker reports the write, then the held key steps once more
			go func() { s.enforceMaxVolume(vol); s.enforceMaxVolume(33) }()
			return nil
		}
		go s.enforceMaxVolume(vol)
		return nil
	}
	s.enforceMaxVolume(31)
	time.Sleep(10 * time.Millisecond)
	for _, lvl := range []int{32, 33, 34, 35} { // frames while the write is in flight
		s.enforceMaxVolume(lvl)
	}
	close(release)
	waitIdle(t, s)
	if w := rec.got(); len(w) != 2 || w[0] != 30 || w[1] != 30 {
		t.Errorf("want exactly two writes of the ceiling (first, then the re-check), got %v", w)
	}
}

// Saving a ceiling below the current level turns the speaker down at once,
// rather than waiting for somebody to touch the volume.
func TestSavingACeilingBelowTheLevelTurnsItDown(t *testing.T) {
	s, rec := maxVolServer(t)
	s.announceVolumeFn = func(context.Context) int { return 70 }
	postMaxVol(t, s, `{"volume":25}`)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(rec.got()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	waitIdle(t, s)
	if w := rec.got(); len(w) == 0 || w[0] != 25 {
		t.Errorf("want the speaker turned down to 25, got %v", w)
	}
}

// The start level never wakes the speaker above the ceiling.
func TestStartVolumeStopsAtTheCeiling(t *testing.T) {
	s, _ := maxVolServer(t)
	s.startVolumePath = filepath.Join(t.TempDir(), "start-volume")
	postMaxVol(t, s, `{"volume":20}`)
	postStartVol(t, s, `{"volume":60}`)
	got := make(chan int, 1)
	s.setVolumeFn = func(_ context.Context, v int) error { got <- v; return nil }
	s.applyStartVolume("test")
	select {
	case v := <-got:
		if v != 20 {
			t.Errorf("start level must stop at the ceiling 20, got %d", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("start level was never applied")
	}
}
