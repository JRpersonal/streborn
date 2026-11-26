package main

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/webui"
)

// The roster tells its hook about a peer that comes up (a new seed, or a
// dimmed entry the app reached again) and stays quiet for one that was up
// already. The Deezer share rides on it (internal/webui/deezershare.go).
func TestPeerUpHookFiresOnlyWhenAPeerComesUp(t *testing.T) {
	r := newTestPeerRegistry(t)
	r.mu.Lock()
	r.byIP = map[string]*peerEntry{}
	r.mu.Unlock()
	var mu sync.Mutex
	var ups []string
	r.setOnPeerUp(func(ip string, port int) {
		mu.Lock()
		ups = append(ups, ip)
		mu.Unlock()
	})
	got := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), ups...)
	}

	r.seedPeers([]webui.PeerSeed{{Name: "Kitchen", Host: "192.0.2.9", Port: 17008}}, slog.Default())
	if g := got(); len(g) != 1 || g[0] != "192.0.2.9" {
		t.Fatalf("a new seed: %v", g)
	}
	r.seedPeers([]webui.PeerSeed{{Name: "Kitchen", Host: "192.0.2.9", Port: 17008}}, slog.Default())
	if g := got(); len(g) != 1 {
		t.Fatalf("a peer that was up already: %v", g)
	}
	r.mu.Lock()
	r.byIP["192.0.2.9"].reachable = false
	r.mu.Unlock()
	r.seedPeers([]webui.PeerSeed{{Name: "Kitchen", Host: "192.0.2.9", Port: 17008}}, slog.Default())
	if g := got(); len(g) != 2 {
		t.Fatalf("a dimmed peer reached again: %v", g)
	}

	if !r.isKnownPeer("192.0.2.9") || r.isKnownPeer("192.0.2.10") || r.isKnownPeer("") {
		t.Fatal("isKnownPeer")
	}
	r.mu.Lock()
	r.byIP["192.0.2.9"].lastSeen = time.Now().Add(-peerTTL - time.Minute)
	r.mu.Unlock()
	if r.isKnownPeer("192.0.2.9") {
		t.Fatal("an aged-out entry counts as known")
	}
}
