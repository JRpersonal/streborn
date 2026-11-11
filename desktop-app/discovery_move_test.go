package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// A speaker that comes back from its update on a new lease, inside the
// post-OTA pin window, must leave nothing behind at its old address: not while
// the pin runs, and not after it expires. The pin used to re-create a record
// at the old address in the very cycle that evicted it, without a deviceID,
// and that record then sat in the list for up to a day (field case
// 2026-10-26).

func moveTestApp() *App {
	return &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), discCache: map[string]discEntry{}}
}

func TestMovedSpeakerLeavesNoEntryAtItsOldAddress(t *testing.T) {
	const oldIP, newIP = "192.0.2.40", "192.0.2.41"
	a := moveTestApp()
	a.discCache[oldIP] = discEntry{box: BoxInfo{Host: oldIP, Port: 8888, Kind: "str", DeviceID: idA, FriendlyName: "Kitchen"}, seen: time.Now()}
	a.notePostOTA(oldIP)
	if a.otaPinnedID[oldIP] != idA {
		t.Fatalf("pin id = %q, want the speaker's deviceID", a.otaPinnedID[oldIP])
	}

	// Mid-reboot: nothing answers, the pin keeps the tile, as STR.
	seen := map[string]BoxInfo{}
	a.mergeDiscoveryCache(seen)
	if b, ok := seen[oldIP]; !ok || b.Kind != "str" || !b.OTAPending {
		t.Fatalf("mid-reboot tile = %+v (present %v), want the pinned STR record", b, ok)
	}

	// Back, at a new address, inside the pin window.
	seen = map[string]BoxInfo{newIP: {Host: newIP, Port: 8888, Kind: "str", DeviceID: idA}}
	a.mergeDiscoveryCache(seen)
	if _, ok := seen[oldIP]; ok {
		t.Fatal("the old address is still served after the speaker turned up at its new one")
	}
	if _, ok := a.discCache[oldIP]; ok {
		t.Fatal("the old address is still cached")
	}
	if _, ok := a.otaPinned[oldIP]; ok {
		t.Fatal("the pin on the old address survived the move")
	}
	if seen[newIP].FriendlyName != "Kitchen" {
		t.Errorf("name at the new address = %q, want the remembered name carried over", seen[newIP].FriendlyName)
	}

	// The next cycles, before and after the pin would have expired.
	for i := 0; i < 2; i++ {
		seen = map[string]BoxInfo{newIP: {Host: newIP, Port: 8888, Kind: "str", DeviceID: idA}}
		a.mergeDiscoveryCache(seen)
		if _, ok := seen[oldIP]; ok {
			t.Fatalf("cycle %d: the old address came back", i)
		}
	}
}

// A pin whose speaker discovery never tied to an id still keeps the tile while
// the speaker restarts, but the stand-in record is never cached: once the pin
// expires there is nothing left at the address.
func TestPinWithoutIdentityIsServedNotCached(t *testing.T) {
	const ip = "192.0.2.42"
	a := moveTestApp()
	a.notePostOTA(ip)
	seen := map[string]BoxInfo{}
	a.mergeDiscoveryCache(seen)
	if b, ok := seen[ip]; !ok || b.Kind != "str" {
		t.Fatalf("pinned address not served while the speaker restarts: %+v", b)
	}
	if _, ok := a.discCache[ip]; ok {
		t.Fatal("an id-less stand-in record was cached")
	}
	a.discMu.Lock()
	a.otaPinned[ip] = time.Now().Add(-otaRebootGrace - time.Minute)
	a.discMu.Unlock()
	seen = map[string]BoxInfo{}
	a.mergeDiscoveryCache(seen)
	if _, ok := seen[ip]; ok {
		t.Fatal("the stand-in record outlived its pin")
	}
}

// Another speaker taking the pinned address ends the pin: its own record
// stands, and it is not merged into the old speaker's.
func TestAnotherSpeakerAtTheAddressIsNotMergedIntoTheOldRecord(t *testing.T) {
	const ip = "192.0.2.43"
	a := moveTestApp()
	a.discCache[ip] = discEntry{box: BoxInfo{Host: ip, Port: 8888, Kind: "str", DeviceID: idA, Name: "Living room", Model: "Wireless Link Adapter"}, seen: time.Now()}
	a.notePostOTA(ip)

	seen := map[string]BoxInfo{ip: {Host: ip, Port: 8888, Kind: "str", DeviceID: idB}}
	a.mergeDiscoveryCache(seen)
	b := seen[ip]
	if b.Name == "Living room" || b.Model == "Wireless Link Adapter" {
		t.Errorf("the other speaker inherited the old speaker's record: %+v", b)
	}
	if b.DeviceID != idB {
		t.Errorf("deviceID = %q, want the speaker that answers", b.DeviceID)
	}
	if b.OTAPending {
		t.Error("the other speaker was marked as mid-update")
	}
	if _, ok := a.otaPinned[ip]; ok {
		t.Error("the pin survived another speaker answering at its address")
	}
}

