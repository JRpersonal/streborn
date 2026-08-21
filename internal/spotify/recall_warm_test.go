// Tests for the warm same-context recall fast path and the recall boundary
// cut (the 20+s same-preset re-press, live Portable 2026-08-21). The
// mockLibrespot harness has no sink, so Streaming() is false there and every
// pre-existing test keeps exercising the cold path untouched; the warm tests
// inject a fake sink + first-audio stamp to make the stream count as flowing.

package spotify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// makeWarm makes m look like a box is attached and audibly streaming the
// given context: exactly the state a same-preset re-press finds.
func makeWarm(m *Manager, ctxURI string) {
	m.mu.Lock()
	m.lastContext = ctxURI
	m.sink = io.Discard
	m.sinkAttachedAt = time.Now()
	m.sinkFirstAudioAt = time.Now()
	m.mu.Unlock()
}

// TestPlayWarmShuffleFastPath: re-pressing the playing shuffle preset must
// skip the paused-reload staging entirely (no /player/play) and instead
// reseed the shuffle order (off->on), skip once, and ensure playback - with
// the recall cut left armed so the /player/next BOS is cut, not flushed.
func TestPlayWarmShuffleFastPath(t *testing.T) {
	m, calls, cleanup := mockLibrespot(t)
	defer cleanup()
	const ctxURI = "spotify:playlist:abc"
	makeWarm(m, ctxURI)
	m.ArmRecallCut() // the recall entry point arms before its PlayURLMime

	if err := m.Play(context.Background(), ctxURI, PlayOptions{Shuffle: true}); err != nil {
		t.Fatalf("Play: %v", err)
	}

	want := []string{"/player/shuffle_context", "/player/shuffle_context", "/player/next", "/player/resume"}
	got := pathsOf(*calls)
	if len(got) != len(want) {
		t.Fatalf("warm shuffle calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("warm shuffle calls = %v, want %v", got, want)
		}
	}
	if !strings.Contains((*calls)[0].body, `"shuffle_context":false`) ||
		!strings.Contains((*calls)[1].body, `"shuffle_context":true`) {
		t.Errorf("warm shuffle must reseed off-then-on, got %q then %q", (*calls)[0].body, (*calls)[1].body)
	}
	if !m.skipCutArmed() {
		t.Error("the recall cut must stay armed so the /player/next BOS drops the old tail")
	}
}

// TestPlayWarmResumeClearsCut: re-pressing the playing resume preset only
// ensures shuffle-off playback (no reload, no skip). No BOS will arrive, so
// the cut the entry point armed MUST be disarmed, or it would drop the live
// audio for the whole 30s window.
func TestPlayWarmResumeClearsCut(t *testing.T) {
	m, calls, cleanup := mockLibrespot(t)
	defer cleanup()
	const ctxURI = "spotify:playlist:abc"
	makeWarm(m, ctxURI)
	m.ArmRecallCut()

	if err := m.Play(context.Background(), ctxURI, PlayOptions{Shuffle: false}); err != nil {
		t.Fatalf("Play: %v", err)
	}

	for _, p := range pathsOf(*calls) {
		if p == "/player/play" || p == "/player/next" {
			t.Errorf("warm resume re-press must not reload or skip, saw %s", p)
		}
	}
	shufBody, ok := bodyForPath(*calls, "/player/shuffle_context")
	if !ok || !strings.Contains(shufBody, `"shuffle_context":false`) {
		t.Errorf("warm resume must ensure shuffle OFF, got %q ok=%v", shufBody, ok)
	}
	if m.skipCutArmed() {
		t.Error("warm resume must clear the recall cut (no BOS is coming)")
	}
}

