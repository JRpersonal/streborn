package main

import (
	"context"
	"testing"
	"time"
)

// knownBoxFixture returns a cached STR box whose SerialNumber and Model are
// pre-filled so enrichBoxWithStockInfo short-circuits without network I/O.
func knownBoxFixture(host, deviceID string) BoxInfo {
	return BoxInfo{
		Host:         host,
		Port:         8888,
		Kind:         "str",
		DeviceID:     deviceID,
		Model:        "SoundTouch 10",
		SerialNumber: "serial-" + deviceID,
		FriendlyName: "Box " + deviceID,
		Version:      "0.9.0",
	}
}

func TestClassifyKnownBox(t *testing.T) {
	cached := knownBoxFixture("192.0.2.30", "DEVC")
	probed := knownBoxFixture("192.0.2.30", "DEVC")
	probed.Version = "0.9.1" // the fresh probe carries newer live values

	cases := []struct {
		name             string
		probeOK          bool
		bosePortOpen     bool
		wantRecord       BoxInfo
		wantLive         bool
		wantSTRConfirmed bool
	}{
		{"agent answered", true, false, probed, true, true},
		{"stock 8090 only", false, true, cached, true, false},
		{"nothing answered", false, false, cached, false, false},
	}
	for _, c := range cases {
		record, live, strConfirmed := classifyKnownBox(cached, probed, c.probeOK, c.bosePortOpen)
		if record != c.wantRecord || live != c.wantLive || strConfirmed != c.wantSTRConfirmed {
			t.Errorf("%s: classifyKnownBox = (%+v, %v, %v), want (%+v, %v, %v)",
				c.name, record, live, strConfirmed, c.wantRecord, c.wantLive, c.wantSTRConfirmed)
		}
	}
}

// The producer contract behind the offline-eviction fix (98883aa): a box that
// answers NO probe this cycle must stay in the returned list (visible through
// its grace window) but must NOT have its presence refreshed — re-merging it
// into the cache every refresh is what kept a powered-off Wave listed forever.
func TestRefreshKnownBoxesOfflineBoxKeepsAgingOut(t *testing.T) {
	a := testApp()
	oldSeen := time.Now().Add(-time.Hour)
	live := knownBoxFixture("192.0.2.31", "DEVL")
	offline := knownBoxFixture("192.0.2.32", "DEVO")
	a.discCache = map[string]discEntry{
		live.Host:    {box: live, seen: oldSeen},
		offline.Host: {box: offline, seen: oldSeen},
	}
	a.probeSTRFn = func(ctx context.Context, host string) (BoxInfo, bool) {
		if host == live.Host {
			return live, true
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
	if !hosts[live.Host] || !hosts[offline.Host] {
		t.Fatalf("both boxes must stay visible on the first missed cycle, got %v", hosts)
	}

	le, ok := a.discCache[live.Host]
	if !ok || !le.seen.After(oldSeen) {
		t.Errorf("live box must refresh its presence timestamp, entry=%+v", le)
	}
	if !le.firstMiss.IsZero() {
		t.Errorf("live box must not carry a miss streak, firstMiss=%v", le.firstMiss)
	}
	oe, ok := a.discCache[offline.Host]
	if !ok {
		t.Fatalf("offline box must remain cached through the grace window")
	}
	if !oe.seen.Equal(oldSeen) {
		t.Errorf("offline box presence must NOT refresh (the pre-98883aa bug), seen=%v want %v", oe.seen, oldSeen)
	}
	if oe.firstMiss.IsZero() {
		t.Errorf("offline box must start its miss streak so eviction can come due")
	}
}

// A box that answers only on the stock :8090 (agent mid-reboot, or genuinely
// reverted to stock) must count as PRESENT — its tile stays and its eviction
// timer resets — without counting as an STR sighting: the deviceID-keyed STR
// identity memory must keep its old timestamp so a permanently-reverted box
// can eventually reclassify as stock (strKnownTTL).
func TestRefreshKnownBoxesPresenceOnlyDoesNotConfirmSTR(t *testing.T) {
	a := testApp()
	box := knownBoxFixture("192.0.2.33", "DEVP")
	memoSeen := time.Now().Add(-2 * time.Hour)
	a.discCache = map[string]discEntry{
		box.Host: {box: box, seen: time.Now().Add(-time.Hour), firstMiss: time.Now().Add(-time.Minute)},
	}
	a.strKnown = map[string]discEntry{
		box.DeviceID: {box: box, seen: memoSeen},
	}
	a.probeSTRFn = func(ctx context.Context, host string) (BoxInfo, bool) { return BoxInfo{}, false }
	a.portOpenFn = func(host string, port int, timeoutMs int) bool { return port == 8090 }

	out, err := a.RefreshKnownBoxes()
	if err != nil {
		t.Fatalf("RefreshKnownBoxes: %v", err)
	}
	if len(out) != 1 || out[0].Host != box.Host {
		t.Fatalf("presence-only box must stay listed, got %v", out)
	}
	e, ok := a.discCache[box.Host]
	if !ok {
		t.Fatalf("presence-only box must stay cached")
	}
	if !e.firstMiss.IsZero() {
		t.Errorf("presence must clear the miss streak, firstMiss=%v", e.firstMiss)
	}
	if got := a.strKnown[box.DeviceID].seen; !got.Equal(memoSeen) {
		t.Errorf("a :8090-only sighting must not refresh the STR identity memory: got %v, want %v", got, memoSeen)
	}
}