func TestDifferentDevices(t *testing.T) {
	cases := []struct {
		a, b BoxInfo
		want bool
	}{
		{BoxInfo{DeviceID: idA}, BoxInfo{DeviceID: idB}, true},
		{BoxInfo{DeviceID: idA}, BoxInfo{DeviceID: strings.ToLower(idA)}, false},
		{BoxInfo{DeviceID: idA}, BoxInfo{}, false},
		{BoxInfo{}, BoxInfo{DeviceID: idB}, false},
		// The firmware id on one side names the other: the same speaker.
		{BoxInfo{DeviceID: idA, BoxDeviceID: idB}, BoxInfo{DeviceID: idB}, false},
		{BoxInfo{DeviceID: idA}, BoxInfo{DeviceID: idB, BoxDeviceID: idA}, false},
	}
	for i, c := range cases {
		if got := differentDevices(c.a, c.b); got != c.want {
			t.Errorf("case %d: differentDevices = %v, want %v", i, got, c.want)
		}
	}
}

// The late confirmation follows the speaker by its id: another speaker on the
// new build at the old address does not confirm it, the speaker itself at its
// new address does, on the update's own journal thread.
func TestLateConfirmationFindsTheSpeakerAtItsNewAddress(t *testing.T) {
	journal := useTempJournal(t)
	oldBuild := appBuild
	appBuild = "b5"
	t.Cleanup(func() { appBuild = oldBuild })

	const oldIP, newIP = "192.0.2.50", "192.0.2.51"
	a := fastTestApp()
	a.discCache = map[string]discEntry{oldIP: {box: BoxInfo{Host: oldIP, DeviceID: idA}}}
	a.rememberOTAPort(oldIP, 8888)
	a.noteOTAUnconfirmed(oldIP, verdictAddressChanged)

	a.confirmLateOTA(map[string]BoxInfo{oldIP: {Host: oldIP, Port: 8888, Kind: "str", Version: "v2", Build: "b5", DeviceID: idB}}, nil)
	if strings.Contains(readJournal(t, journal), "confirmed late") {
		t.Fatal("another speaker at the old address confirmed this speaker's update")
	}

	a.confirmLateOTA(map[string]BoxInfo{newIP: {Host: newIP, Port: 8888, Kind: "str", Version: "v2", Build: "b5", DeviceID: idA}}, nil)
	j := readJournal(t, journal)
	if !strings.Contains(j, "host="+oldIP+" outcome: confirmed late by discovery - the speaker got a new address") || !strings.Contains(j, newIP+":8888") {
		t.Fatalf("journal lacks the corrective line for the moved speaker:\n%s", j)
	}
	a.confirmLateOTA(map[string]BoxInfo{newIP: {Host: newIP, Port: 8888, Kind: "str", Version: "v2", Build: "b5", DeviceID: idA}}, nil)
	if n := strings.Count(readJournal(t, journal), "confirmed late by discovery"); n != 1 {
		t.Errorf("corrective line written %d times, want exactly 1", n)
	}
}

// A speaker found by the network search at a new address replaces its entry at
// the old one, and nothing else: another speaker that is merely silent keeps
// its entry (#1852).
func TestSpeakerFoundAtANewAddressReplacesItsOldEntry(t *testing.T) {
	const oldIP, newIP, otherIP = "192.0.2.60", "192.0.2.61", "192.0.2.62"
	a := moveTestApp()
	a.discCache[oldIP] = discEntry{box: BoxInfo{Host: oldIP, Port: 8888, Kind: "str", DeviceID: idA, FriendlyName: "Kitchen"}, seen: time.Now()}
	a.discCache[otherIP] = discEntry{box: BoxInfo{Host: otherIP, Port: 8888, Kind: "str", DeviceID: idB, FriendlyName: "Bath"}, seen: time.Now()}

	seen := map[string]BoxInfo{newIP: {Host: newIP, Port: 8888, Kind: "str", DeviceID: idA}}
	a.mergeDiscoveryCache(seen)
	if _, ok := seen[oldIP]; ok {
		t.Error("the old address is still served next to the new one")
	}
	if _, ok := a.discCache[oldIP]; ok {
		t.Error("the old address is still cached")
	}
	if seen[newIP].FriendlyName != "Kitchen" {
		t.Errorf("name at the new address = %q, want it carried over", seen[newIP].FriendlyName)
	}
	if b, ok := seen[otherIP]; !ok || b.DeviceID != idB {
		t.Errorf("another speaker that is only silent lost its entry: %+v (present %v)", b, ok)
	}
}

