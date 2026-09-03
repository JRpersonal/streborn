package streamproxy

import (
	"context"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// TestAwaitQueueLiveURLCtxCancel: when the box drops the connection mid-hold
// (context cancelled), awaitQueueLiveURL must return "" promptly instead of
// polling out the remaining recall budget.
func TestAwaitQueueLiveURLCtxCancel(t *testing.T) {
	s := New(presets.New(), silentLogger())
	// A recall permanently in flight that never lands a track: without the
	// cancel this would hold for the full queueRecallHold.
	s.SetQueueLiveURLFn(func(slot int) (string, bool) { return "", true })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		url     string
		elapsed time.Duration
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		url := s.awaitQueueLiveURL(ctx, 6)
		done <- result{url: url, elapsed: time.Since(start)}
	}()

	time.AfterFunc(150*time.Millisecond, cancel)

	select {
	case res := <-done:
		if res.url != "" {
			t.Fatalf("cancelled hold must resolve to no URL, got %q", res.url)
		}
		if res.elapsed >= time.Second {
			t.Fatalf("ctx cancel took %s to unblock; it must return promptly", res.elapsed)
		}
	case <-time.After(queueRecallHold + time.Second):
		t.Fatal("awaitQueueLiveURL never returned after ctx cancel")
	}
}

// TestAwaitQueueLiveURLRecallHoldBound: a recall that stays in flight forever
// but never produces a track must give up after ~queueRecallHold - the hold is
// a bounded budget, never an indefinite hang of the box's fetch.
func TestAwaitQueueLiveURLRecallHoldBound(t *testing.T) {
	s := New(presets.New(), silentLogger())
	s.SetQueueLiveURLFn(func(slot int) (string, bool) { return "", true })

	type result struct {
		url     string
		elapsed time.Duration
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		url := s.awaitQueueLiveURL(context.Background(), 6)
		done <- result{url: url, elapsed: time.Since(start)}
	}()

	select {
	case res := <-done:
		if res.url != "" {
			t.Fatalf("a never-landing recall must resolve to no URL, got %q", res.url)
		}
		if res.elapsed > queueRecallHold+time.Second {
			t.Fatalf("hold ran %s; it must end near queueRecallHold (%s)", res.elapsed, queueRecallHold)
		}
		if res.elapsed < queueRecallGrace {
			t.Fatalf("hold ended after only %s; a recall in flight must be held past queueRecallGrace", res.elapsed)
		}
	case <-time.After(queueRecallHold + 2*time.Second):
		t.Fatal("awaitQueueLiveURL never returned; the recall hold must be bounded")
	}
}
