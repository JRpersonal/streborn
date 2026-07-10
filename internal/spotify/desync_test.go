package spotify

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// Three Connect-desync markers within the window must trigger exactly one
// engine restart (upstream go-librespot #300 self-heal); further markers
// inside the rate-limit window must not restart again.
func TestDesyncSelfHeal(t *testing.T) {
	m := &Manager{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var restarts atomic.Int64
	m.runCancel = context.CancelFunc(func() { restarts.Add(1) })

	line := `time="..." level=error msg="failed receiving dealer message" error="failed to get reader"`
	m.noteLibrespotLine(line)
	m.noteLibrespotLine(line)
	if got := restarts.Load(); got != 0 {
		t.Fatalf("two markers must not restart yet, got %d", got)
	}
	m.noteLibrespotLine(`level=error msg="failed put state after update" error="context deadline exceeded"`)
	if got := restarts.Load(); got != 1 {
		t.Fatalf("third marker must trigger exactly one restart, got %d", got)
	}

	// Inside the 10-minute heal rate limit: pile on markers, no second restart.
	for i := 0; i < 6; i++ {
		m.noteLibrespotLine(line)
	}
	if got := restarts.Load(); got != 1 {
		t.Fatalf("rate limit must hold, got %d restarts", got)
	}

	// After the rate limit expires, a fresh burst heals again.
	m.mu.Lock()
	m.lastDesyncHeal = time.Now().Add(-11 * time.Minute)
	m.mu.Unlock()
	for i := 0; i < 3; i++ {
		m.noteLibrespotLine(line)
	}
	if got := restarts.Load(); got != 2 {
		t.Fatalf("a fresh burst after the rate limit must heal again, got %d", got)
	}
}

// Stale markers outside the two-minute window must not accumulate toward the
// threshold.
func TestDesyncWindowPrunes(t *testing.T) {
	m := &Manager{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var restarts atomic.Int64
	m.runCancel = context.CancelFunc(func() { restarts.Add(1) })

	m.mu.Lock()
	m.desyncAt = []time.Time{time.Now().Add(-3 * time.Minute), time.Now().Add(-5 * time.Minute)}
	m.mu.Unlock()
	m.noteDesyncSignature()
	m.noteDesyncSignature()
	if got := restarts.Load(); got != 0 {
		t.Fatalf("stale markers must not count toward the threshold, got %d restarts", got)
	}
}
