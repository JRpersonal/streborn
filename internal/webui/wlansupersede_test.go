package webui

import (
	"os"
	"path/filepath"
	"testing"
)

func intentPaths(t *testing.T) wlanIntentPaths {
	t.Helper()
	d := t.TempDir()
	return wlanIntentPaths{
		creds:   filepath.Join(d, "wlan-creds"),
		retired: filepath.Join(d, "wlan-creds.superseded"),
		target:  filepath.Join(d, "wlan-target"),
		known:   filepath.Join(d, "wlan-known.json"),
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// The support case: STR moved the speaker to OldNet; the owner later reset the
// speaker's Wi-Fi and joined NewNet through Bose's setup page. The firmware's
// store then holds OldNet, the setup AP's own open profile and NewNet.
func TestReconcile_OwnerJoinedNewNetworkRetiresSTRRecords(t *testing.T) {
	p := intentPaths(t)
	if err := writeWlanCredsFile(p.creds, "OldNet", "secret", false); err != nil {
		t.Fatal(err)
	}
	if err := writeWlanTargetAt(p.target, wlanTarget{SSID: "OldNet", PSK: "secret", Gen: 3}); err != nil {
		t.Fatal(err)
	}
	// First look: baseline only, never a verdict.
	before := []storedNet{{SSID: "OldNet"}, {SSID: "Neighbour"}}
	if got := reconcileWlanIntentAt(p, before, "", nil); got != "" {
		t.Fatalf("first look retired: %q", got)
	}
	if !exists(p.known) || !exists(p.creds) || !exists(p.target) {
		t.Fatal("first look must take a baseline and leave STR's records alone")
	}
	// Nothing new: still nothing.
	if got := reconcileWlanIntentAt(p, before, "", nil); got != "" {
		t.Fatalf("unchanged store retired: %q", got)
	}
	after := []storedNet{
		{SSID: "OldNet"}, {SSID: "Neighbour"},
		{SSID: "Bose SoundTouch Wi-Fi Network", Open: true},
		{SSID: "NewNet"},
	}
	if got := reconcileWlanIntentAt(p, after, "", nil); got != "NewNet" {
		t.Fatalf("superseding network = %q, want NewNet", got)
	}
	if exists(p.creds) || !exists(p.retired) || exists(p.target) || exists(p.known) {
		t.Fatalf("records not retired: creds=%v retired=%v target=%v known=%v",
			exists(p.creds), exists(p.retired), exists(p.target), exists(p.known))
	}
	if readWlanCredsSSIDAt(p.retired) != "OldNet" {
		t.Fatal("retired credentials lost their content")
	}
}

// Drift back to a network the speaker already knew (#461) is the guard's job
// and must never retire the target.
func TestReconcile_DriftToKnownNetworkKeepsTarget(t *testing.T) {
	p := intentPaths(t)
	_ = writeWlanCredsFile(p.creds, "HomeNet", "pw", false)
	_ = writeWlanTargetAt(p.target, wlanTarget{SSID: "HomeNet", PSK: "pw"})
	stored := []storedNet{{SSID: "OldNet"}, {SSID: "HomeNet"}}
	reconcileWlanIntentAt(p, stored, "", nil)
	if got := reconcileWlanIntentAt(p, stored, "OldNet", nil); got != "" {
		t.Fatalf("drift to a known network retired the target: %q", got)
	}
	if !exists(p.target) || !exists(p.creds) {
		t.Fatal("records touched on drift")
	}
}

// A live association to a network that appeared after the baseline is the
// strongest evidence, even before the store shows it.
func TestReconcile_LiveOnNewNetworkRetires(t *testing.T) {
	p := intentPaths(t)
	_ = writeWlanCredsFile(p.creds, "HomeNet", "pw", false)
	reconcileWlanIntentAt(p, []storedNet{{SSID: "HomeNet"}}, "HomeNet", nil)
	if got := reconcileWlanIntentAt(p, []storedNet{{SSID: "HomeNet"}}, "NewNet", nil); got != "NewNet" {
		t.Fatalf("got %q, want NewNet", got)
	}
	if exists(p.creds) {
		t.Fatal("credentials still in place")
	}
}

// Open networks and the setup AP are never the owner's choice of a home
// network.
func TestReconcile_OpenAndSetupAPNeverCount(t *testing.T) {
	p := intentPaths(t)
	_ = writeWlanCredsFile(p.creds, "HomeNet", "pw", false)
	reconcileWlanIntentAt(p, []storedNet{{SSID: "HomeNet"}}, "", nil)
	stored := []storedNet{
		{SSID: "HomeNet"},
		{SSID: "Bose SoundTouch Wi-Fi Network", Open: true},
		{SSID: "CafeGuest", Open: true},
	}
	if got := reconcileWlanIntentAt(p, stored, "Bose SoundTouch Wi-Fi Network", nil); got != "" {
		t.Fatalf("open network retired STR's records: %q", got)
	}
}

// New credentials written by STR retake the baseline instead of being compared
// against the old one.
func TestReconcile_NewCredentialsRetakeBaseline(t *testing.T) {
	p := intentPaths(t)
	_ = writeWlanCredsFile(p.creds, "HomeNet", "pw", false)
	reconcileWlanIntentAt(p, []storedNet{{SSID: "HomeNet"}}, "", nil)
	_ = writeWlanCredsFile(p.creds, "OfficeNet", "pw2", false)
	stored := []storedNet{{SSID: "HomeNet"}, {SSID: "OfficeNet"}, {SSID: "Other"}}
	if got := reconcileWlanIntentAt(p, stored, "", nil); got != "" {
		t.Fatalf("new STR credentials were compared against a stale baseline: %q", got)
	}
	k, ok := readWlanKnownAt(p.known)
	if !ok || k.CredsTag != ssidTag("OfficeNet") || len(k.Known) != 3 {
		t.Fatalf("baseline not retaken: %+v ok=%v", k, ok)
	}
}

func TestReconcile_NoSTRNetworkIsNoOp(t *testing.T) {
	p := intentPaths(t)
	if got := reconcileWlanIntentAt(p, []storedNet{{SSID: "HomeNet"}}, "HomeNet", nil); got != "" || exists(p.known) {
		t.Fatalf("a speaker STR never set up got a baseline or a verdict: %q", got)
	}
}
