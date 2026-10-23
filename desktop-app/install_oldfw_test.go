package main

import (
	"strings"
	"testing"
	"time"
)

// Field case 2026-10-22: a Wave SoundTouch on Bose firmware 2.1.51 went
// through seven network installs in 26 minutes. Each one ran the stick-free
// :17000 unlock, restarted the speaker, restarted it again to put its stock
// settings back, and then told the owner to boot it with the STR stick, which
// a Wave never reads.

func TestUnlockNeedsNewerFirmware(t *testing.T) {
	for v, want := range map[string]bool{
		"2.1.51":  true,  // the Wave of 2026-10-22
		"10.0.11": true,  // the ST30 of 2026-08-04 and the CineMate of 2026-08-25
		"14.0.15": true,  // the 2015/2016 scm ST20 of #854
		"15.0.0":  false, // no evidence either way from 15 to 26: still tried
		"26.9.9":  false,
		"27.0.6":  false,
		"":        false, // unparsable: no evidence, no block
		"x.y.z":   false,
		"0.1.2":   false,
	} {
		if got := unlockNeedsNewerFirmware(v); got != want {
			t.Errorf("unlockNeedsNewerFirmware(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestNeverReadsStickAtBoot(t *testing.T) {
	cases := []struct {
		model, variant string
		want           bool
	}{
		{"Wave SoundTouch", "", true}, // 2.1.51 reports no variant
		{"", "lisa", true},
		{"SoundTouch 300", "", true},
		{"", "ginger", true},
		{"SoundTouch SA-4", "", true},
		{"SoundTouch SA-5", "burns", true},
		{"CineMate 520", "", true},
		{"Cinemate", "", true},
		{"Lifestyle", "bardeen", true},
		{"SoundTouch Wireless Link Adapter", "binky", true},
		{"SoundTouch 10", "rhino", false},
		{"SoundTouch 20", "spotty", false},
		{"SoundTouch 30", "mojo", false},
		{"SoundTouch Portable", "taigan", false},
		{"", "", false}, // unknown keeps the stick advice it always had
	}
	for _, c := range cases {
		if got := neverReadsStickAtBoot(c.model, c.variant); got != c.want {
			t.Errorf("neverReadsStickAtBoot(%q, %q) = %v, want %v", c.model, c.variant, got, c.want)
		}
	}
}

func TestNoStickAdviceNeverNamesTheStick(t *testing.T) {
	for _, a := range []string{
		installWindowClosedNoStickAdvice,
		controlUnresponsiveNoStickAdvice,
		restartingAfterUnlockNoStickAdvice,
		alreadyInstalledNoStickAdvice,
		firmwareFirstAdvice,
	} {
		l := strings.ToLower(a)
		if strings.Contains(l, "str stick") || strings.Contains(l, "insert") || strings.Contains(l, "plug the") {
			t.Errorf("no-stick advice still tells the owner to use a stick: %q", a)
		}
	}
	if stickAdviceFor(true, installWindowClosedAdvice, installWindowClosedNoStickAdvice) != installWindowClosedNoStickAdvice {
		t.Error("stickAdviceFor(true, ...) must pick the no-stick advice")
	}
	if stickAdviceFor(false, installWindowClosedAdvice, installWindowClosedNoStickAdvice) != installWindowClosedAdvice {
		t.Error("stickAdviceFor(false, ...) must keep the stick advice")
	}
}

func TestRecentUnlockReboot(t *testing.T) {
	host := "192.0.2.77"
	unlockReboots.Delete(host)
	if _, ok := recentUnlockReboot(host); ok {
		t.Fatal("no reboot recorded, but recentUnlockReboot says there was one")
	}
	markUnlockReboot(host)
	if _, ok := recentUnlockReboot(host); !ok {
		t.Fatal("a reboot just recorded is not reported as recent")
	}
	unlockReboots.Store(host, time.Now().Add(-unlockRebootGrace-time.Second))
	if _, ok := recentUnlockReboot(host); ok {
		t.Fatal("a reboot older than the grace is still reported as recent")
	}
	unlockReboots.Delete(host)
}

// The order inside the preflight is the fix, so it is asserted on the source:
// every branch needs a live speaker in a specific state to exercise.
func TestPreflightStopsOldFirmwareBeforeTheUnlock(t *testing.T) {
	code := readInstallSrc(t)
	start := strings.Index(code, `res.Step = "preflight"`)
	end := strings.Index(code, `res.Step = "ssh-handshake"`)
	if start < 0 || end <= start {
		t.Fatal("could not locate the preflight block")
	}
	block := code[start:end]
	idx := func(s string) int {
		i := strings.Index(block, s)
		if i < 0 {
			t.Fatalf("preflight block lost %q", s)
		}
		return i
	}
	oldFW := idx("if fwTooOldForUnlock {")
	recent := idx("recentUnlockReboot(host)")
	unlock := idx("a.enableSSHViaTelnet(host, model)")
	restore := idx("a.restoreStockBoseURLsAndReboot(host)")
	if oldFW > unlock || oldFW > restore {
		t.Error("the old-firmware stop must come before the unlock and before any restart")
	}
	if recent > unlock {
		t.Error("the recent-restart check must come before the unlock, or a second press restarts the speaker again")
	}
	if !strings.Contains(block, `res.Code = "firmware-too-old"`) {
		t.Error("the old-firmware stop must carry the firmware-too-old code the setup screen keys on")
	}
	// Our own restart is classified before :8090 is measured: the ports keep
	// answering for a few seconds after the restart command.
	if idx("if rebootedByUs {") > idx("onLAN := tcpReachable(host, 8090") {
		t.Error("rebootedByUs must be classified before the :8090 probe")
	}
	// The log must not claim an unlock ran when :17000 was closed.
	if strings.Contains(block, "stick-free :17000 unlock did not open SSH)") {
		t.Error("install-window-closed log still claims the unlock ran unconditionally")
	}
}
