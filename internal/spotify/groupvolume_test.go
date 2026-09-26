package spotify

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
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

// A volume event caused by STR's OWN seed/nudge must not be written back to
// the speaker either, not just kept out of the group fan-out.
//
// syncVolumeFromBox deliberately sends vol, then vol-1, then vol, because the
// Spotify app refreshes its slider only on a CHANGE. Each of those echoed back
// as an ordinary volume event and was mirrored straight onto the box, so every
// Spotify activation audibly dropped the speaker one step and put it back.
// Reported 2026-09-25 as "lauter/leiser springt hoch-runter", with all three
// writes visible in the reporter's own log.
//
// The guard existed for the group fan-out and simply never covered the speaker
// itself, which is the one that makes a sound. This pins the source, because
// the event loop needs a live go-librespot socket to exercise directly.
func TestOwnVolumeNudgeIsNotWrittenToTheBox(t *testing.T) {
	src, err := os.ReadFile("volume.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	at := strings.Index(s, "volume event is our own seed/nudge")
	if at < 0 {
		t.Fatal("the self-caused guard in front of the box write is gone")
	}
	// It must sit BEFORE the box write, and skip the whole event.
	head := s[at:]
	skip := strings.Index(head, "continue")
	write := strings.Index(head, "m.box.SetVolume")
	if skip < 0 || write < 0 || skip > write {
		t.Errorf("the guard must skip the event before the box write (skip=%d write=%d)", skip, write)
	}
	// And the nudge that caused it must still be there, or the slider goes
	// back to showing 100 until the user touches it.
	if !strings.Contains(s, "nudge := vol - 1") {
		t.Error("the slider nudge itself must stay; only writing it back is wrong")
	}
}
