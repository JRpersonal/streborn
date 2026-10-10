package main

import (
	"context"
	"testing"
	"time"
)

// portFetcher answers each agent port after its own delay; a negative delay
// means the port never answers (the probe fails after |delay|).
func portFetcher(delays map[int]time.Duration) func(context.Context, int) ([]byte, bool) {
	return func(_ context.Context, p int) ([]byte, bool) {
		d := delays[p]
		if d < 0 {
			time.Sleep(-d)
			return nil, false
		}
		time.Sleep(d)
		return []byte{byte(p % 256)}, true
	}
}

// A box that answers on both ports must get the same port every time, no
// matter which one replied first (#1190: an ST10 flipped between 8888 and
// 17008 once a minute and the app reset its now-playing view each time).
func TestPickAgentPortStableWhenBothAnswer(t *testing.T) {
	cases := []struct {
		name   string
		prefer int
		delays map[int]time.Duration
		want   int
	}{
		{"no preference, 8888 first", 0, map[int]time.Duration{8888: 0, 17008: 20 * time.Millisecond}, 8888},
		{"no preference, 17008 first", 0, map[int]time.Duration{8888: 30 * time.Millisecond, 17008: 0}, 8888},
		{"prefers cached 17008, 8888 first", 17008, map[int]time.Duration{8888: 0, 17008: 30 * time.Millisecond}, 17008},
		{"prefers cached 8888, 17008 first", 8888, map[int]time.Duration{8888: 30 * time.Millisecond, 17008: 0}, 8888},
		{"unknown preference behaves like none", 9999, map[int]time.Duration{8888: 30 * time.Millisecond, 17008: 0}, 8888},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, body := pickAgentPort(context.Background(), c.prefer, portFetcher(c.delays))
			if got != c.want {
				t.Fatalf("port = %d, want %d", got, c.want)
			}
			if len(body) != 1 || body[0] != byte(c.want%256) {
				t.Fatalf("body is not the answer of port %d", c.want)
			}
		})
	}
}

// When only one port answers it is used, whichever is preferred.
func TestPickAgentPortFallsBackToTheAnsweringPort(t *testing.T) {
	got, _ := pickAgentPort(context.Background(), 8888,
		portFetcher(map[int]time.Duration{8888: -10 * time.Millisecond, 17008: 0}))
	if got != 17008 {
		t.Fatalf("port = %d, want 17008", got)
	}
	got, _ = pickAgentPort(context.Background(), 17008,
		portFetcher(map[int]time.Duration{8888: 0, 17008: -10 * time.Millisecond}))
	if got != 8888 {
		t.Fatalf("port = %d, want 8888", got)
	}
	got, _ = pickAgentPort(context.Background(), 0,
		portFetcher(map[int]time.Duration{8888: -time.Millisecond, 17008: -time.Millisecond}))
	if got != 0 {
		t.Fatalf("port = %d, want 0 when neither answers", got)
	}
}

// A preferred port that hangs (a Portable whose :8888 SYNs are dropped) must
// not hold the answer from the other port for longer than the grace.
func TestPickAgentPortBoundsTheWaitForThePreferredPort(t *testing.T) {
	start := time.Now()
	got, _ := pickAgentPort(context.Background(), 0,
		portFetcher(map[int]time.Duration{8888: 5 * time.Second, 17008: 0}))
	if got != 17008 {
		t.Fatalf("port = %d, want 17008", got)
	}
	if el := time.Since(start); el > portPreferGrace+time.Second {
		t.Fatalf("waited %v for a hanging preferred port, grace is %v", el, portPreferGrace)
	}
}

func TestKnownAgentPort(t *testing.T) {
	if p := knownAgentPort(BoxInfo{Kind: "str", Port: 17008, PortVerified: true}); p != 17008 {
		t.Fatalf("verified str port = %d, want 17008", p)
	}
	if p := knownAgentPort(BoxInfo{Kind: "str", Port: 17008}); p != 0 {
		t.Fatalf("unverified port = %d, want 0", p)
	}
	if p := knownAgentPort(BoxInfo{Kind: "stock", Port: 8090, PortVerified: true}); p != 0 {
		t.Fatalf("stock port = %d, want 0", p)
	}
}
