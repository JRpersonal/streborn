package main

import (
	"log/slog"
	"testing"
)

// TestEngineDeliverySkippedWhileUninstalling: once an uninstall has started
// for a speaker, the post-update engine delivery must not push anything onto
// it and must answer with the skip marker the update flow keeps quiet about.
func TestEngineDeliverySkippedWhileUninstalling(t *testing.T) {
	a := &App{logger: slog.Default()}
	if a.isUninstalling("192.0.2.60") {
		t.Fatal("fresh app must not report an uninstall")
	}
	a.uninstalling.Store("192.0.2.60", struct{}{})
	got, err := a.EnsureSpotifyEngine("192.0.2.60", 17008)
	if err != nil || got != EngineSkippedUninstalling {
		t.Fatalf("got %q, %v; want the skip marker and no error", got, err)
	}
	// Another speaker is unaffected (dev builds carry no engine, so the
	// answer is the "no embedded engine" one, never a network call).
	got, err = a.EnsureSpotifyEngine("192.0.2.61", 17008)
	if err != nil || got == EngineSkippedUninstalling {
		t.Fatalf("other host: got %q, %v", got, err)
	}
}

// TestReinstallLiftsTheUninstallMark: a speaker removed earlier in the session
// and set up again must get its engine delivery back.
func TestReinstallLiftsTheUninstallMark(t *testing.T) {
	a := &App{logger: slog.Default()}
	a.uninstalling.Store("192.0.2.60", struct{}{})
	a.uninstalling.Delete("192.0.2.60") // what InstallSTROnBox does first
	if a.isUninstalling("192.0.2.60") {
		t.Fatal("mark must be lifted by a new install")
	}
}
