package spotify

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func newVolTestManager(ips []string, set func(ctx context.Context, ip string, pct int) error) *Manager {
	m := &Manager{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	m.groupSlaveIPsFn = func() []string { return ips }
	m.groupVolumeSetFn = set
	return m
}

// A burst of volume events must coalesce to (at most) the first in-flight
// value plus the LAST queued one - never one HTTP volley per event - and the
// requesting side must never block on a slow follower.
func TestGroupVolumeCoalescesBursts(t *testing.T) {
	var mu sync.Mutex
	var got []int
	block := make(chan struct{})
	m := newVolTestManager([]string{"192.0.2.20"}, func(_ context.Context, _ string, pct int) error {
		mu.Lock()
		got = append(got, pct)
		mu.Unlock()
		if pct == 1 {
			<-block // hold the worker so the burst queues up
		}
		return nil
	})

	start := time.Now()
	m.requestGroupVolume(1) // worker picks this up and blocks
	for v := 2; v <= 9; v++ {
		m.requestGroupVolume(v) // all superseded while the worker is held
	}
	m.requestGroupVolume(10) // the final value
	if e := time.Since(start); e > time.Second {
		t.Fatalf("requestGroupVolume must not block on a slow follower, took %v", e)
	}
	close(block)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(got) > 0 && got[len(got)-1] == 10
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	// Depending on when the worker first reads the channel, either only the
	// final value is applied or the first in-flight one plus the final one -
	// never one volley per event, and the final level always wins.
	if len(got) == 0 || got[len(got)-1] != 10 {
		t.Fatalf("the final volume must be applied last, got %v", got)
	}
	if len(got) > 2 {
		t.Fatalf("a 10-event burst must coalesce to at most 2 applies, got %v", got)
	}
}

func TestGroupVolumeEmptyProviderIsNoop(t *testing.T) {
	called := false
	m := newVolTestManager(nil, func(context.Context, string, int) error {
		called = true
		return nil
	})
	m.mirrorVolumeToGroup(context.Background(), 42)
	if called {
		t.Fatal("no followers -> no volume calls")
	}
}

// The manager's own SetVolume (slider seed + nudge) must flag the follow-up
// go-librespot volume event as self-caused so the event loop skips the group
// fan-out; the flag expires so genuine Connect changes fan out again.
func TestSelfVolSuppression(t *testing.T) {
	m := &Manager{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if m.selfVolActive() {
		t.Fatal("fresh manager must not report a self-caused volume change")
	}
	m.mu.Lock()
	m.selfVolUntil = time.Now().Add(50 * time.Millisecond)
	m.mu.Unlock()
	if !m.selfVolActive() {
		t.Fatal("selfVolUntil in the future must report active")
	}
	time.Sleep(80 * time.Millisecond)
	if m.selfVolActive() {
		t.Fatal("expired selfVolUntil must report inactive")
	}
}
