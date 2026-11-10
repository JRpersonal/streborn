// Tests for the sticky speaker-picker seed path (2026-07-26): the desktop app
// distributes its known-speaker set to every agent, and the agent must merge
// it into the peer map as a fresh sighting so the on-box picker lists the
// whole fleet even where local mDNS is lossy.

package main

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/webui"
)

func TestSeedPeersMergesAsSighting(t *testing.T) {
	r := newTestPeerRegistry(t)
	r.mu.Lock()
	r.byIP = map[string]*peerEntry{}
	r.mu.Unlock()

	r.seedPeers([]webui.PeerSeed{
		{Name: "Küche", Host: "192.0.2.9", Port: 17008},
		{Name: "", Host: "", Port: 8888}, // empty host must be ignored
	}, slog.Default())

	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.byIP) != 1 {
		t.Fatalf("expected exactly the one valid seed, got %d entries", len(r.byIP))
	}
	e := r.byIP["192.0.2.9"]
	if e == nil || e.name != "Küche" || e.port != 17008 {
		t.Fatalf("seed not merged verbatim: %+v", e)
	}
	if time.Since(e.lastSeen) > time.Minute {
		t.Fatal("a seed must count as a fresh sighting (sticky list), lastSeen is stale")
	}
}

func TestSeedPeersDoesNotDowngradeKnownName(t *testing.T) {
	r := newTestPeerRegistry(t)
	r.mu.Lock()
	r.byIP = map[string]*peerEntry{"192.0.2.9": {name: "Küche", port: 17008, lastSeen: time.Now()}}
	r.mu.Unlock()

	r.seedPeers([]webui.PeerSeed{{Name: "", Host: "192.0.2.9", Port: 0}}, slog.Default())

	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.byIP["192.0.2.9"]
	if e == nil || e.name != "Küche" || e.port != 17008 {
		t.Fatalf("an empty seed field overwrote known data: %+v", e)
	}
}

// newTestPeerRegistry returns an empty roster of the test's own, persisted in
// the test's temp directory, so no test shares roster state with another or
// reaches for the speaker's NAND path.
func newTestPeerRegistry(t *testing.T) *peerRegistry {
	t.Helper()
	return newPeerRegistry(filepath.Join(t.TempDir(), "peers.json"))
}
