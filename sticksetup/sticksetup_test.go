package sticksetup

import (
	"os"
	"path/filepath"
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
