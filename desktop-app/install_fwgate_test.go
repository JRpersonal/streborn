package main

// The setup wizard's firmware gate for the USB stick (install_fwgate.go).
// Support case 2026-11: the preflight said firmware-too-old for a SoundTouch
// 20 on 9.0.41, and the wizard wrote its install files to the stick anyway.

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func gateEnv(t *testing.T, read func(host string) (FirmwareInfo, error)) (*App, *time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 8, 15, 18, 28, 0, time.UTC)
	a := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	seams := testInstallSeams(a)
	seams.readFirmware = read
	seams.now = func() time.Time { return now }
	fwTooOldVerdicts.mu.Lock()
	fwTooOldVerdicts.m = map[string]fwTooOldVerdict{}
	fwTooOldVerdicts.mu.Unlock()
	t.Cleanup(func() {
		fwTooOldVerdicts.mu.Lock()
		fwTooOldVerdicts.m = map[string]fwTooOldVerdict{}
		fwTooOldVerdicts.mu.Unlock()
	})
	return a, &now
}

var errGone = errors.New("no answer")

func TestStickGateBlocksOldFirmwareReadNow(t *testing.T) {
	a, _ := gateEnv(t, func(string) (FirmwareInfo, error) {
		return FirmwareInfo{Reachable: true, Model: "SoundTouch 20", ModuleType: "scm", Short: "9.0.41", Firmware: "9.0.41.12345", Outdated: true}, nil
	})
	g := a.StickInstallFirmwareGate("192.0.2.1")
	if !g.Blocked || g.Firmware != "9.0.41" || g.Model != "SoundTouch 20" || g.ModuleType != "scm" || g.Source != "speaker" {
		t.Fatalf("a speaker on 9.0.41 needs the firmware update first: %+v", g)
	}
}

// The case itself: the install stopped at firmware-too-old, and the speaker
// did not answer when the stick was prepared minutes later.
func TestStickGateKeepsTheInstallVerdict(t *testing.T) {
	a, now := gateEnv(t, func(string) (FirmwareInfo, error) { return FirmwareInfo{}, errGone })
	a.rememberFirmwareTooOld("192.0.2.1", fwTooOldVerdict{firmware: "9.0.41", model: "SoundTouch 20", moduleType: "scm"})
	*now = now.Add(148 * time.Second) // 15:18:28 -> 15:20:56
	g := a.StickInstallFirmwareGate("192.0.2.1")
	if !g.Blocked || g.Firmware != "9.0.41" || g.Source != "install" {
		t.Fatalf("the install's verdict must stop the stick: %+v", g)
	}
	if g := a.StickInstallFirmwareGate("192.0.2.2"); g.Blocked {
		t.Fatalf("another speaker is not held back: %+v", g)
	}
	*now = now.Add(fwTooOldVerdictTTL + time.Minute)
	if g := a.StickInstallFirmwareGate("192.0.2.1"); g.Blocked {
		t.Fatalf("an old verdict no longer stands in for a silent speaker: %+v", g)
	}
}

// A speaker brought up to date (by the ladder or otherwise) is let through,
// and the old verdict is gone with it.
func TestStickGateClearsOnNewerFirmware(t *testing.T) {
	answer := FirmwareInfo{Reachable: true, Model: "SoundTouch 20", Short: "16.0.9", Outdated: true}
	reachable := true
	a, _ := gateEnv(t, func(string) (FirmwareInfo, error) {
		if !reachable {
			return FirmwareInfo{}, errGone
		}
		return answer, nil
	})
	a.rememberFirmwareTooOld("192.0.2.1", fwTooOldVerdict{firmware: "9.0.41"})
	if g := a.StickInstallFirmwareGate("192.0.2.1"); g.Blocked {
		t.Fatalf("16.0.9 is new enough for the install: %+v", g)
	}
	reachable = false
	if g := a.StickInstallFirmwareGate("192.0.2.1"); g.Blocked {
		t.Fatalf("the verdict must be gone once the speaker said it was updated: %+v", g)
	}
}

func TestStickGateNoHostNoAnswerNoVerdict(t *testing.T) {
	a, _ := gateEnv(t, func(string) (FirmwareInfo, error) { return FirmwareInfo{}, errGone })
	for _, h := range []string{"", "  ", "192.0.2.1"} {
		if g := a.StickInstallFirmwareGate(h); g.Blocked {
			t.Errorf("host %q: nothing says the firmware is too old: %+v", h, g)
		}
	}
	// A booting speaker names no firmware: no evidence, no block.
	a2, _ := gateEnv(t, func(string) (FirmwareInfo, error) { return FirmwareInfo{Reachable: true}, nil })
	if g := a2.StickInstallFirmwareGate("192.0.2.1"); g.Blocked {
		t.Errorf("no firmware named, no block: %+v", g)
	}
}

// The preflight records its verdict where it stops, and a later read of a
// new enough firmware clears it. Asserted on the source, like the order of
// the preflight itself: every branch needs a live speaker.
func TestPreflightRecordsTheFirmwareVerdict(t *testing.T) {
	code := readInstallSrc(t)
	start := strings.Index(code, `res.Code = "firmware-too-old"`)
	if start < 0 {
		t.Fatal("could not locate the firmware-too-old stop")
	}
	stop := code[start:]
	if end := strings.Index(stop, "return true"); end < 0 || !strings.Contains(stop[:end], "rememberFirmwareTooOld(host,") {
		t.Error("the firmware-too-old stop must record its verdict for the stick gate before it returns")
	}
	if !strings.Contains(code, "fwTooOldForUnlock = firmwareTooOldForInstall(fw)") {
		t.Error("the preflight and the stick gate must share one rule")
	}
	if !strings.Contains(code, "forgetFirmwareTooOld(host)") {
		t.Error("a read of a new enough firmware must clear the verdict")
	}
}
