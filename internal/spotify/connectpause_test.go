// connectpause_test.go: the Spotify-app pause vs ServeOgg attach-resume
// interplay (Klaus' field case, 2026-08). The user pauses in the Spotify app,
// the starved box drains its buffer and re-fetches the Ogg stream, and the
// attach-resume used to restart the very playback the user just paused; the
// resume's own-command stamp then swallowed the user's NEXT pause as an echo,
// so the loop never ended. These tests pin the gate that breaks the loop and
// the paths that must stay exactly as they were.

package spotify

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newAttachTestManager returns a Manager whose go-librespot API is a local
// fake counting /player/resume posts, with a binary stub so Ready() lets
// ServeOgg serve.
func newAttachTestManager(t *testing.T) (*Manager, *int32) {
	t.Helper()
	var resumes int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/player/resume" {
			atomic.AddInt32(&resumes, 1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(api.Close)
	bin := filepath.Join(t.TempDir(), "engine")
	if err := os.WriteFile(bin, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(bin, filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.apiAddr = strings.TrimPrefix(api.URL, "http://")
	return m, &resumes
}

// serveReattach runs one ServeOgg request to completion as a RE-fetch: a
// previous consumer is attached when the request arrives, exactly what the
// box's buffer-drain re-fetch after a pause looks like. The previous attach is
// spaced outside the storm window so backoff bookkeeping stays out of the
// picture, and the request context arrives already cancelled so the handler
// returns right after the resume decision (the only part under test).
func serveReattach(t *testing.T, m *Manager) {
	t.Helper()
	m.mu.Lock()
	m.sink = &closeNotifyWriter{w: io.Discard, done: make(chan struct{})}
	m.lastAttachAt = time.Now().Add(-time.Minute)
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/spotify/stream", nil).WithContext(ctx)
	m.ServeOgg(httptest.NewRecorder(), req)
}

// A real Spotify-app pause followed by the box's re-fetch must NOT resume the
// engine: that resume is what restarted Klaus' playback seconds after every
// pause. Failed before the gate existed.
func TestAttachAfterAppPauseDoesNotResume(t *testing.T) {
	m, resumes := newAttachTestManager(t)
	m.SetConnectIntentHooks(func(string) {}, func() {})
	// The pause arrives as a paused event outside the own-command window, the
	// same path production takes.
	m.handleEnginePlaybackEnd("paused")
	if !m.connectPauseStands() {
		t.Fatal("a real Spotify-app pause did not stamp lastConnectPauseAt")
	}
	serveReattach(t, m)
	if n := atomic.LoadInt32(resumes); n != 0 {
		t.Fatalf("box re-fetch after a Spotify-app pause resumed the engine %d time(s); the pause was defeated", n)
	}
}

// With no recent pause the attach-resume must fire exactly as before the gate:
// it is what revives a drain-paused engine when the box comes back with nobody
// recalling.
func TestAttachResumeStillFiresWhenNoPauseSeen(t *testing.T) {
	m, resumes := newAttachTestManager(t)
	serveReattach(t, m)
	if n := atomic.LoadInt32(resumes); n != 1 {
		t.Fatalf("idle re-attach posted %d resumes, want 1 (behaviour must be unchanged without a pause)", n)
	}
}

// A hardware preset recall must still attach-resume despite a fresh pause
// stamp: the cross-account recall leaves the engine paused in its restart gap
// and the attach-resume is what unsticks it (the #45 class the resume exists
// for). A recall is also exactly how a paused user asks for music again.
func TestRecallAttachStillResumesDespitePause(t *testing.T) {
	m, resumes := newAttachTestManager(t)
	m.SetConnectIntentHooks(func(string) {}, func() {})
	m.handleEnginePlaybackEnd("paused")
	m.SetRecalling()
	m.mu.Lock()
	m.recallRestartAt = time.Now() // cross-account SwitchAccount restarted the engine
	m.mu.Unlock()
	serveReattach(t, m)
	if n := atomic.LoadInt32(resumes); n != 1 {
		t.Fatalf("cross-account recall re-attach posted %d resumes, want 1 (recall must win over the pause stamp)", n)
	}
}

// A same-account recall attach never resumed (Play drives the new track) and
// must not start doing so, pause stamp or not: resuming there replayed the OLD
// track over the new one (ST30 5 to 4 switch, 2026-07-14).
func TestSameAccountRecallAttachStillDefersToPlay(t *testing.T) {
	m, resumes := newAttachTestManager(t)
	m.SetRecalling()
	serveReattach(t, m)
	if n := atomic.LoadInt32(resumes); n != 0 {
		t.Fatalf("same-account recall re-attach posted %d resumes, want 0", n)
	}
}

// The pause stamp expires: a re-fetch beyond connectPauseHoldWindow behaves
// exactly as before the gate, so a stale stamp can never park the attach-resume
// for good.
func TestConnectPauseStampExpires(t *testing.T) {
	m, resumes := newAttachTestManager(t)
	m.mu.Lock()
	m.lastConnectPauseAt = time.Now().Add(-connectPauseHoldWindow - time.Second)
	m.mu.Unlock()
	if m.connectPauseStands() {
		t.Fatal("an expired pause stamp still stands")
	}
	serveReattach(t, m)
	if n := atomic.LoadInt32(resumes); n != 1 {
		t.Fatalf("re-attach after stamp expiry posted %d resumes, want 1", n)
	}
}

// Playback demonstrably starting again (playing/active event) lifts the gate
// early: the user pressed play, so the next attach must resume as always.
func TestEnginePlaybackStartClearsPauseStamp(t *testing.T) {
	m, resumes := newAttachTestManager(t)
	m.SetConnectIntentHooks(func(string) {}, func() {})
	m.handleEnginePlaybackEnd("paused")
	m.handleEnginePlaybackStart()
	if m.connectPauseStands() {
		t.Fatal("playback start did not clear the pause stamp")
	}
	serveReattach(t, m)
	if n := atomic.LoadInt32(resumes); n != 1 {
		t.Fatalf("re-attach after playback restarted posted %d resumes, want 1", n)
	}
}

// The attach-resume must not open the own-command echo window: Klaus' next
// pause, pressed within 15 s of the box re-attaching, was dropped as STR's own
// echo, so the stop latch never armed and the loop started over. Failed before
// ServeOgg switched to the unstamped resume.
func TestAttachResumeDoesNotSwallowTheNextPause(t *testing.T) {
	m, resumes := newAttachTestManager(t)
	var pauses []string
	m.SetConnectIntentHooks(func(ev string) { pauses = append(pauses, ev) }, func() {})
	serveReattach(t, m)
	if n := atomic.LoadInt32(resumes); n != 1 {
		t.Fatalf("setup: idle re-attach posted %d resumes, want 1", n)
	}
	// The user pauses right after the attach-resume.
	m.handleEnginePlaybackEnd("paused")
	if len(pauses) != 1 {
		t.Fatalf("a pause right after the attach-resume armed the stop latch %d time(s), want 1 (it was misread as STR's own echo)", len(pauses))
	}
}
