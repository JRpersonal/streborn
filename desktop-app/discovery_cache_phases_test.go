package main

import (
	"testing"
	"time"
)

// phaseNow is the fixed instant the phase tests place their merge at. The
// phases take the cycle's timestamp as an argument, so every boundary below is
// exact instead of racing the wall clock.
var phaseNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// Phase 2 of the cache merge, for one cached box the cycle missed: how long
// ago its miss streak began decides whether it is served, greyed out, or gone.
func TestReaddOrEvictLockedByMissAge(t *testing.T) {
	const host = "192.0.2.10"
	cases := []struct {
		name        string
		kind        string
		missAge     time.Duration // 0 = no miss streak yet
		wantServed  bool
		wantOffline bool
		wantCached  bool
		wantSince   int
	}{
		{name: "first miss starts the streak", kind: "stock", wantServed: true, wantCached: true},
		{name: "stock at the sticky TTL", kind: "stock", missAge: discoveryStickyTTL, wantServed: true, wantCached: true},
		{name: "stock just past the sticky TTL goes offline", kind: "stock", missAge: discoveryStickyTTL + time.Second,
			wantServed: true, wantOffline: true, wantCached: true, wantSince: int((discoveryStickyTTL + time.Second).Seconds())},
		{name: "str past the stock TTL is still in its longer grace", kind: "str", missAge: discoveryStickyTTL + time.Second,
			wantServed: true, wantCached: true},
		{name: "str at its sticky TTL", kind: "str", missAge: discoverySTRStickyTTL, wantServed: true, wantCached: true},
		{name: "str just past its sticky TTL goes offline", kind: "str", missAge: discoverySTRStickyTTL + time.Second,
			wantServed: true, wantOffline: true, wantCached: true, wantSince: int((discoverySTRStickyTTL + time.Second).Seconds())},
		{name: "offline at the retention limit", kind: "str", missAge: discoveryOfflineRetention,
			wantServed: true, wantOffline: true, wantCached: true, wantSince: int(discoveryOfflineRetention.Seconds())},
		{name: "evicted past the retention", kind: "str", missAge: discoveryOfflineRetention + time.Second},
		{name: "stock evicted past the retention", kind: "stock", missAge: discoveryOfflineRetention + time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp()
			e := discEntry{box: BoxInfo{Host: host, Kind: tc.kind, DeviceID: "device-id-here"}, seen: phaseNow.Add(-48 * time.Hour)}
			if tc.missAge != 0 {
				e.firstMiss = phaseNow.Add(-tc.missAge)
			}
			a.discCache = map[string]discEntry{host: e}
			seen := map[string]BoxInfo{}

			a.discMu.Lock()
			moved := a.readdOrEvictLocked(phaseNow, seen, freshDeviceHosts(seen))
			a.discMu.Unlock()

			if len(moved) != 0 {
				t.Fatalf("nothing moved, got movedAway=%v", moved)
			}
			b, served := seen[host]
			if served != tc.wantServed {
				t.Fatalf("served=%v, want %v", served, tc.wantServed)
			}
			cached, inCache := a.discCache[host]
			if inCache != tc.wantCached {
				t.Fatalf("cached=%v, want %v", inCache, tc.wantCached)
			}
			if !served {
				return
			}
			if !b.Unreachable {
				t.Errorf("a box served from the cache must say it was not reached")
			}
			if b.Offline != tc.wantOffline || b.OfflineSinceSec != tc.wantSince {
				t.Errorf("Offline=%v since=%d, want %v / %d", b.Offline, b.OfflineSinceSec, tc.wantOffline, tc.wantSince)
			}
			if cached.box.Unreachable || cached.box.Offline {
				t.Errorf("the cached record must stay clean, got %+v", cached.box)
			}
			if tc.missAge == 0 && !cached.firstMiss.Equal(phaseNow) {
				t.Errorf("first miss must be stamped with the cycle's now, got %v", cached.firstMiss)
			}
			if !cached.seen.Equal(e.seen) {
				t.Errorf("a miss must not refresh the last-seen stamp")
			}
		})
	}
}

