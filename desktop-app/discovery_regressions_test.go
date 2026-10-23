package main

import (
	"testing"
	"time"
)

// Scenario tests for the speaker-list regressions that have bitten before.
// Each one replays the exact sequence of sightings the issue described
// through the real upsert and cache-merge code, not the merge helpers in
// isolation, so a change anywhere on that path trips the right test.

// #90 (as cited on the sticky cache): an ST20 on marginal Wi-Fi, or mid-reboot,
// dropped out of the list for a cycle and radio + presets failed whenever it
// briefly vanished. A box that misses one whole cycle must stay listed with
// its full record (verified agent port, version, name), live and not greyed
// out, so the app keeps talking to it.
func TestFlakyCycleKeepsFullRecord(t *testing.T) {
	a := &App{discCache: map[string]discEntry{}}
	good := map[string]BoxInfo{
		"192.0.2.20": {Kind: "str", Host: "192.0.2.20", DeviceID: "DEV#SPOTTY", FriendlyName: "Esszimmer",
			Version: "v1.0.17", Build: "2026-10-01-1200", Port: 17008, PortVerified: true, Model: "SoundTouch 20"},
	}
	a.mergeDiscoveryCache(good)
	// Next cycle: neither mDNS nor any probe reached it.
	miss := map[string]BoxInfo{}
	a.mergeDiscoveryCache(miss)
	b, ok := miss["192.0.2.20"]
	if !ok {
		t.Fatalf("a box that missed one cycle must stay listed")
	}
	want := good["192.0.2.20"]
	if b.Offline || b.OfflineSinceSec != 0 {
		t.Errorf("first miss must not grey the tile out: offline=%v since=%d", b.Offline, b.OfflineSinceSec)
	}
	if b.Kind != "str" || !b.PortVerified || b.Port != 17008 || b.Version != want.Version || b.FriendlyName != want.FriendlyName {
		t.Errorf("record thinned out on a miss: %+v", b)
	}
}

// #108, the two halves in one cycle: right after an OTA the mDNS announcement
// carries the real name but the OLD version (the re-announce lags the agent
// restart), the live probe carries the NEW version and a verified port but no
// name yet. The list must show the real name AND the new version, with the
// verified port, in the order DiscoverBoxes produces them (mDNS first), and
// again after the cached pre-OTA record is merged in.
func TestPostOTACycleKeepsNameAndNewVersion(t *testing.T) {
	a := &App{discCache: map[string]discEntry{
		"192.0.2.30": {box: BoxInfo{Kind: "str", Host: "192.0.2.30", DeviceID: "DEV#ST10", FriendlyName: "Wohnzimmer",
			Version: "v0.7.0", Port: 17008, PortVerified: true}, seen: time.Now()},
	}}
	seen := map[string]BoxInfo{}
	upsertSighting(seen, BoxInfo{Kind: "str", Host: "192.0.2.30", DeviceID: "DEV#ST10", Name: "STR-AAAAAA",
		FriendlyName: "Wohnzimmer", Version: "v0.7.0", Port: 8888})
	upsertSighting(seen, BoxInfo{Kind: "str", Host: "192.0.2.30", DeviceID: "DEV#ST10", Name: "str-192.0.2.30",
		Version: "v0.7.1", Port: 17008, PortVerified: true})
	a.mergeDiscoveryCache(seen)
	b := seen["192.0.2.30"]
	if b.FriendlyName != "Wohnzimmer" {
		t.Errorf("FriendlyName = %q, want Wohnzimmer (the str-<ip> half of #108)", b.FriendlyName)
	}
	if b.Version != "v0.7.1" {
		t.Errorf("Version = %q, want v0.7.1 (the update-not-flagged half of #108)", b.Version)
	}
	if !b.PortVerified || b.Port != 17008 {
		t.Errorf("port = %d verified=%v, want the verified 17008", b.Port, b.PortVerified)
	}
	if b.Kind != "str" {
		t.Errorf("Kind = %q, want str", b.Kind)
	}
}

