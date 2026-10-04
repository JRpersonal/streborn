package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func engineTestApp(t *testing.T, results []error) (*App, *int, *[]time.Duration) {
	t.Helper()
	prevEnsure, prevSleep := installEnsureEngine, installEngineSleep
	t.Cleanup(func() { installEnsureEngine, installEngineSleep = prevEnsure, prevSleep })
	calls := 0
	installEnsureEngine = func(*App, string) (string, error) {
		err := results[calls]
		calls++
		if err != nil {
			return "", err
		}
		return "ok", nil
	}
	var slept []time.Duration
	installEngineSleep = func(d time.Duration) { slept = append(slept, d) }
	a := newTestApp()
	a.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return a, &calls, &slept
}

// Fleet run 2026-10-04: the engine was missing after a fresh network install.
// A speaker that is still settling gets retried until the engine lands.
func TestInstallEndsWithTheEngineAfterATransientFailure(t *testing.T) {
	a, calls, slept := engineTestApp(t, []error{errors.New("connection refused"), errors.New("timeout"), nil})
	if why := a.ensureEngineAfterInstall("192.0.2.1"); why != "" {
		t.Fatalf("want the engine delivered, got %q", why)
	}
	if *calls != 3 || len(*slept) != 2 || (*slept)[0] != 5*time.Second || (*slept)[1] != 10*time.Second {
		t.Fatalf("calls=%d slept=%v, want 3 calls with 5 s then 10 s between", *calls, *slept)
	}
}

func TestInstallEngineGivesUpAtOnceWhenItCannotFit(t *testing.T) {
	a, calls, _ := engineTestApp(t, []error{errors.New("sidecar push: 507 insufficient NAND")})
	if why := a.ensureEngineAfterInstall("192.0.2.1"); !strings.Contains(why, "507") {
		t.Fatalf("want the space refusal reported, got %q", why)
	}
	if *calls != 1 {
		t.Fatalf("retried a push that can never fit: %d calls", *calls)
	}
}

func TestInstallEngineReportsAfterTheLastAttempt(t *testing.T) {
	errs := make([]error, installEngineAttempts)
	for i := range errs {
		errs[i] = errors.New("unreachable")
	}
	a, calls, _ := engineTestApp(t, errs)
	if why := a.ensureEngineAfterInstall("192.0.2.1"); why != "unreachable" {
		t.Fatalf("got %q", why)
	}
	if *calls != installEngineAttempts {
		t.Fatalf("calls = %d, want %d", *calls, installEngineAttempts)
	}
}

// Both ways an install can end in success run the engine step: the prompt one
// and the late re-check.
func TestEverySuccessfulInstallEndsWithTheEngineStep(t *testing.T) {
	for _, f := range []string{"install_str.go", "installwait.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "a.ensureEngineAfterInstall(host)") {
			t.Errorf("%s no longer ends a successful install with the engine step", f)
		}
	}
}
