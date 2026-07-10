package main

import (
	"log/slog"
	"testing"
	"time"
)

func testApp() *App {
	return &App{logger: slog.Default()}
}

// A speaker that reboots after a long quiet period (no discovery cycles while
// the user was just listening) must survive its whole reboot in the list: the
// grace window starts at the first MISSED probe, not at the last sighting.
func TestEvictionGraceStartsAtFirstMiss(t *testing.T) {
	a := testApp()
	old := time.Now().Add(-10 * time.Minute) // long past both sticky TTLs
	a.discCache = map[string]discEntry{
		"192.0.2.10": {box: BoxInfo{Host: "192.0.2.10", Kind: "str"}, seen: old},
	}

	// First cycle that misses the box: it must stay listed and start a miss streak.
	seen := map[string]BoxInfo{}
	a.mergeDiscoveryCacheWith(seen, nil)
	if _, ok := seen["192.0.2.10"]; !ok {
		t.Fatalf("box must survive the first missed cycle regardless of its last-seen age")
	}
	e, ok := a.discCache["192.0.2.10"]
	if !ok || e.firstMiss.IsZero() {
		t.Fatalf("first missed cycle must stamp firstMiss, entry=%+v ok=%v", e, ok)
	}

	// Still inside the STR grace window: keep it.
	e.firstMiss = time.Now().Add(-discoverySTRStickyTTL + 30*time.Second)
	a.discCache["192.0.2.10"] = e
	seen = map[string]BoxInfo{}
	a.mergeDiscoveryCacheWith(seen, nil)
	if _, ok := seen["192.0.2.10"]; !ok {
		t.Fatalf("box inside the miss grace window must stay listed")
	}

	// Missed for longer than the grace window: evict.
	e.firstMiss = time.Now().Add(-discoverySTRStickyTTL - time.Minute)
	a.discCache["192.0.2.10"] = e
	seen = map[string]BoxInfo{}
	a.mergeDiscoveryCacheWith(seen, nil)
	if _, ok := seen["192.0.2.10"]; ok {
		t.Fatalf("box missed past the grace window must be evicted")
	}
	if _, ok := a.discCache["192.0.2.10"]; ok {
		t.Fatalf("evicted box must leave the cache")
	}
}

func TestGenuineSightingClearsMissStreak(t *testing.T) {
	a := testApp()
	a.discCache = map[string]discEntry{
		"192.0.2.11": {
			box:       BoxInfo{Host: "192.0.2.11", Kind: "str"},
			seen:      time.Now().Add(-time.Hour),
			firstMiss: time.Now().Add(-time.Minute),
		},
	}
	seen := map[string]BoxInfo{"192.0.2.11": {Host: "192.0.2.11", Kind: "str"}}
	a.mergeDiscoveryCacheWith(seen, nil)
	if e := a.discCache["192.0.2.11"]; !e.firstMiss.IsZero() {
		t.Fatalf("a genuine sighting must clear the miss streak, firstMiss=%v", e.firstMiss)
	}
}

// A :8090-only (presence-only) sighting keeps the box listed but must NOT
// refresh the STR identity memory: a box whose agent is permanently gone has
// to be able to reclassify as stock once strKnownTTL passes.
func TestPresenceOnlyDoesNotRefreshSTRIdentity(t *testing.T) {
	a := testApp()
	memoSeen := time.Now().Add(-2 * time.Hour)
	a.strKnown = map[string]discEntry{
		"DEV1": {box: BoxInfo{Host: "192.0.2.12", DeviceID: "DEV1", Kind: "str"}, seen: memoSeen},
	}
	seen := map[string]BoxInfo{
		"192.0.2.12": {Host: "192.0.2.12", DeviceID: "DEV1", Kind: "str"},
	}
	a.mergeDiscoveryCacheWith(seen, map[string]bool{"192.0.2.12": true})
	if got := a.strKnown["DEV1"].seen; !got.Equal(memoSeen) {
		t.Fatalf("presence-only sighting must not refresh the STR identity memory: got %v want %v", got, memoSeen)
	}

	// A genuinely confirmed STR sighting DOES refresh it.
	a.mergeDiscoveryCacheWith(seen, nil)
	if got := a.strKnown["DEV1"].seen; got.Equal(memoSeen) {
		t.Fatalf("confirmed STR sighting must refresh the identity memory")
	}
}
