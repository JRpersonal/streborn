package main

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The stale-rule selector must delete exactly the streborn-shaped REDIRECT
// rules whose -d address the box no longer holds: nothing for current
// addresses, nothing for foreign ports, nothing that is not a REDIRECT, and
// the comment-less taigan shape must match just like the commented one (#697).
func TestStaleRedirectDeleteArgs(t *testing.T) {
	rules := strings.Join([]string{
		"-P PREROUTING ACCEPT",
		// stale, commented (xt_comment kernels)
		`-A PREROUTING -d 192.168.178.174/32 ! -i lo -p tcp -m tcp --dport 8888 -m comment --comment streborn-redirect -j REDIRECT --to-ports 8888`,
		// stale, comment-less (taigan)
		`-A PREROUTING -d 192.168.178.174/32 ! -i lo -p tcp -m tcp --dport 17008 -j REDIRECT --to-ports 8888`,
		// current address: keep
		`-A PREROUTING -d 10.10.50.101/32 ! -i lo -p tcp -m tcp --dport 8888 -m comment --comment streborn-redirect -j REDIRECT --to-ports 8888`,
		// stale address but a port that is not ours: keep
		`-A PREROUTING -d 192.168.178.174/32 ! -i lo -p tcp -m tcp --dport 5000 -j REDIRECT --to-ports 5000`,
		// stale address but not a REDIRECT: keep
		`-A PREROUTING -d 192.168.178.174/32 -p tcp -m tcp --dport 8888 -j DNAT --to-destination 127.0.0.1:9080`,
		// no -d at all: keep
		`-A PREROUTING -p tcp -m tcp --dport 17008 -j REDIRECT --to-ports 8888`,
	}, "\n")
	own := map[string]bool{"10.10.50.101": true, "127.0.0.1": true}

	got := staleRedirectDeleteArgs(rules, own)
	if len(got) != 2 {
		t.Fatalf("want exactly the two stale streborn rules deleted, got %d: %v", len(got), got)
	}
	for _, args := range got {
		joined := strings.Join(args, " ")
		if !strings.HasPrefix(joined, "-w -t nat -D PREROUTING ") {
			t.Errorf("delete must replay the rule with -D: %q", joined)
		}
		if !strings.Contains(joined, "-d 192.168.178.174/32") {
			t.Errorf("delete must target only the stale address: %q", joined)
		}
	}
	// The comment-less rule must be replayed WITHOUT comment args, or -D
	// cannot match it on a taigan kernel.
	if strings.Contains(strings.Join(got[1], " "), "comment") {
		t.Errorf("comment-less rule replayed with comment args: %v", got[1])
	}
}

// purgeSelfPeers must drop entries that carry this speaker's own deviceID even
// under a placeholder name (the #697 shape: the box's stale self-announcement
// re-adopted at the old address as "str-<old-ip>"), plus name-matching ones,
// and leave real peers alone.
func TestPurgeSelfPeers(t *testing.T) {
	prevDev, prevName := peerSelfDeviceIDFn, peerSelfNameFn
	defer func() { peerSelfDeviceIDFn, peerSelfNameFn = prevDev, prevName }()
	peerSelfDeviceIDFn = func() string { return "AABBCCDDEEFF" }
	peerSelfNameFn = func() string { return "Küche" }

	peersMu.Lock()
	peersByIP = map[string]*peerEntry{
		"192.168.178.174": {name: "str-192.168.178.174", deviceID: "AABBCCDDEEFF", lastSeen: time.Now()}, // self, placeholder name
		"192.168.178.175": {name: "Küche", lastSeen: time.Now()},                                         // self by name, no deviceID
		"10.10.50.100":    {name: "Wohnzimmer", deviceID: "112233445566", lastSeen: time.Now()},          // a real peer
	}
	peersBrowseAt = time.Now()
	peersMu.Unlock()

	purgeSelfPeers(slog.Default())

	peersMu.Lock()
	defer peersMu.Unlock()
	if len(peersByIP) != 1 {
		t.Fatalf("want only the real peer left, got %d entries: %v", len(peersByIP), peersByIP)
	}
	if _, ok := peersByIP["10.10.50.100"]; !ok {
		t.Fatalf("the real peer was purged")
	}
	if !peersBrowseAt.IsZero() {
		t.Errorf("purge must force a fresh sweep on the next browse")
	}
}
