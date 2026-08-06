package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The hint is what a stuck user reads, so blaming the wrong thing costs them an
// afternoon. A speaker that ANSWERED is not behind a firewall, and saying so
// sent a reporter hunting through antivirus settings for two days while his
// speaker replied to every request (field 2026-08-06).
func TestReachabilityHintDoesNotBlameTheFirewallWhenTheSpeakerAnswered(t *testing.T) {
	answered := []string{
		`HTTP preflight rejected the listener at :8888 and SSH fallback also failed: ssh handshake failed: exit status 255 (ssh: connect to host 192.168.1.14 port 22: Connection timed out) (preflight: status 400 on 192.168.1.14 — body="")`,
		"version read failed: status 400",
		"unexpected status 503 from the speaker",
		"status 500 on 192.0.2.7",
	}
	for _, msg := range answered {
		got := reachabilityHint(errors.New(msg)).Error()
		// The word may appear in the negation ("this is not your firewall").
		// What must be gone is the ADVICE that sends the user into their
		// antivirus settings for a speaker that was answering all along.
		if strings.Contains(got, "Allow ST Reborn through your firewall") {
			t.Errorf("still sends the user into antivirus settings although the speaker answered:\n%s", got)
		}
		if strings.Contains(got, "different Wi-Fi networks") {
			t.Errorf("still blames the network although the speaker answered:\n%s", got)
		}
		if !strings.Contains(got, "answered") {
			t.Errorf("does not say the speaker answered:\n%s", got)
		}
		if !strings.Contains(strings.ToLower(got), "unplug") {
			t.Errorf("gives no usable next step:\n%s", got)
		}
	}
}

// A connection that never completed really can be a firewall, and that advice
// has to survive: it was added for a user whose security suite filtered the app
// while their browser worked fine.
func TestReachabilityHintStillBlamesTheFirewallWhenNothingAnswered(t *testing.T) {
	silent := []string{
		"dial tcp 192.0.2.7:8888: connect: connection refused",
		"context deadline exceeded (Client.Timeout exceeded while awaiting headers)",
		"dial tcp 192.0.2.7:8888: connect: no route to host",
	}
	for _, msg := range silent {
		got := reachabilityHint(errors.New(msg)).Error()
		if !strings.Contains(got, "Allow ST Reborn through your firewall") {
			t.Errorf("lost the firewall advice for a silent speaker:\n%s", got)
		}
	}
}

// A cancelled context is the app shutting down, never a diagnosis for the user.
func TestReachabilityHintLeavesCancellationAlone(t *testing.T) {
	if got := reachabilityHint(context.Canceled); got != context.Canceled {
		t.Errorf("cancellation was decorated: %v", got)
	}
	if got := reachabilityHint(nil); got != nil {
		t.Errorf("nil became %v", got)
	}
}

func TestAnsweredNotSTR(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"status 400 on 192.0.2.1", true},
		{"unexpected status", true},
		{"status 503", true},
		{"connection refused", false},
		{"i/o timeout", false},
		{"no route to host", false},
		// Both in one chain: the preflight answered, SSH then timed out. The
		// answer wins, because it proves the speaker was reachable.
		{`ssh handshake failed: Connection timed out (preflight: status 400 body="")`, true},
	}
	for _, tc := range cases {
		if got := answeredNotSTR(errors.New(tc.msg)); got != tc.want {
			t.Errorf("answeredNotSTR(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}
