package main

import (
	"errors"
	"fmt"
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
	installEnsureEngine = func(*App, string) (engineResult, error) {
		err := results[calls]
		calls++
		if err != nil {
			return engineNone, err
		}
		return engineInstalled, nil
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
	// What EnsureSpotifyEngine returns when the agent answers 507: the typed
	// verdict, wrapped the way pushSidecarIfNeeded wraps it.
	a, calls, _ := engineTestApp(t, []error{fmt.Errorf("sidecar upload failed: %w", &nandFullError{status: 507, detail: "insufficient NAND space"})})
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

// engineTestAppReplies is engineTestApp for the answers that are not errors.
func engineTestAppReplies(t *testing.T, replies []engineReply) (*App, *int) {
	t.Helper()
	prevEnsure, prevSleep := installEnsureEngine, installEngineSleep
	t.Cleanup(func() { installEnsureEngine, installEngineSleep = prevEnsure, prevSleep })
	calls := 0
	installEnsureEngine = func(*App, string) (engineResult, error) {
		r := replies[calls]
		calls++
		return r.res, r.err
	}
	installEngineSleep = func(time.Duration) {}
	a := newTestApp()
	a.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return a, &calls
}

// The engine step answers without an error in cases where no engine is
// running on the speaker. The install took every one of them as "verified"
// and told the user nothing.
func TestInstallEngineOnlyCountsAnEngineThatIsInPlace(t *testing.T) {
	deferred := make([]engineReply, installEngineAttempts)
	for i := range deferred {
		deferred[i] = engineReply{res: engineDeferred}
	}
	cases := []struct {
		name      string
		replies   []engineReply
		wantWhy   string // a part of the reason; empty: the install says nothing
		wantCalls int
	}{
		{"already current", []engineReply{{res: engineCurrent}}, "", 1},
		{"sent and active", []engineReply{{res: engineInstalled}}, "", 1},
		{"this build has no engine to give", []engineReply{{res: engineNotEmbedded}}, "", 1},
		{"STR is being removed again", []engineReply{{res: engineSkipped}}, "", 1},
		// Refused by the speaker's update latch: nothing was sent. Asked again,
		// and said when it never clears.
		{"refused, then sent", []engineReply{{res: engineDeferred}, {res: engineInstalled}}, "", 2},
		{"refused every time", deferred, "update is in flight", installEngineAttempts},
		// On the disk, not running. Asking again would only answer "current"
		// for the file that is already there.
		{"sent but the activating restart failed", []engineReply{{res: engineStaged}, {res: engineCurrent}}, "restart", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, calls := engineTestAppReplies(t, c.replies)
			why := a.ensureEngineAfterInstall("192.0.2.1")
			if (c.wantWhy == "") != (why == "") || !strings.Contains(why, c.wantWhy) {
				t.Errorf("reason = %q, want one containing %q", why, c.wantWhy)
			}
			if *calls != c.wantCalls {
				t.Errorf("calls = %d, want %d", *calls, c.wantCalls)
			}
		})
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
