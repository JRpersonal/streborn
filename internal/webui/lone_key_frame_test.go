package webui

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// A phantom userActivityUpdate (#419 Finding 3) must not cost the user their
// stream: the conservative power-off handling still runs, but what was playing
// is armed for a deferred resume so it returns when the speaker is switched on.
func TestPowerOffArmsDeferredResumeFromLastPlay(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.lastPlay = &lastPlayInfo{
		boxURL: "http://192.0.2.10:8888/stream/3",
		title:  "Test Station",
		mime:   "audio/mpeg",
		ts:     time.Now(),
	}
	s.armDeferredResumeFromLastPlay("unit test")
	s.deferredMu.Lock()
	d := s.deferred
	s.deferredMu.Unlock()
	if d == nil {
		t.Fatal("a deferred resume must be armed so the stream returns on the next power-on")
	}
	if d.boxURL != "http://192.0.2.10:8888/stream/3" || d.title != "Test Station" {
		t.Fatalf("armed the wrong stream: %+v", d)
	}
}

// With nothing playing there is nothing to restore, and arming must stay a
// no-op rather than queueing an empty resume.
func TestPowerOffWithNothingPlayingArmsNothing(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.armDeferredResumeFromLastPlay("unit test")
	s.deferredMu.Lock()
	d := s.deferred
	s.deferredMu.Unlock()
	if d != nil {
		t.Fatalf("nothing was playing, so nothing may be armed, got %+v", d)
	}
}
