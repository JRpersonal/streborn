package main

import (
	"testing"
	"time"
)

// The update badge keys on the hash of the agent the speaker runs
// (agentRunningSha256). In one discovery cycle the mDNS announcement, which
// carries no hash, is upserted first and the live probe, which carries the
// hash of the binary now running, second; then the cached pre-update record is
// merged in. The hash the list ends up with must be the probe's, never the
// cached pre-update one and never empty. Replays that exact order.
//
// Before the fix the same-kind merge never copied the hashes (nor the
// key-refusal readings) from the probe, so the upsert came out with no hash
// and the cache merge then refilled it from the PRE-update record: the list
// said the speaker still ran the old agent right after a successful update,
// and cached that answer for the next cycle.
func TestDiscoveryCycleKeepsProbedAgentHash(t *testing.T) {
	const host = "192.0.2.60"
	a := &App{discCache: map[string]discEntry{
		host: {box: BoxInfo{Kind: "str", Host: host, DeviceID: "DEV#SHA", FriendlyName: "Bad", Version: "v1.0.17",
			Port: 8888, PortVerified: true, AgentBinarySha256: "old", AgentRunningSha256: "old"}, seen: time.Now()},
	}}
	seen := map[string]BoxInfo{}
	// 1. mDNS: name, stale version, no hash (discovery.Instance has none).
	upsertSighting(seen, BoxInfo{Kind: "str", Host: host, DeviceID: "DEV#SHA", Name: "STR-AAAAAA", FriendlyName: "Bad",
		Version: "v1.0.17", Port: 8888})
	// 2. Live probe: the new build, with both hashes.
	upsertSighting(seen, BoxInfo{Kind: "str", Host: host, DeviceID: "DEV#SHA", Name: "str-" + host,
		Version: "v1.0.18", Port: 8888, PortVerified: true, AgentBinarySha256: "new", AgentRunningSha256: "new",
		GroupKeyError: "no partner", HoldRefusedSource: "SPOTIFY", HoldRefusedSlot: "3"})
	merged := seen[host]
	if merged.AgentRunningSha256 != "new" || merged.AgentBinarySha256 != "new" {
		t.Errorf("after the upsert: running=%q binary=%q, want new/new (the probe's hashes)", merged.AgentRunningSha256, merged.AgentBinarySha256)
	}
	if merged.GroupKeyError != "no partner" || merged.HoldRefusedSource != "SPOTIFY" || merged.HoldRefusedSlot != "3" {
		t.Errorf("after the upsert: the probe's key-refusal readings were lost: %+v", merged)
	}
	// 3. Cache merge with the pre-update record.
	a.mergeDiscoveryCache(seen)
	b := seen[host]
	if b.AgentRunningSha256 != "new" {
		t.Errorf("AgentRunningSha256 = %q, want new (the cached pre-update hash must not win)", b.AgentRunningSha256)
	}
	if b.AgentBinarySha256 != "new" {
		t.Errorf("AgentBinarySha256 = %q, want new", b.AgentBinarySha256)
	}
	if b.Version != "v1.0.18" || b.FriendlyName != "Bad" {
		t.Errorf("version/name: %+v", b)
	}
	if cached := a.discCache[host].box; cached.AgentRunningSha256 != "new" {
		t.Errorf("cached AgentRunningSha256 = %q, want new (the next cycle would serve it again)", cached.AgentRunningSha256)
	}
}

// The same with the probe first (the known-speaker probe or the sweep can land
// before the announcement): the hash must come out the same either way.
func TestDiscoveryCycleKeepsProbedAgentHashProbeFirst(t *testing.T) {
	const host = "192.0.2.61"
	seen := map[string]BoxInfo{}
	upsertSighting(seen, BoxInfo{Kind: "str", Host: host, Version: "v1.0.18", Port: 8888, PortVerified: true,
		AgentBinarySha256: "new", AgentRunningSha256: "new", GroupKeyError: "no partner"})
	upsertSighting(seen, BoxInfo{Kind: "str", Host: host, Name: "STR-AAAAAA", FriendlyName: "Bad", Version: "v1.0.17", Port: 8888})
	b := seen[host]
	if b.AgentRunningSha256 != "new" || b.AgentBinarySha256 != "new" || b.GroupKeyError != "no partner" {
		t.Errorf("probe first: %+v", b)
	}
	if b.FriendlyName != "Bad" || b.Version != "v1.0.18" {
		t.Errorf("probe first lost the name or the version: %+v", b)
	}
}
