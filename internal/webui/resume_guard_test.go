package webui

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

// newGuardTestServer returns a Server with a persisted last-play target, ready
// for the auto-resume guard calls, persisting to a temp last-play.json.
func newGuardTestServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		lastPlayPath: filepath.Join(t.TempDir(), "last-play.json"),
	}
	s.setLastPlay("http://127.0.0.1:8888/stream/3", "Station", "", "")
	return s
}

// TestAutoResumeGuardTripsAndSurvivesRestart is the #381 crash loop: each boot
// auto-resumes the persisted stream, the stream crashes the box, the watchdog
// reboots. The attempt count must trip the guard after maxAutoResumeAttempts
// rapid attempts AND survive the reboot (a fresh Server loading the same
// last-play.json must still be blocked), or the loop never ends.
func TestAutoResumeGuardTripsAndSurvivesRestart(t *testing.T) {
	s := newGuardTestServer(t)

	for i := 1; i <= maxAutoResumeAttempts; i++ {
		if s.autoResumeBlocked() {
			t.Fatalf("guard blocked after %d attempts, must allow %d", i-1, maxAutoResumeAttempts)
		}
		s.noteAutoResumeAttempt()
	}
	if !s.autoResumeBlocked() {
		t.Fatalf("guard not blocked after %d rapid attempts", maxAutoResumeAttempts)
	}

	// The "reboot": a fresh agent process loads the same NAND file.
	s2 := &Server{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		lastPlayPath: s.lastPlayPath,
	}
	s2.loadLastPlay()
	if !s2.autoResumeBlocked() {
		t.Fatal("guard state lost across an agent restart; the crash loop would continue")
	}
}

// TestAutoResumeGuardDecay: attempts far enough apart are not a crash loop.
// A stale count must neither block nor accumulate - the next attempt counts
// from 1 again, so a box whose crash was transient heals itself.
func TestAutoResumeGuardDecay(t *testing.T) {
	s := newGuardTestServer(t)
	s.lastPlayMu.Lock()
	s.resumeAttempts = maxAutoResumeAttempts
	s.lastResumeAt = time.Now().Add(-autoResumeAttemptWindow - time.Minute)
	s.lastPlayMu.Unlock()

	if s.autoResumeBlocked() {
		t.Fatal("a tripped count older than the window must not block anymore")
	}
	s.noteAutoResumeAttempt()
	s.lastPlayMu.Lock()
	got := s.resumeAttempts
	s.lastPlayMu.Unlock()
	if got != 1 {
		t.Fatalf("attempt after the decay window counted as %d, want a fresh 1", got)
	}
}

// TestSetLastPlayRearmsAutoResume: any manual play (preset key, app, queue) is
// an explicit "play this" and must clear a tripped guard, in memory and on
// NAND.
func TestSetLastPlayRearmsAutoResume(t *testing.T) {
	s := newGuardTestServer(t)
	for i := 0; i < maxAutoResumeAttempts; i++ {
		s.noteAutoResumeAttempt()
	}
	if !s.autoResumeBlocked() {
		t.Fatal("guard should be tripped before the manual play")
	}

	s.setLastPlay("http://127.0.0.1:8888/stream/5", "Other station", "", "")
	if s.autoResumeBlocked() {
		t.Fatal("a manual play must re-arm the auto-resume")
	}
	// And persisted: the next boot must not think it is still blocked.
	s2 := &Server{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		lastPlayPath: s.lastPlayPath,
	}
	s2.loadLastPlay()
	if s2.autoResumeBlocked() {
		t.Fatal("re-armed guard must be persisted, or the next boot skips the resume for no reason")
	}
}

// TestClearResumeAttemptsIfQuiet: the stability reset only fires for the
// attempt it was armed for. A NEWER attempt in between means a reboot DID
// happen and its count must survive.
func TestClearResumeAttemptsIfQuiet(t *testing.T) {
	s := newGuardTestServer(t)
	s.noteAutoResumeAttempt()
	s.lastPlayMu.Lock()
	first := s.lastResumeAt
	s.lastPlayMu.Unlock()

	// A newer attempt (must land on a different timestamp for the guard's
	// identity check, so nudge it explicitly rather than sleeping).
	s.lastPlayMu.Lock()
	s.resumeAttempts++
	s.lastResumeAt = first.Add(time.Second)
	second := s.lastResumeAt
	s.lastPlayMu.Unlock()

	s.clearResumeAttemptsIfQuiet(first) // stale timer: must be a no-op
	s.lastPlayMu.Lock()
	got := s.resumeAttempts
	s.lastPlayMu.Unlock()
	if got != 2 {
		t.Fatalf("stale stability timer cleared a newer attempt count: got %d, want 2", got)
	}

	s.clearResumeAttemptsIfQuiet(second) // current timer: playback proved stable
	s.lastPlayMu.Lock()
	got = s.resumeAttempts
	s.lastPlayMu.Unlock()
	if got != 0 {
		t.Fatalf("stability timer did not clear the count: got %d, want 0", got)
	}
}