// TestPlayColdPathArmsCutBeforePost: a different context (or, below, no sink)
// must keep today's staged cold reload, with the cut armed by the time the
// /player/play POST goes out so the box never re-buffers the old track during
// the load.
func TestPlayColdPathArmsCutBeforePost(t *testing.T) {
	var (
		mu          sync.Mutex
		mgr         *Manager
		sawPlay     bool
		armedAtPost bool
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			_, _ = w.Write([]byte(`{"username":"u","track":{"uri":"spotify:track:cur","name":"Cur"}}`))
			return
		}
		if r.URL.Path == "/player/play" {
			mu.Lock()
			sawPlay = true
			armedAtPost = mgr.skipCutArmed()
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	m := New("", filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.apiAddr = strings.TrimPrefix(ts.URL, "http://")
	mu.Lock()
	mgr = m
	mu.Unlock()
	makeWarm(m, "spotify:playlist:OLD") // streaming, but a DIFFERENT context

	if err := m.Play(context.Background(), "spotify:playlist:NEW", PlayOptions{Shuffle: false}); err != nil {
		t.Fatalf("Play: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !sawPlay {
		t.Fatal("a cross-context recall must take the cold path (/player/play)")
	}
	if !armedAtPost {
		t.Error("the recall cut must be armed before the /player/play POST")
	}
}

// TestPlaySameContextNoSinkTakesColdPath: the warm shortcut requires audio to
// actually be flowing to a box; the same context with no sink attached (box
// detached, engine paused) must still get the full staged reload.
func TestPlaySameContextNoSinkTakesColdPath(t *testing.T) {
	m, calls, cleanup := mockLibrespot(t)
	defer cleanup()
	const ctxURI = "spotify:playlist:abc"
	m.mu.Lock()
	m.lastContext = ctxURI // same context, but no sink
	m.mu.Unlock()

	if err := m.Play(context.Background(), ctxURI, PlayOptions{Shuffle: false}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	if _, ok := bodyForPath(*calls, "/player/play"); !ok {
		t.Fatal("same context with no sink must reload via /player/play")
	}
}

// TestPlayPostFailureDisarmsCut: when the cold path's /player/play fails, no
// track boundary will ever arrive, so the armed cut must be cleared or it
// silently drops whatever is still playing for up to 30s.
func TestPlayPostFailureDisarmsCut(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			_, _ = w.Write([]byte(`{"username":"u","track":{"uri":"spotify:track:cur","name":"Cur"}}`))
			return
		}
		if r.URL.Path == "/player/play" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	m := New("", filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.apiAddr = strings.TrimPrefix(ts.URL, "http://")

	if err := m.Play(context.Background(), "spotify:playlist:abc", PlayOptions{}); err == nil {
		t.Fatal("Play must surface the /player/play failure")
	}
	if m.skipCutArmed() {
		t.Error("a failed /player/play must disarm the recall cut")
	}
}

// TestPlayAccountAbortDisarmsCut: PlayAccount bailing out before /player/play
// (no live session, no credential) is the other no-BOS exit and must disarm
// the cut the recall entry point armed.
func TestPlayAccountAbortDisarmsCut(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "cfg")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	m := New("", cfg, "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.apiAddr = "127.0.0.1:0" // nothing listening: no live session
	m.ArmRecallCut()

	err := m.PlayAccount(context.Background(), "spotify:playlist:abc", "", PlayOptions{})
	if !errors.Is(err, ErrNoSpotifySession) {
		t.Fatalf("PlayAccount = %v, want ErrNoSpotifySession", err)
	}
	if m.skipCutArmed() {
		t.Error("an aborted recall must disarm the recall cut (no BOS is coming)")
	}
}

// TestPlayAccountWarmSingleStatusProbe: the warm recall preamble must cost
// exactly ONE /status round trip (the old shape probed three times, each a
// potential 5s timeout on a slow engine).
func TestPlayAccountWarmSingleStatusProbe(t *testing.T) {
	var (
		mu          sync.Mutex
		statusCount int
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			mu.Lock()
			statusCount++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"username":"u","track":{"uri":"spotify:track:cur","name":"Cur"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	m := New("", filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.apiAddr = strings.TrimPrefix(ts.URL, "http://")
	const ctxURI = "spotify:playlist:abc"
	makeWarm(m, ctxURI)

	if err := m.PlayAccount(context.Background(), ctxURI, "", PlayOptions{Shuffle: true}); err != nil {
		t.Fatalf("PlayAccount: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if statusCount != 1 {
		t.Errorf("warm PlayAccount made %d /status probes, want exactly 1", statusCount)
	}
}

// TestRecallCutWindowAndStaleSnapshot covers the mechanics behind the recall
// cut: the 30s arm that only ever extends, and the boundary-time snapshot of
// how much stale (non-header) audio the attachment received, which gates the
// recall re-push (~0 = the cut kept the box clean, no push needed).
func TestRecallCutWindowAndStaleSnapshot(t *testing.T) {
	m := New("", filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// A skip's 15s window is extended to the recall's 30s...
	m.NoteSkip()
	m.ArmRecallCut()
	m.mu.Lock()
	until := m.skipCutUntil
	m.mu.Unlock()
	if time.Until(until) < 29*time.Second {
		t.Errorf("ArmRecallCut after NoteSkip left %v, want ~30s", time.Until(until))
	}
	// ...and a re-arm never SHRINKS a longer window.
	m.mu.Lock()
	m.skipCutUntil = time.Now().Add(40 * time.Second)
	m.mu.Unlock()
	m.ArmRecallCut()
	m.mu.Lock()
	until = m.skipCutUntil
	m.mu.Unlock()
	if time.Until(until) < 39*time.Second {
		t.Errorf("ArmRecallCut shrank an armed window to %v", time.Until(until))
	}

	// Boundary snapshot: sink bytes minus the header pages, in KB.
	m.mu.Lock()
	m.headerPages = make([]byte, 4096)
	m.sinkBytes = 300*1024 + 4096
	m.mu.Unlock()
	m.noteSkipBoundary()
	if got := m.LastBoundaryStaleKB(); got != 300 {
		t.Errorf("LastBoundaryStaleKB = %d, want 300 (headers subtracted)", got)
	}
	if m.LastSkipBoundary().IsZero() {
		t.Error("noteSkipBoundary must stamp the boundary time")
	}

	// Clamped at zero when only header bytes (or nothing) flowed.
	m.mu.Lock()
	m.sinkBytes = 1024 // less than the header pages
	m.mu.Unlock()
	m.noteSkipBoundary()
	if got := m.LastBoundaryStaleKB(); got != 0 {
		t.Errorf("LastBoundaryStaleKB = %d, want 0 (never negative)", got)
	}
}
