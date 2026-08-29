package main

import "testing"

// The restore has to find "the same speaker" months after the backup was
// written: the deviceID decides when it matches, and only the display name is
// left when it does not (agent reinstalled, id recorded differently). A wrong
// match would write presets onto somebody else's speaker, so the matcher is
// pinned down here.
func TestBackupMatchTarget(t *testing.T) {
	boxes := []BoxInfo{
		{Host: "192.0.2.10", DeviceID: "AABBCCDDEE01", FriendlyName: "Küche"},
		{Host: "192.0.2.11", DeviceID: "AABBCCDDEE02", Name: "str-192.0.2.11"},
		{Host: "192.0.2.12", DeviceID: "AABBCCDDEE03", FriendlyName: "Büro"},
	}

	t.Run("deviceID wins, case-insensitively", func(t *testing.T) {
		got := backupMatchTarget(backupSpeaker{Name: "Büro", DeviceID: "aabbccddee01"}, boxes)
		if got == nil || got.Host != "192.0.2.10" {
			t.Fatalf("expected the deviceID match to beat the name, got %+v", got)
		}
	})

	t.Run("name fallback ignores case and spacing", func(t *testing.T) {
		got := backupMatchTarget(backupSpeaker{Name: "  küche "}, boxes)
		if got == nil || got.Host != "192.0.2.10" {
			t.Fatalf("expected the name fallback to match, got %+v", got)
		}
	})

	t.Run("name fallback also reads the bare Name field", func(t *testing.T) {
		got := backupMatchTarget(backupSpeaker{Name: "str-192.0.2.11"}, boxes)
		if got == nil || got.Host != "192.0.2.11" {
			t.Fatalf("expected the Name-field fallback to match, got %+v", got)
		}
	})

	t.Run("no identity, no guess", func(t *testing.T) {
		if got := backupMatchTarget(backupSpeaker{Name: "Wohnzimmer", DeviceID: "FFFFFFFFFFFF"}, boxes); got != nil {
			t.Fatalf("an unknown speaker must not match anything, got %+v", got)
		}
		if got := backupMatchTarget(backupSpeaker{}, boxes); got != nil {
			t.Fatalf("an empty entry must not match anything, got %+v", got)
		}
	})
}
