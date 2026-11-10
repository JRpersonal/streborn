// Tests for the sticky speaker-picker seed path (2026-07-26): the desktop app
// distributes its known-speaker set to every agent, and the agent must merge
// it into the peer map as a fresh sighting so the on-box picker lists the
// whole fleet even where local mDNS is lossy.

package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/webui"
)

// The roster a speaker saved must be the roster it restores after a reboot,
// and the durable write must leave no temp sibling behind on the NAND.
func TestPeerRosterSurvivesARestart(t *testing.T) {
	t.Parallel()
	r := newTestPeerRegistry(t)
	seen := time.Now().Add(-time.Minute).Truncate(time.Second)
	r.mu.Lock()
	r.byIP["192.0.2.9"] = &peerEntry{name: "Küche", deviceID: "AABBCCDDEEFF", boxDeviceID: "112233445566", port: 17008, lastSeen: seen, reachable: true}
	r.savePersistedPeersLocked(slog.Default())
	r.mu.Unlock()

	if _, err := os.Stat(r.storePath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("the temp file must be gone after a successful write, stat err = %v", err)
	}

	restored := newPeerRegistry(r.storePath)
	restored.loadPersistedPeers(slog.Default())
	restored.mu.Lock()
	defer restored.mu.Unlock()
	e := restored.byIP["192.0.2.9"]
	if e == nil {
		t.Fatalf("the saved peer was not restored, got %v", restored.byIP)
	}
	if e.name != "Küche" || e.deviceID != "AABBCCDDEEFF" || e.boxDeviceID != "112233445566" || e.port != 17008 || !e.lastSeen.Equal(seen) {
		t.Fatalf("restored entry differs from the saved one: %+v", e)
	}
	if e.reachable {
		t.Fatal("a restored peer must come back dimmed until a probe confirms it")
	}
}

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
