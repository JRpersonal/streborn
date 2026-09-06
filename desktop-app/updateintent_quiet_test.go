package main

import (
	"log/slog"
	"testing"
)

// TestPendingIntentIsQuietWhileASpeakerIsBeingWritten: the unfinished-update
// reminder must not judge a speaker mid-install or mid-update.
func TestPendingIntentIsQuietWhileASpeakerIsBeingWritten(t *testing.T) {
	a := &App{logger: slog.Default()}
	otaRunning.Store(true)
	defer otaRunning.Store(false)
	if got := a.PendingUpdateIntent("192.0.2.60", 8888); got["action"] != "" || got["reason"] == "" {
		t.Fatalf("got %v", got)
	}
	otaRunning.Store(false)
	release, err := writesInFlight.claim("192.0.2.61", "an install")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if !writesInFlight.isBusy("192.0.2.61") || writesInFlight.isBusy("192.0.2.62") {
		t.Fatal("isBusy must follow the claim")
	}
	if got := a.PendingUpdateIntent("192.0.2.61", 8888); got["action"] != "" {
		t.Fatalf("busy host: got %v", got)
	}
}
