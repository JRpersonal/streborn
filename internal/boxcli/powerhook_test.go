package boxcli

// The 1036 storm counter stands down for a beat around every `sys power`
// toggle, because the firmware answers its own power-on self-resume with a
// 1036 about 60 ms later (field bundle 2026-09-07: toggle logged at
// 14:15:16.483, rejection at 14:15:16.549). The stand-down is armed through
// this hook, so what has to hold is that the hook runs on every toggle, runs
// before the command is written, and that an agent without it behaves as
// before.

import (
	"context"
	"testing"
	"time"
)

func TestPowerOnFiresTheHookOncePerToggle(t *testing.T) {
	t.Cleanup(func() { SetBeforePowerToggle(nil) })
	calls := 0
	SetBeforePowerToggle(func() { calls++ })

	// The dial fails (nothing serves :17000 here) and that is deliberate: the
	// window must be armed before the toggle is sent, not after it succeeds,
	// or a box that answers in 60 ms rejects inside an unarmed window.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = PowerOn(ctx, "127.0.0.1")
	_ = PowerOn(ctx, "127.0.0.1")

	if calls != 2 {
		t.Fatalf("the hook ran %d times for two toggles, want 2", calls)
	}
}

func TestPowerOnWithoutAHookIsUnchanged(t *testing.T) {
	SetBeforePowerToggle(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// No panic, no nil deref: the wiring is optional, and only the agent sets
	// it. Every other binary and test in the tree calls PowerOn without one.
	_ = PowerOn(ctx, "127.0.0.1")
}
