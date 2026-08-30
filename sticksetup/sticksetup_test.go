package sticksetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An SSID with an empty password must never reach the stick: applied on the
// box it provisions an OPEN-network profile and kicks the speaker off a
// protected home Wi-Fi right after an otherwise clean install (field report
// 2026-08-29). A stale wlan.conf from an earlier attempt is removed too.
func TestWriteWLANConfigRefusesEmptyPassword(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "wlan.conf")
	if err := os.WriteFile(stale, []byte(`{"ssid":"Home","password":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := WriteWLANConfig(dir, WLANConfig{SSID: "Home", Password: "   "})
	if err == nil {
		t.Fatal("empty password must be refused")
	}
	if _, statErr := os.Stat(stale); !os.IsNotExist(statErr) {
		t.Error("a stale wlan.conf must be removed when the write is refused")
	}
	if err := WriteWLANConfig(dir, WLANConfig{SSID: "Home", Password: "secret123"}); err != nil {
		t.Fatalf("a real password must still write: %v", err)
	}
}

// Special characters must survive the trip onto the stick byte-exact. Go's
// default JSON marshalling HTML-escaped ampersands and angle brackets, and
// the stick's sed parser then read those escapes literally: a password with
// an ampersand was provisioned wrong and the speaker never joined (field,
// 2026-08-30). The raw sidecar files are the escape-free transport run.sh
// prefers; wlan.conf stays escape-free too for older readers.
func TestWriteWLANConfigSpecialCharacters(t *testing.T) {
	dir := t.TempDir()
	cfg := WLANConfig{SSID: `FRITZ!Box 5690 "Pro" & Co <5GHz>`, Password: `pa&ss<w>ord"quote\back`}
	if err := WriteWLANConfig(dir, cfg); err != nil {
		t.Fatalf("write: %v", err)
	}
	ssid, err := os.ReadFile(filepath.Join(dir, "wlan.ssid"))
	if err != nil || string(ssid) != cfg.SSID {
		t.Fatalf("wlan.ssid = %q, %v; want the exact SSID", ssid, err)
	}
	pass, err := os.ReadFile(filepath.Join(dir, "wlan.pass"))
	if err != nil || string(pass) != cfg.Password {
		t.Fatalf("wlan.pass = %q, %v; want the exact password", pass, err)
	}
	conf, err := os.ReadFile(filepath.Join(dir, "wlan.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if bytesContains(conf, `\u0026`) || bytesContains(conf, `\u003c`) {
		t.Fatalf("wlan.conf still HTML-escapes: %s", conf)
	}

	if err := WriteWLANConfig(dir, WLANConfig{SSID: "Home", Password: "with\nnewline"}); err == nil {
		t.Fatal("a line break in the password must be refused")
	}

	// The empty-password refusal must clear the sidecars too, or a stale
	// pair keeps provisioning the previous network.
	if err := WriteWLANConfig(dir, WLANConfig{SSID: "Home", Password: " "}); err == nil {
		t.Fatal("empty password must be refused")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "wlan.ssid")); !os.IsNotExist(statErr) {
		t.Error("a stale wlan.ssid must be removed when the write is refused")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "wlan.pass")); !os.IsNotExist(statErr) {
		t.Error("a stale wlan.pass must be removed when the write is refused")
	}
}

func bytesContains(b []byte, s string) bool { return strings.Contains(string(b), s) }
