package main

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/webui"
)

// POST /api/deezer/kept-login takes a copied Deezer login only from a speaker
// in the roster: a seeded entry counts, an unknown or aged-out one does not.
func TestIsKnownPeer(t *testing.T) {
	r := newTestPeerRegistry(t)
	r.mu.Lock()
	r.byIP = map[string]*peerEntry{}
	r.mu.Unlock()
	r.seedPeers([]webui.PeerSeed{{Name: "Kitchen", Host: "192.0.2.9", Port: 17008}}, slog.Default())
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

// A Deezer login goes to another speaker only when the owner presses Copy in
// the Deezer area. Neither the agent start nor a peer coming up in the roster
// may send one: the agent wires no such call, and the roster has no peer-up
// hook to hang one on.
func TestNoAutomaticDeezerCopy(t *testing.T) {
	for _, f := range []string{"agent.go", "peers.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, banned := range []string{"OfferKeptDeezer", "RequestDeezerOffers", "setOnPeerUp", "firePeerUp", "onPeerUp", "/api/deezer/copy", "/api/deezer/kept-login\""} {
			if strings.Contains(src, banned) {
				t.Errorf("%s wires an automatic Deezer copy: %s", f, banned)
			}
		}
	}
}
