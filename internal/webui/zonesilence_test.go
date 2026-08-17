package webui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

// Shipped in v0.9.48 and reported the same day by the person whose log the
// guard was built from: forming a group answered "the speaker leading the group
// is not answering" while the speaker was answering fine a moment later. The
// guard asked once with a two second budget; a speaker waking up or starting a
// stream misses that easily.
//
// The fault it exists for looked nothing like that: nothing at all for twenty
// five seconds, across several reads.
func TestASlowSpeakerIsNotASilentOne(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	calls := 0
	probe := func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("context deadline exceeded")
		}
		return nil
	}
	if err := s.staysSilentVia(context.Background(), probe, 3, 0); err != nil {
		t.Errorf("a speaker that answered on the third try was called silent: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected it to keep asking, got %d calls", calls)
	}
}

// The case the guard is actually for: nothing, ever.
func TestASilentSpeakerIsReported(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	calls := 0
	probe := func(context.Context) error {
		calls++
		return errors.New("context deadline exceeded")
	}
	if err := s.staysSilentVia(context.Background(), probe, 3, 0); err == nil {
		t.Error("a speaker that never answered was treated as fine")
	}
	if calls != 3 {
		t.Errorf("expected 3 attempts, got %d", calls)
	}
}

// One answer is enough. A speaker that replies immediately must not be delayed.
func TestAnAnsweringSpeakerIsAskedOnce(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	calls := 0
	probe := func(context.Context) error { calls++; return nil }
	start := time.Now()
	if err := s.staysSilentVia(context.Background(), probe, 3, time.Second); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected one call, got %d", calls)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Error("a speaker that answers at once was made to wait")
	}
}
