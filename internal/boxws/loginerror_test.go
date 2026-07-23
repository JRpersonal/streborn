package boxws

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// The box reuses errorUpdate value 1036 for two conditions that can arrive
// separately OR together: UNABLE_TO_PROCESS_NOT_LOGGED_IN (a login loss, must
// trigger the re-registration self-heal) and UpnpRcvdContentItemInWrongState
// (the routine SetURI vs standby-wake race, and the expected teardown when
// /setZone kills an in-flight UPnP session during group forming, #70). Firing
// the self-heal on the PURE wrong-state flavor killed the recall retry and
// forced a pointless re-pair on every wake race, so the name is the decisive
// signal, not the detail.
func TestLoginErrorFiresOnlyForNotLoggedIn1036(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	var fires atomic.Int64
	c.SetOnLoginError(func() { fires.Add(1) })

	// Pure wrong-state flavor first (name is a plain UNABLE_TO_PROCESS): it must
	// not fire, and must not consume the dedup window either.
	c.handleMessage(context.Background(), []byte(
		`<errorUpdate><error value="1036" name="UNABLE_TO_PROCESS" severity="Unknown">`+
			`UpnpRcvdContentItemInWrongState</error></errorUpdate>`))
	time.Sleep(150 * time.Millisecond)
	if got := fires.Load(); got != 0 {
		t.Fatalf("UpnpRcvdContentItemInWrongState must not trigger the login self-heal, fired %d times", got)
	}

	// Genuine not-logged-in flavor must fire.
	c.handleMessage(context.Background(), []byte(
		`<errorUpdate><error value="1036" name="UNABLE_TO_PROCESS_NOT_LOGGED_IN" severity="Unknown">`+
			`source rejected</error></errorUpdate>`))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fires.Load() == 0 {
		time.Sleep(25 * time.Millisecond)
	}
	if got := fires.Load(); got != 1 {
		t.Fatalf("NOT_LOGGED_IN 1036 must trigger the login self-heal exactly once, fired %d times", got)
	}
}

// TestLoginErrorFiresOnCombinedNotLoggedInWrongState is the field case: real
// hardware-preset rejections on the Portable/ST10/ST20/ST30 carry BOTH markers
// at once - name UNABLE_TO_PROCESS_NOT_LOGGED_IN AND detail
// UpnpRcvdContentItemInWrongState (the box could not activate its own stored
// ContentItem precisely because it is not logged in). The name must win: the
// re-registration self-heal MUST fire (the old detail-first check swallowed
// every one of these, so the box was re-pushed the identical SetURI forever and
// only a power pull recovered it). The wrong-state re-point signal fires too.
func TestLoginErrorFiresOnCombinedNotLoggedInWrongState(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	var fires atomic.Int64
	c.SetOnLoginError(func() { fires.Add(1) })

	c.handleMessage(context.Background(), []byte(
		`<errorUpdate><error value="1036" name="UNABLE_TO_PROCESS_NOT_LOGGED_IN" severity="Unrecoverable">`+
			`UpnpRcvdContentItemInWrongState</error></errorUpdate>`))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fires.Load() == 0 {
		time.Sleep(25 * time.Millisecond)
	}
	if got := fires.Load(); got != 1 {
		t.Fatalf("combined NOT_LOGGED_IN + wrong-state 1036 must trigger the re-login self-heal, fired %d times", got)
	}
	// The recall's verify re-point signal must also have fired so the box does
	// not hang attached-but-buffering after the re-login lands.
	if h.sourceRejects == 0 {
		t.Fatalf("combined 1036 must also signal OnSourceRejected for the verify re-point, got %d", h.sourceRejects)
	}
}

// TestLoginErrorDedupWindow: a box that answers EVERY press with the 1036
// NOT_LOGGED_IN frame (the fresh-install steady state seen in the field
// bundles) must trigger at most one re-login per dedup window - the re-assert
// cadence is owned by autopair's own rate limit, not by frame arrival.
func TestLoginErrorDedupWindow(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	var fires atomic.Int64
	c.SetOnLoginError(func() { fires.Add(1) })

	frame := []byte(`<errorUpdate><error value="1036" name="UNABLE_TO_PROCESS_NOT_LOGGED_IN" severity="Unrecoverable">` +
		`UpnpRcvdContentItemInWrongState</error></errorUpdate>`)
	for i := 0; i < 4; i++ {
		c.handleMessage(context.Background(), frame)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fires.Load() == 0 {
		time.Sleep(25 * time.Millisecond)
	}
	// Give a mistaken extra fire a moment to surface before asserting.
	time.Sleep(150 * time.Millisecond)
	if got := fires.Load(); got != 1 {
		t.Fatalf("repeated 1036 frames within the dedup window must fire once, fired %d times", got)
	}
	// The verify re-point signal is per-frame, not deduped: every rejected
	// press needs its re-point.
	if h.sourceRejects != 4 {
		t.Fatalf("every 1036 frame must signal OnSourceRejected, got %d of 4", h.sourceRejects)
	}
}
