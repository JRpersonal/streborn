package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Removing STR and flashing Bose's firmware both end in a restart of the
// speaker, and neither used to take the one-writer-per-speaker claim that
// installs and updates take. A removal started under a running update deleted
// the folder the update was writing into; a flash restarted the speaker in the
// middle of an install.

func TestUninstallIsRefusedWhileTheSpeakerIsBeingWrittenTo(t *testing.T) {
	const host = "192.0.2.61" // TEST-NET-1: nothing answers there
	release, err := writesInFlight.claim(host, "an update")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	a := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	begin := time.Now()
	res := a.UninstallSTR(host)
	if res.OK || res.Step != "busy" {
		t.Fatalf("step = %q ok = %v, want the removal refused as busy", res.Step, res.OK)
	}
	if !strings.HasPrefix(res.Message, errBoxBusyToken) || !strings.Contains(res.Message, "an update is running") {
		t.Errorf("message = %q, want the same refusal an install or update gives", res.Message)
	}
	if took := time.Since(begin); took > 2*time.Second {
		t.Errorf("the refusal took %v: it must come before anything talks to the speaker", took)
	}
	// A refused removal must not leave the speaker marked as being uninstalled,
	// or the running update could not deliver its Spotify engine.
	if _, marked := a.uninstalling.Load(host); marked {
		t.Error("the refused removal left its uninstalling mark behind")
	}
}

func TestFirmwareFlashIsRefusedWhileTheSpeakerIsBeingWrittenTo(t *testing.T) {
	const host = "192.0.2.62"
	release, err := writesInFlight.claim(host, "an install")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	a := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	begin := time.Now()
	_, err = a.flashBoseFirmware(context.Background(), " "+host+" ", true)
	if err == nil || !strings.HasPrefix(err.Error(), errBoxBusyToken) || !strings.Contains(err.Error(), "an install is running") {
		t.Fatalf("err = %v, want the same refusal an install or update gives", err)
	}
	if took := time.Since(begin); took > 2*time.Second {
		t.Errorf("the refusal took %v: it must come before anything talks to the speaker", took)
	}
}

// A flash that ends, however it ends, gives the speaker back, and holds it
// while it runs.
func TestFirmwareFlashHoldsAndReleasesTheSpeaker(t *testing.T) {
	const host = "192.0.2.63"
	heldDuring := false
	a := &App{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			heldDuring = writesInFlight.isBusy(host)
			return nil, errors.New("the speaker does not answer in this test")
		})},
	}
	if _, err := a.flashBoseFirmware(context.Background(), host, false); err == nil {
		t.Fatal("a flash of a speaker that does not answer cannot succeed")
	}
	if !heldDuring {
		t.Error("the flash talked to the speaker without claiming it")
	}
	if writesInFlight.isBusy(host) {
		t.Error("the failed flash kept the speaker claimed")
	}
}
