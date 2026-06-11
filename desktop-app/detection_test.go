package main

import (
	"testing"
	"time"
)

// These tests lock in the speaker-detection invariants that have repeatedly
// regressed (#108): a flashed speaker must never be downgraded to "stock"
// (which prompts a needless USB-stick reinstall), and a real speaker name must
// never be lost to the generic "str-<ip>" / "Bose SoundTouch <id>" fallback
// when one discovery cycle comes back thin.

func TestIsGenericBoxName(t *testing.T) {
	generic := []string{"", "   ", "Bose SoundTouch 0A1B2C"}
	for _, n := range generic {
		if !isGenericBoxName(n) {
			t.Errorf("isGenericBoxName(%q) = false, want true", n)
		}
	}
	real := []string{"Wohnzimmer", "Kitchen", "str-192.168.0.5"}
	for _, n := range real {
		if isGenericBoxName(n) {
			t.Errorf("isGenericBoxName(%q) = true, want false", n)
		}
	}
}

// A known-STR box that this cycle was only seen as a thin stock /info hit
// (probeSTR missed it) must stay classified STR, keep its version, and keep
// its verified port. Otherwise the app shows "needs install" for a speaker
// that already runs STR.
func TestMergeBoxInfoStrNotDowngradedToStock(t *testing.T) {
	prev := BoxInfo{
		Kind: "str", FriendlyName: "Wohnzimmer", Version: "v0.7.1", Build: "b1",
		Port: 17008, PortVerified: true, Host: "192.168.0.5",
	}
	cur := BoxInfo{Kind: "stock", Host: "192.168.0.5"} // thin stock-only sighting

	out := mergeBoxInfo(prev, cur)
	if out.Kind != "str" {
		t.Errorf("Kind = %q, want str (must not downgrade a flashed speaker)", out.Kind)
	}
	if out.Version != "v0.7.1" {
		t.Errorf("Version = %q, want v0.7.1", out.Version)
	}
	if out.FriendlyName != "Wohnzimmer" {
		t.Errorf("FriendlyName = %q, want Wohnzimmer", out.FriendlyName)
	}
	if !out.PortVerified || out.Port != 17008 {
		t.Errorf("port = %d verified=%v, want 17008 verified", out.Port, out.PortVerified)
	}
}

// A thin cycle that lost the friendly name must not blank a name the user
// already saw (the "str-<ip>" flicker, #108).
func TestMergeBoxInfoKeepsRealName(t *testing.T) {
	prev := BoxInfo{Kind: "str", FriendlyName: "Kitchen", Host: "192.168.0.6"}
	cur := BoxInfo{Kind: "str", FriendlyName: "", Host: "192.168.0.6"}
	if out := mergeBoxInfo(prev, cur); out.FriendlyName != "Kitchen" {
		t.Errorf("FriendlyName = %q, want Kitchen (must not lose the name)", out.FriendlyName)
	}
}

// mergeSameKind must take the best of each source: the real name from the mDNS
// record and the fresh version + verified port from the live probe. Picking one
// whole record dropped either the name or the new version (the two halves of
// #108).
func TestMergeSameKindKeepsNameAndFreshVersion(t *testing.T) {
	mdns := BoxInfo{ // carries the real name but a stale version, not port-verified
		Kind: "str", FriendlyName: "Wohnzimmer", Version: "v0.7.0", Port: 8888, PortVerified: false,
	}
	probe := BoxInfo{ // fresh version + verified reachable port, no name
		Kind: "str", FriendlyName: "", Version: "v0.7.1", Port: 17008, PortVerified: true,
	}
	out := mergeSameKind(mdns, probe)
	if out.FriendlyName != "Wohnzimmer" {
		t.Errorf("FriendlyName = %q, want Wohnzimmer", out.FriendlyName)
	}
	if out.Version != "v0.7.1" {
		t.Errorf("Version = %q, want v0.7.1 (verified probe wins)", out.Version)
	}
	if !out.PortVerified || out.Port != 17008 {
		t.Errorf("port = %d verified=%v, want 17008 verified", out.Port, out.PortVerified)
	}
}

// After STR triggers an OTA, a stock sighting during the box's reboot (its Bose
// :8090 answers before the agent) must NOT reclassify the box as stock: the
// post-OTA pin forces it to stay STR for the reboot grace (#108).
func TestPostOTAPinForcesStr(t *testing.T) {
	a := &App{
		discCache: map[string]discEntry{},
		otaPinned: map[string]time.Time{"192.168.0.7": time.Now()},
	}
	// This cycle only saw the box as a stock /info hit (agent still rebooting).
	seen := map[string]BoxInfo{"192.168.0.7": {Kind: "stock", Host: "192.168.0.7"}}
	a.mergeDiscoveryCache(seen)
	if got := seen["192.168.0.7"].Kind; got != "str" {
		t.Errorf("Kind = %q, want str (post-OTA pin must keep it STR through reboot)", got)
	}
}

// A box mid-reboot that is not seen at all this cycle (neither agent nor stock
// answered) must still be re-added as STR while the OTA pin is fresh, so it does
// not vanish from the list during the reboot.
func TestPostOTAPinReaddsMissingBox(t *testing.T) {
	a := &App{
		discCache: map[string]discEntry{},
		otaPinned: map[string]time.Time{"192.168.0.8": time.Now()},
	}
	seen := map[string]BoxInfo{} // nothing visible this cycle
	a.mergeDiscoveryCache(seen)
	b, ok := seen["192.168.0.8"]
	if !ok || b.Kind != "str" {
		t.Errorf("box missing or not STR: ok=%v kind=%q, want present and STR", ok, b.Kind)
	}
}

// An expired OTA pin must stop forcing STR so a box genuinely reverted to stock
// can correct itself.
func TestPostOTAPinExpires(t *testing.T) {
	a := &App{
		discCache: map[string]discEntry{},
		otaPinned: map[string]time.Time{"192.168.0.9": time.Now().Add(-otaRebootGrace - time.Minute)},
	}
	seen := map[string]BoxInfo{"192.168.0.9": {Kind: "stock", Host: "192.168.0.9"}}
	a.mergeDiscoveryCache(seen)
	if got := seen["192.168.0.9"].Kind; got != "stock" {
		t.Errorf("Kind = %q, want stock (an expired pin must not force STR)", got)
	}
	if _, still := a.otaPinned["192.168.0.9"]; still {
		t.Errorf("expired pin should have been evicted")
	}
}

func TestBlockDeviceBase(t *testing.T) {
	cases := map[string]string{
		"/dev/sda1": "sda",
		"/dev/sdb":  "sdb",
		"/dev/sdc1": "sdc",
		"":          "",
		"/dev/":     "",
		"bad path":  "",
	}
	for in, want := range cases {
		if got := blockDeviceBase(in); got != want {
			t.Errorf("blockDeviceBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLineValue(t *testing.T) {
	out := "noise\nSTR_STICK_MP=/tmp/str-stick\nSTR_STICK_DEV=/dev/sda1\nDONE"
	if got := lineValue(out, "STR_STICK_MP="); got != "/tmp/str-stick" {
		t.Errorf("lineValue MP = %q, want /tmp/str-stick", got)
	}
	if got := lineValue(out, "STR_STICK_DEV="); got != "/dev/sda1" {
		t.Errorf("lineValue DEV = %q, want /dev/sda1", got)
	}
	if got := lineValue(out, "MISSING="); got != "" {
		t.Errorf("lineValue MISSING = %q, want empty", got)
	}
}
