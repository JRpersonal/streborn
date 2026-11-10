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

	"streborn-app/agentbin"
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

// The SSH repair behind the failure screen's button stops the speaker's agent,
// rewrites its NAND folder and restarts it, and it was the one writer left
// without the claim: a click on it under a running update or removal ran both
// against the same speaker.
func TestRepairIsRefusedWhileTheSpeakerIsBeingWrittenTo(t *testing.T) {
	const host = "192.0.2.64"
	release, err := writesInFlight.claim(host, "an update")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	a := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	begin := time.Now()
	res, err := a.RepairInstallViaSSH(host, "SoundTouch 20")
	if err == nil || !strings.HasPrefix(err.Error(), errBoxBusyToken) || !strings.Contains(err.Error(), "an update is running") {
		t.Fatalf("err = %v, want the same refusal an install or update gives", err)
	}
	if res.OK || res.Step != "busy" || res.Message != err.Error() {
		t.Errorf("result = %+v, want it refused as busy with the refusal as its message", res)
	}
	if took := time.Since(begin); took > 2*time.Second {
		t.Errorf("the refusal took %v: it must come before anything talks to the speaker", took)
	}
	// The refusal must not take the claim away from the run that holds it.
	if !writesInFlight.isBusy(host) {
		t.Error("the refused repair released the claim of the update that is running")
	}
}

// A repair on one speaker is not held up by work on another, and it gives its
// own speaker back when it ends. Runs on the checked-in empty agent stub, where
// the repair ends before it reaches the network; with a real agent embedded
// (a release build tree) it would go on to an SSH handshake, so it is skipped.
func TestRepairOnAnotherSpeakerIsNotRefusedAndReleases(t *testing.T) {
	if agentbin.Available() {
		t.Skip("a real agent is embedded: the repair would go on to the network")
	}
	const busyHost, host = "192.0.2.65", "192.0.2.66"
	release, err := writesInFlight.claim(busyHost, "an update")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	a := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	res, err := a.RepairInstallViaSSH(host, "SoundTouch 20")
	if err != nil || res.Step == "busy" || strings.Contains(res.Message, errBoxBusyToken) {
		t.Fatalf("result = %+v err = %v, want a repair on a different speaker let through", res, err)
	}
	if res.Code != "no-embedded-agent" {
		t.Errorf("code = %q, want the repair to have run up to its embedded-agent check", res.Code)
	}
	if writesInFlight.isBusy(host) {
		t.Error("the ended repair kept its speaker claimed")
	}
	// The install's own repair step runs under the install's claim and must
	// not ask for a second one, or every install would refuse its own repair.
	helper := funcBody(t, readInstallSrc(t), "func (a *App) repairAndAwaitAgent(")
	if strings.Contains(helper, "a.RepairInstallViaSSH(") || !strings.Contains(helper, "a.repairInstallViaSSH(") {
		t.Error("repairAndAwaitAgent must call the unexported repair: it already holds the speaker's claim")
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
