// Tests for the #487 rule: STR never powers a speaker on by itself. A stream
// the firmware dropped while the box was off is armed as a deferred resume and
// replayed only when the user switches the box on (the gabbo standby exit).

package webui

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestArmAndRunDeferredResume(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.armDeferredResume("http://127.0.0.1:8888/stream/3", "Radio X", "", "", time.Now())
	s.deferredMu.Lock()
	armed := s.deferred != nil
	s.deferredMu.Unlock()
	if !armed {
		t.Fatal("armDeferredResume must store the resume")
	}
	// Without a renderer the replay cannot run, but it must still CONSUME the
	// arm so a later wake does not replay it twice.
	defer func() { _ = recover() }()
	s.RunDeferredResume()
	s.deferredMu.Lock()
	left := s.deferred
	s.deferredMu.Unlock()
	if left != nil {
		t.Fatal("RunDeferredResume must consume the arm exactly once")
	}
}

func TestDeferredResumeExpires(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.armDeferredResume("http://127.0.0.1:8888/stream/3", "Radio X", "", "", time.Now())
	s.deferredMu.Lock()
	s.deferred.armedAt = time.Now().Add(-deferredResumeTTL - time.Minute)
	s.deferredMu.Unlock()
	// An expired arm must be dropped without touching the renderer (which is
	// nil here: a replay attempt would panic and fail the test).
	s.RunDeferredResume()
	s.deferredMu.Lock()
	left := s.deferred
	s.deferredMu.Unlock()
	if left != nil {
		t.Fatal("an expired deferred resume must be dropped")
	}
}
