package main

import "testing"

// TestResyncSafeSource pins the allowlist: input source names differ per model
// (CineMate says LOCAL where an ST10 says AUX, an SA-5 answers AUX1..AUX3), so
// anything we do not explicitly know must count as the user's choice and block
// the key-registration write, which would otherwise switch the box to UPNP.
func TestResyncSafeSource(t *testing.T) {
	for _, src := range []string{"UPNP", "INVALID_SOURCE"} {
		if !resyncSafeSource(src) {
			t.Errorf("%s must allow the re-sync (idle or our own source)", src)
		}
	}
	for _, src := range []string{
		"BLUETOOTH", "AUX", "AUX1", "LOCAL", "SPOTIFY", "DEEZER",
		"AIRPLAY", "TUNEIN", "STORED_MUSIC", "QPLAY", "PRODUCT", "HDMI_1",
		"STANDBY", "",
	} {
		if resyncSafeSource(src) {
			t.Errorf("%s is a user-chosen, idle-unknown or unknown source: the re-sync must not run", src)
		}
	}
}
