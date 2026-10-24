package oled

import (
	"log/slog"
	"testing"
	"time"
)

// The flip thread alone is not enough: on the ST30 it exists seconds before
// the display controller has finished booting, and a hold in that window
// left the firmware unable to paint the panel until the next reboot.
func TestSplashWaitsForTheDisplayControllerOrAnOldBoseApp(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		flip, ctrl bool
		age        time.Duration
		want       bool
	}{
		{"nothing up", false, false, 0, false},
		{"flip thread only, fresh BoseApp", true, false, 4 * time.Second, false},
		{"controller seen but no flip thread", false, true, 0, false},
		{"flip thread and controller past boot", true, true, 4 * time.Second, true},
		{"flip thread and BoseApp up for minutes", true, false, boseAppSettledAge, true},
		{"flip thread, BoseApp just short of settled", true, false, boseAppSettledAge - time.Second, false},
	}
	for _, c := range cases {
		if got := splashMayStart(c.flip, c.ctrl, c.age); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Every panel hold goes through panelGate: the same decision as the splash,
// evaluated lazily so the cheap checks spare the /proc walks.
func TestPanelGateMatchesTheSplashDecision(t *testing.T) {
	t.Parallel()
	for _, flip := range []bool{false, true} {
		for _, ctrl := range []bool{false, true} {
			for _, age := range []time.Duration{0, 4 * time.Second, boseAppSettledAge - time.Second, boseAppSettledAge, 10 * time.Minute} {
				got := panelGate(func() bool { return flip }, func() bool { return ctrl }, func() time.Duration { return age })
				if want := splashMayStart(flip, ctrl, age); got != want {
					t.Errorf("flip=%v ctrl=%v age=%v: got %v, want %v", flip, ctrl, age, got, want)
				}
			}
		}
	}
}

func TestPanelGateSkipsTheExpensiveChecks(t *testing.T) {
	t.Parallel()
	var ctrlCalls, ageCalls int
	ctrl := func(v bool) func() bool { return func() bool { ctrlCalls++; return v } }
	age := func() time.Duration { ageCalls++; return 0 }
	if panelGate(func() bool { return false }, ctrl(true), age) {
		t.Fatal("no flip thread must refuse the hold")
	}
	if ctrlCalls != 0 || ageCalls != 0 {
		t.Fatalf("no flip thread: controller asked %d times, age read %d times, want 0", ctrlCalls, ageCalls)
	}
	if !panelGate(func() bool { return true }, ctrl(true), age) {
		t.Fatal("flip thread and controller up must allow the hold")
	}
	if ageCalls != 0 {
		t.Fatalf("controller up: age read %d times, want 0", ageCalls)
	}
	if panelGate(func() bool { return true }, ctrl(false), age) || ageCalls != 1 {
		t.Fatalf("controller not up, fresh BoseApp: want refused after one age read, got %d reads", ageCalls)
	}
}

// SetDisplayUp is the one wire every panel hold reads; ShowAtStart with a
// nil check keeps it, with a check replaces it. Not parallel: package state.
func TestSetDisplayUpWiresTheControllerCheck(t *testing.T) {
	t.Cleanup(func() { SetDisplayUp(nil) })
	SetDisplayUp(nil)
	if displayUp() {
		t.Fatal("unwired: the controller must count as not up")
	}
	SetDisplayUp(func() bool { return true })
	if !displayUp() {
		t.Fatal("wired true: displayUp must report it")
	}
	ShowAtStart(KindNone, slog.New(slog.DiscardHandler), nil)
	if !displayUp() {
		t.Fatal("ShowAtStart with a nil check must keep the wired one")
	}
	ShowAtStart(KindNone, slog.New(slog.DiscardHandler), func() bool { return false })
	if displayUp() {
		t.Fatal("ShowAtStart with a check must wire it")
	}
}

func TestHoldAttrsCarryKindControllerAndAge(t *testing.T) {
	t.Cleanup(func() { SetDisplayUp(nil) })
	SetDisplayUp(func() bool { return true })
	a := holdAttrs("notice")
	if len(a) != 6 || a[0] != "holdKind" || a[1] != "notice" || a[2] != "controllerUp" || a[3] != true || a[4] != "boseAppAge" {
		t.Fatalf("holdAttrs = %v", a)
	}
	if _, ok := a[5].(time.Duration); !ok {
		t.Fatalf("boseAppAge is %T, want time.Duration", a[5])
	}
}
