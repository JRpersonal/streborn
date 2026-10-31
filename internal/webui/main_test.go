package webui

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestMain keeps the background length read of prearmNextLength off the
// network: every queue push starts it, and most queue tests use placeholder
// media server addresses that must never be dialled. A test that exercises the
// read sets its own Server.prearmProbeFn.
//
// The same goes for the length check of a library key press (keyRecallLength):
// a nil client skips it, and a test that exercises it sets its own
// Server.keyLengthClientFn.
func TestMain(m *testing.M) {
	defaultPrearmProbe = func(context.Context, string) (time.Duration, bool) { return 0, false }
	defaultKeyLengthClient = func() *http.Client { return nil }
	os.Exit(m.Run())
}
