// Tests for the sticky speaker-picker seed path (2026-07-26): the desktop app
// distributes its known-speaker set to every agent, and the agent must merge
// it into the peer map as a fresh sighting so the on-box picker lists the
// whole fleet even where local mDNS is lossy.

package main

import (
	"log/slog"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/webui"
)

func TestSeedPeersMergesAsSighting(t *testing.T) {
	peersMu.Lock()
	peersByIP = map[string]*peerEntry{}
	peersMu.Unlock()

	seedPeers([]webui.PeerSeed{
		{Name: "Küche", Host: "192.0.2.9", Port: 17008},
		{Name: "", Host: "", Port: 8888}, // empty host must be ignored
	}, slog.Default())

	peersMu.Lock()
	defer peersMu.Unlock()
	if len(peersByIP) != 1 {
		t.Fatalf("expected exactly the one valid seed, got %d entries", len(peersByIP))
	}
	e := peersByIP["192.0.2.9"]
	if e == nil || e.name != "Küche" || e.port != 17008 {
		t.Fatalf("seed not merged verbatim: %+v", e)
	}
	if time.Since(e.lastSeen) > time.Minute {
		t.Fatal("a seed must count as a fresh sighting (sticky list), lastSeen is stale")
	}
}

func TestSeedPeersDoesNotDowngradeKnownName(t *testing.T) {
	peersMu.Lock()
	peersByIP = map[string]*peerEntry{"192.0.2.9": {name: "Küche", port: 17008, lastSeen: time.Now()}}
	peersMu.Unlock()

	seedPeers([]webui.PeerSeed{{Name: "", Host: "192.0.2.9", Port: 0}}, slog.Default())

	peersMu.Lock()
	defer peersMu.Unlock()
	e := peersByIP["192.0.2.9"]
	if e == nil || e.name != "Küche" || e.port != 17008 {
		t.Fatalf("an empty seed field overwrote known data: %+v", e)
	}
}