// One sighting can carry the agent's own id and the next the id the firmware
// calls the speaker. The old entry is tied to the new one by either.
func TestSpeakerIsFollowedByItsFirmwareID(t *testing.T) {
	const oldIP, newIP = "192.0.2.63", "192.0.2.64"
	cases := []struct {
		name        string
		cached, now BoxInfo
	}{
		{"cached under the agent id, answers under the firmware id",
			BoxInfo{Host: oldIP, Kind: "str", DeviceID: idA, BoxDeviceID: idB},
			BoxInfo{Host: newIP, Kind: "str", DeviceID: idB, BoxDeviceID: idB}},
		{"cached under the firmware id, answers under the agent id",
			BoxInfo{Host: oldIP, Kind: "str", DeviceID: idB},
			BoxInfo{Host: newIP, Kind: "str", DeviceID: idA, BoxDeviceID: idB}},
	}
	for _, c := range cases {
		a := moveTestApp()
		a.discCache[oldIP] = discEntry{box: c.cached, seen: time.Now()}
		seen := map[string]BoxInfo{newIP: c.now}
		a.mergeDiscoveryCache(seen)
		if _, ok := seen[oldIP]; ok {
			t.Errorf("%s: the old address is still served", c.name)
		}
		if _, ok := a.discCache[oldIP]; ok {
			t.Errorf("%s: the old address is still cached", c.name)
		}
	}
}

// A record without any id is never tied to another speaker.
func TestEntryWithoutAnIDIsNotReplaced(t *testing.T) {
	const oldIP, newIP = "192.0.2.65", "192.0.2.66"
	a := moveTestApp()
	a.discCache[oldIP] = discEntry{box: BoxInfo{Host: oldIP, Kind: "stock"}, seen: time.Now()}
	seen := map[string]BoxInfo{newIP: {Host: newIP, Kind: "str", DeviceID: idA, BoxDeviceID: idA}}
	a.mergeDiscoveryCache(seen)
	if _, ok := seen[oldIP]; !ok {
		t.Error("a speaker without an id was dropped because another one was found")
	}
}

// The routed-network case: the speaker was connected by its address, got a new
// one, and was connected by that. Nothing but the refresh of the known speakers
// sees it, so that refresh has to replace the old entry, in what it returns and
// in the file the next start reads (#1852).
func TestSpeakerConnectedByAddressReplacesItsOldEntry(t *testing.T) {
	isolateConfigDir(t)
	const oldIP, newIP, otherIP = "192.0.2.70", "192.0.2.71", "192.0.2.72"
	a := moveTestApp()
	moved := knownBoxFixture(oldIP, idA)
	other := knownBoxFixture(otherIP, idB)
	other.Kind = "stock"
	a.discCache[oldIP] = discEntry{box: moved, seen: time.Now()}
	a.discCache[otherIP] = discEntry{box: other, seen: time.Now()}
	a.persistKnownSpeakers()
	// What AddBoxByIP leaves behind for the address that was typed in.
	atNew := knownBoxFixture(newIP, idA)
	a.discCache[newIP] = discEntry{box: atNew, seen: time.Now()}

	a.probeSTRFn = func(ctx context.Context, host string) (BoxInfo, bool) {
		if host == newIP {
			return atNew, true
		}
		return BoxInfo{}, false
	}
	a.portOpenFn = func(host string, port int, timeoutMs int) bool { return false }

	out, err := a.RefreshKnownBoxes()
	if err != nil {
		t.Fatalf("RefreshKnownBoxes: %v", err)
	}
	hosts := map[string]bool{}
	for _, b := range out {
		hosts[b.Host] = true
	}
	if hosts[oldIP] {
		t.Error("the refresh still returns the old address next to the new one")
	}
	if !hosts[newIP] || !hosts[otherIP] {
		t.Errorf("returned hosts = %v, want the new address and the silent bystander", hosts)
	}
	if _, ok := a.discCache[oldIP]; ok {
		t.Error("the old address is still cached")
	}

	a.persistKnownSpeakers()
	list, ok := readKnownSpeakersFile(t)
	if !ok {
		t.Fatal("known-speakers.json is missing")
	}
	stored := map[string]bool{}
	for _, k := range list {
		stored[k.Host] = true
	}
	if stored[oldIP] || !stored[newIP] || !stored[otherIP] {
		t.Errorf("known-speakers.json = %v, want the new address in place of the old one", list)
	}
}