// Phase 2 for a speaker that came back at a new address: the old address is
// evicted at once, whatever its miss age, and reported as moved away.
func TestReaddOrEvictLockedEvictsMovedSpeaker(t *testing.T) {
	const oldHost, newHost = "192.0.2.10", "192.0.2.20"
	cases := []struct {
		name      string
		cachedID  string
		liveID    string
		wantMoved bool
	}{
		{name: "same device at a new address", cachedID: "device-id-here", liveID: "device-id-here", wantMoved: true},
		{name: "another device at the new address", cachedID: "device-id-here", liveID: "other-device-id"},
		{name: "cached record without an id", cachedID: "", liveID: "device-id-here"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp()
			a.discCache = map[string]discEntry{
				oldHost: {box: BoxInfo{Host: oldHost, Kind: "str", DeviceID: tc.cachedID, FriendlyName: "Kitchen"}, seen: phaseNow.Add(-time.Minute)},
				newHost: {box: BoxInfo{Host: newHost, Kind: "str", DeviceID: tc.liveID}, seen: phaseNow},
			}
			seen := map[string]BoxInfo{newHost: {Host: newHost, Kind: "str", DeviceID: tc.liveID}}

			a.discMu.Lock()
			moved := a.readdOrEvictLocked(phaseNow, seen, freshDeviceHosts(seen))
			a.discMu.Unlock()

			_, oldCached := a.discCache[oldHost]
			_, oldServed := seen[oldHost]
			if moved[oldHost] != tc.wantMoved || oldCached == tc.wantMoved || oldServed == tc.wantMoved {
				t.Fatalf("moved=%v cached=%v served=%v, want moved=%v", moved[oldHost], oldCached, oldServed, tc.wantMoved)
			}
			wantName := ""
			if tc.wantMoved {
				wantName = "Kitchen"
			}
			if got := seen[newHost].FriendlyName; got != wantName {
				t.Errorf("live record name=%q, want %q", got, wantName)
			}
			if got := seen[newHost].Host; got != newHost {
				t.Errorf("live record must keep its own address, got %q", got)
			}
		})
	}
}

// Phase 7: an STR identity memory is dropped once it is older than strKnownTTL.
func TestExpireSTRIdentityLocked(t *testing.T) {
	cases := []struct {
		name string
		age  time.Duration
		want bool
	}{
		{name: "fresh", age: 0, want: true},
		{name: "one second inside the TTL", age: strKnownTTL - time.Second, want: true},
		{name: "exactly at the TTL", age: strKnownTTL, want: true},
		{name: "one second past the TTL", age: strKnownTTL + time.Second, want: false},
		{name: "long gone", age: 30 * 24 * time.Hour, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp()
			a.strKnown = map[string]discEntry{
				"device-id-here": {box: BoxInfo{Kind: "str", DeviceID: "device-id-here"}, seen: phaseNow.Add(-tc.age)},
			}
			a.discMu.Lock()
			a.expireSTRIdentityLocked(phaseNow)
			a.discMu.Unlock()
			if _, ok := a.strKnown["device-id-here"]; ok != tc.want {
				t.Fatalf("memory kept=%v, want %v", ok, tc.want)
			}
		})
	}
}

// Phases 3 and 4 drop a pin once its grace has run out, measured against the
// injected now.
func TestPinsExpireAgainstInjectedNow(t *testing.T) {
	const host = "192.0.2.10"
	cases := []struct {
		name      string
		otaAge    time.Duration
		stockAge  time.Duration
		wantOTA   bool
		wantStock bool
	}{
		{name: "both at their grace", otaAge: otaRebootGrace, stockAge: stockPinGrace, wantOTA: true, wantStock: true},
		{name: "both just past their grace", otaAge: otaRebootGrace + time.Second, stockAge: stockPinGrace + time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp()
			a.discCache = map[string]discEntry{}
			a.otaPinned = map[string]time.Time{host: phaseNow.Add(-tc.otaAge)}
			a.otaPinnedID = map[string]string{host: "device-id-here"}
			a.stockPinned = map[string]time.Time{"192.0.2.11": phaseNow.Add(-tc.stockAge)}
			seen := map[string]BoxInfo{}

			a.discMu.Lock()
			a.applyOTAPinsLocked(phaseNow, seen, freshDeviceHosts(seen), nil)
			a.applyStockPinsLocked(phaseNow, seen)
			a.discMu.Unlock()

			if _, ok := a.otaPinned[host]; ok != tc.wantOTA {
				t.Errorf("OTA pin kept=%v, want %v", ok, tc.wantOTA)
			}
			if b, ok := seen[host]; ok != tc.wantOTA || (ok && (!b.OTAPending || b.Kind != "str")) {
				t.Errorf("pinned box served=%v %+v, want served=%v as a pending STR box", ok, b, tc.wantOTA)
			}
			if _, ok := a.stockPinned["192.0.2.11"]; ok != tc.wantStock {
				t.Errorf("stock pin kept=%v, want %v", ok, tc.wantStock)
			}
		})
	}
}