// #108, the later half: after the OTA the box reboots and its stock :8090
// answers before the agent does. That cycle must not relabel the box "Ready
// for STR" (a reinstall offer for a speaker that runs STR), and the moment the
// agent answers again the record is whole.
func TestPostOTARebootDoesNotOfferReinstall(t *testing.T) {
	a := testApp()
	a.discCache = map[string]discEntry{
		"192.0.2.31": {box: BoxInfo{Kind: "str", Host: "192.0.2.31", DeviceID: "DEV#ST20", FriendlyName: "Küche",
			Version: "v0.7.14", Port: 8888, PortVerified: true}, seen: time.Now()},
	}
	a.notePostOTA("192.0.2.31")
	// Mid-reboot: only the stock /info answered.
	seen := map[string]BoxInfo{}
	upsertSighting(seen, BoxInfo{Kind: "stock", Host: "192.0.2.31", DeviceID: "DEV#ST20", FriendlyName: "Küche", Port: 8090})
	a.mergeDiscoveryCacheWith(seen, map[string]bool{"192.0.2.31": true})
	b := seen["192.0.2.31"]
	if b.Kind != "str" || b.STRNotRunning {
		t.Errorf("mid-reboot stock sighting relabelled the box: kind=%q strNotRunning=%v", b.Kind, b.STRNotRunning)
	}
	if !b.OTAPending {
		t.Errorf("the served record must say the update is pending so no update badge lights")
	}
	// Agent back: the fresh record replaces the pin's view.
	seen = map[string]BoxInfo{}
	upsertSighting(seen, BoxInfo{Kind: "str", Host: "192.0.2.31", DeviceID: "DEV#ST20", Version: "v0.7.21", Port: 8888, PortVerified: true})
	a.mergeDiscoveryCacheWith(seen, nil)
	b = seen["192.0.2.31"]
	if b.Version != "v0.7.21" || b.FriendlyName != "Küche" || b.Kind != "str" {
		t.Errorf("after the agent came back: %+v", b)
	}
}

// #709 (discussion): three SoundTouch 10s installed in a row came back on fresh
// DHCP leases, thin (agent up, name not served yet), and the tiles renamed to
// the technical placeholder. Each box must carry ITS OWN name to its new
// address, and no box may inherit a neighbour's.
func TestTripleInstallCarriesEachNameToNewLease(t *testing.T) {
	a := &App{discCache: map[string]discEntry{}}
	for i, name := range []string{"Grace left", "Grace right", "Office"} {
		host := "192.0.2.4" + string(rune('0'+i))
		a.discCache[host] = discEntry{box: BoxInfo{Kind: "stock", Host: host, DeviceID: "DEV#" + name,
			FriendlyName: name, Model: "SoundTouch 10"}, seen: time.Now()}
	}
	seen := map[string]BoxInfo{}
	for i, name := range []string{"Grace left", "Grace right", "Office"} {
		host := "192.0.2.5" + string(rune('0'+i))
		upsertSighting(seen, BoxInfo{Kind: "str", Host: host, DeviceID: "DEV#" + name, Name: "str-" + host,
			Version: "v0.9.57", Port: 8888, PortVerified: true})
	}
	a.mergeDiscoveryCache(seen)
	if len(seen) != 3 {
		t.Fatalf("got %d boxes, want 3 (each at its new address only): %v", len(seen), seen)
	}
	for i, name := range []string{"Grace left", "Grace right", "Office"} {
		host := "192.0.2.5" + string(rune('0'+i))
		b, ok := seen[host]
		if !ok {
			t.Errorf("%s missing at its new address %s", name, host)
			continue
		}
		if b.FriendlyName != name {
			t.Errorf("%s: FriendlyName = %q, want its own name carried over", host, b.FriendlyName)
		}
		if b.Model != "SoundTouch 10" || b.Kind != "str" || b.Version != "v0.9.57" {
			t.Errorf("%s: carried identity must not touch the live record: %+v", host, b)
		}
	}
	for i := range 3 {
		old := "192.0.2.4" + string(rune('0'+i))
		if _, stale := a.discCache[old]; stale {
			t.Errorf("dead lease %s still cached", old)
		}
	}
}
