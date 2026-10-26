package webui

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestMain keeps the background length read of prearmNextLength off the
// network: every queue push starts it, and most queue tests use placeholder
// media server addresses that must never be dialled. A test that exercises the
// read sets its own Server.prearmProbeFn.
func TestMain(m *testing.M) {
	defaultPrearmProbe = func(context.Context, string) (time.Duration, bool) { return 0, false }
	os.Exit(m.Run())
}
