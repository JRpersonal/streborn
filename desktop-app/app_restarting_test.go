package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// errRefusedBothPorts is the second press from #1768: both of the speaker's
// ports refusing while it boots. Built through a format so net/http's own
// capital "Post" does not trip the lowercase-error-string lint.
var errRefusedBothPorts = fmt.Errorf("%s: dial tcp 192.0.2.30:17008: connectex: No connection could be made because the target machine actively refused it. (also tried :8888: connection refused)",
	`Post "http://192.0.2.30:17008/api/box/reboot"`)

func TestStillRestartingExplains(t *testing.T) {
	now := time.Date(2026, 11, 7, 14, 48, 23, 0, time.UTC)
	at := now.Add(-79 * time.Second)
	cases := []struct {
		name string
		err  error
		at   time.Time
		want bool
	}{
		{"#1768: refused 79 s after the restart", errRefusedBothPorts, at, true},
		{"timed out inside the window", errors.New("dial tcp 192.0.2.30:8888: i/o timeout"), at, true},
		{"firmware answering before the agent is up", errors.New("status 400: "), at, true},
		{"no restart asked for", errRefusedBothPorts, time.Time{}, false},
		{"restart longer ago than the window", errRefusedBothPorts, now.Add(-restartWindow), false},
		{"app shutting down", fmt.Errorf("x: %w", context.Canceled), at, false},
		{"this PC has no network", errors.New("dial tcp 192.0.2.30:8888: connect: network is unreachable"), at, false},
		{"no local address", errors.New("dial tcp 192.0.2.30:8888: connect: can't assign requested address"), at, false},
		{"no error", nil, at, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stillRestartingExplains(c.err, c.at, now); got != c.want {
				t.Errorf("stillRestartingExplains = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRestartedSince(t *testing.T) {
	now := time.Date(2026, 11, 7, 14, 50, 0, 0, time.UTC)
	at := now.Add(-3 * time.Minute)
	if !restartedSince(60, at, now) {
		t.Error("up for 60 s, restart asked 180 s ago: it has restarted")
	}
	if restartedSince(9000, at, now) {
		t.Error("up for 9000 s: the restart has not happened yet")
	}
}

func TestReachabilityHintAfterARestart(t *testing.T) {
	a := &App{}
	const host = "192.0.2.30"
	a.noteRestartRequested(host)

	msg := a.reachabilityHintFor(host, errRefusedBothPorts).Error()
	if !strings.Contains(msg, stillRestartingAdvice) || strings.Contains(msg, firewallAdvice) {
		t.Errorf("a refused restart target inside the window says it is restarting, got %q", msg)
	}
	if !strings.HasPrefix(msg, errRefusedBothPorts.Error()) {
		t.Errorf("the original error stays in front, got %q", msg)
	}

	// Another speaker is unaffected.
	if other := a.stillRestartingHint("192.0.2.31", errRefusedBothPorts); other != nil {
		t.Errorf("only the restarted speaker gets the restart text, got %v", other)
	}

	// Seen back with a low uptime: the window ends early.
	a.restartRequested.Store(host, time.Now().Add(-2*time.Minute))
	a.noteSpeakerUptime(host, "30")
	if a.stillRestartingHint(host, errRefusedBothPorts) != nil {
		t.Error("a speaker seen back after its restart gets the usual advice again")
	}

	// Still on its old uptime: the restart has not happened, the window holds.
	a.restartRequested.Store(host, time.Now().Add(-30*time.Second))
	a.noteSpeakerUptime(host, "9000")
	if a.stillRestartingHint(host, errRefusedBothPorts) == nil {
		t.Error("an uptime from before the request does not end the window")
	}

	// Window over.
	a.restartRequested.Store(host, time.Now().Add(-restartWindow-time.Second))
	if a.stillRestartingHint(host, errRefusedBothPorts) != nil {
		t.Error("past the window the restart no longer explains a failure")
	}
	if _, ok := a.restartRequested.Load(host); ok {
		t.Error("an expired record is dropped")
	}
}
