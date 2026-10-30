package main

import (
	"context"
	"reflect"
	"testing"
)

// #165: a speaker on the cable AND on Wi-Fi answers on two addresses. It must
// show as one speaker, on the cable address while that answers, and move to
// the Wi-Fi address when the cable one goes silent.

const (
	wiredHost = "192.0.2.20"
	wifiHost  = "192.0.2.19" // sorts first: a name-order pick would keep it
)

// dualHomed returns the two sightings of one speaker, both carrying the
// agent's own address report.
func dualHomed() (wired, wifi BoxInfo) {
	wired = knownBoxFixture(wiredHost, "DEVW")
	wired.LANAddrs = wiredHost + "," + wifiHost
	wired.WiredAddrs = wiredHost
	wired.PortVerified = true
	wifi = wired
	wifi.Host = wifiHost
	return wired, wifi
}

func withReachable(t *testing.T, up map[string]bool) {
	t.Helper()
	old := boxKeyReachableFn
	boxKeyReachableFn = func(b BoxInfo) bool { return up[b.Host] }
	t.Cleanup(func() { boxKeyReachableFn = old })
}

func TestDedupeDualHomedPrefersCable(t *testing.T) {
	cases := []struct {
		name string
		up   map[string]bool
		want string
	}{
		{"both answer: the cable wins over the name order", map[string]bool{wiredHost: true, wifiHost: true}, wiredHost},
		{"the cable is silent: fall back to Wi-Fi", map[string]bool{wifiHost: true}, wifiHost},
		{"nothing answers: still one tile, on the cable", map[string]bool{}, wiredHost},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withReachable(t, c.up)
			wired, wifi := dualHomed()
			want := c.want
			a := testApp()
			seen := map[string]BoxInfo{wired.Host: wired, wifi.Host: wifi}
			a.dedupeByDeviceID(seen)
			if len(seen) != 1 {
				t.Fatalf("want one tile, got %d: %v", len(seen), seen)
			}
			if _, ok := seen[want]; !ok {
				t.Errorf("kept %v, want %s", seen, want)
			}
		})
	}
}

// The thinner sighting may lack the wired report; any record of the group
// saying "this host is the cable" is enough.
func TestDedupeWiredReportFromEitherSighting(t *testing.T) {
	withReachable(t, map[string]bool{wifiHost: true, wiredHost: true})
	wifi := knownBoxFixture("192.0.2.19", "DEVW")
	wifi.WiredAddrs = "192.0.2.20"
	wired := knownBoxFixture("192.0.2.20", "DEVW")
	seen := map[string]BoxInfo{wifi.Host: wifi, wired.Host: wired}
	testApp().dedupeByDeviceID(seen)
	if _, ok := seen["192.0.2.20"]; !ok || len(seen) != 1 {
		t.Errorf("want only the cable address, got %v", seen)
	}
}

func TestSameSpeakerGroups(t *testing.T) {
	a := knownBoxFixture("192.0.2.1", "DEV1")
	b := knownBoxFixture("192.0.2.2", "DEV1") // same id
	// Same speaker under another id (the agent's radio MAC instead of the
	// firmware id), tied to a by a's own address report.
	a.LANAddrs = "192.0.2.1,192.0.2.3"
	c := knownBoxFixture("192.0.2.3", "RADIO-MAC")
	d := knownBoxFixture("192.0.2.4", "DEV2") // another speaker
	e := knownBoxFixture("192.0.2.5", "")     // no id: never grouped
	f := knownBoxFixture("192.0.2.6", "")
	seen := map[string]BoxInfo{a.Host: a, b.Host: b, c.Host: c, d.Host: d, e.Host: e, f.Host: f}
	got := sameSpeakerGroups(seen)
	want := [][]string{{"192.0.2.1", "192.0.2.2", "192.0.2.3"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sameSpeakerGroups = %v, want %v", got, want)
	}
}

func TestSplitAddrList(t *testing.T) {
	if got := splitAddrList(" 192.0.2.1, ,192.0.2.2 "); !reflect.DeepEqual(got, []string{"192.0.2.1", "192.0.2.2"}) {
		t.Errorf("got %v", got)
	}
	if got := splitAddrList(""); got != nil {
		t.Errorf("empty: got %v", got)
	}
}

// The quick refresh must not grey the tile out when the cable address goes
// silent: it tries the speaker's other address and moves the tile there.
func TestRefreshKnownBoxesFallsBackToOtherAddress(t *testing.T) {
	a := testApp()
	wired, _ := dualHomed()
	a.discCache = map[string]discEntry{wired.Host: {box: wired}}
	var asked []string
	a.probeSTRFn = func(_ context.Context, host string) (BoxInfo, bool) {
		asked = append(asked, host)
		if host == wifiHost {
			b := knownBoxFixture(wifiHost, "") // thin: no /info answer yet
			b.PortVerified = true
			return b, true
		}
		return BoxInfo{}, false
	}
	a.portOpenFn = func(string, int, int) bool { return false }

	out, err := a.RefreshKnownBoxes()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Host != wifiHost || out[0].Offline {
		t.Fatalf("want one live tile at the Wi-Fi address, got %+v", out)
	}
	if out[0].DeviceID != "DEVW" {
		t.Errorf("the tile must keep its identity, got deviceID %q", out[0].DeviceID)
	}
	if _, ok := a.discCache[wiredHost]; ok {
		t.Errorf("the silent cable address must leave the cache, or the next refresh shows two tiles")
	}
	if !reflect.DeepEqual(asked, []string{wiredHost, wifiHost}) {
		t.Errorf("probe order = %v, want the tile's own address first", asked)
	}
}
