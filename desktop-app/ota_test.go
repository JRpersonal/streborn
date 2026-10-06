package main

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// fakeTimeout is a net.Error whose message carries no timeout keyword, so it
// exercises the errors.As/Timeout() branch of isTimeoutLikeErr rather than the
// string match.
type fakeTimeout struct{}

func (fakeTimeout) Error() string   { return "operation slow" }
func (fakeTimeout) Timeout() bool   { return true }
func (fakeTimeout) Temporary() bool { return true }

func TestIsTimeoutLikeErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		// The exact text two reporters saw on a slow/Norton-stalled OTA.
		{"client timeout reading body", errors.New(`Post "http://x/api/agent/update": context deadline exceeded (Client.Timeout or context cancellation while reading body)`), true},
		{"plain deadline", errors.New("context deadline exceeded"), true},
		{"conn reset", errors.New("read tcp 1.2.3.4:5 -> 6.7.8.9:10: connection reset by peer"), true},
		// A real HTTP rejection proves the binary was refused: must NOT be deferred.
		{"http 413 status", errors.New("status 413: Request Entity Too Large"), false},
		{"http 400 status", fmt.Errorf("status %d: bad request", 400), false},
		{"net.Error timeout without keyword", fakeTimeout{}, true},
		{"unrelated failure", errors.New("some other failure"), false},
	}
	for _, c := range cases {
		if got := isTimeoutLikeErr(c.err); got != c.want {
			t.Errorf("%s: isTimeoutLikeErr(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}

func TestOTASidecarEnsureBackoff(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 3 * time.Second}, // clamped to attempt 1
		{1, 3 * time.Second},
		{2, 6 * time.Second},
		{3, 12 * time.Second},
		{4, 24 * time.Second},
		{5, 30 * time.Second}, // capped
		{6, 30 * time.Second}, // capped
		{20, 30 * time.Second},
	}
	for _, c := range cases {
		if got := otaSidecarEnsureBackoff(c.attempt); got != c.want {
			t.Errorf("otaSidecarEnsureBackoff(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}

	// The cumulative wait across all retry gaps must stay inside otaRebootGrace
	// so a slow-to-reboot box is still covered without the user retrying by hand.
	var total time.Duration
	for attempt := 1; attempt < otaSidecarEnsureAttempts; attempt++ {
		total += otaSidecarEnsureBackoff(attempt)
	}
	if total >= otaRebootGrace {
		t.Errorf("cumulative sidecar retry backoff %v must stay below otaRebootGrace %v", total, otaRebootGrace)
	}
}

func TestAgentDowngradeRefused(t *testing.T) {
	cases := []struct {
		app, box string
		want     bool
	}{
		{"v1.0.1", "v1.0.3", true},               // older app, newer speaker (#1154)
		{"v0.9.40", "v1.0.1", true},              // across a minor boundary
		{"v1.0.3", "v1.0.1", false},              // normal upgrade
		{"v1.0.1", "v1.0.1", false},              // same version: repair re-push allowed
		{"v1.0.1-3-gabc-dirty", "v1.0.1", false}, // dev build off the same tag
		{"v1.0.1", "", false},                    // speaker version unknown
		{"", "v1.0.1", false},                    // app version unknown
		{"1.0.0", "v1.0.3", false},               // unstamped dev build never blocks
		{"v1.0.1", "dev", false},                 // unparsable speaker version
	}
	for _, c := range cases {
		if got := agentDowngradeRefused(c.app, c.box); got != c.want {
			t.Errorf("agentDowngradeRefused(%q, %q) = %v, want %v", c.app, c.box, got, c.want)
		}
	}
}
