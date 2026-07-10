package boxws

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// The box reuses errorUpdate value 1036 for two unrelated conditions:
// UNABLE_TO_PROCESS_NOT_LOGGED_IN (a real login loss, must trigger the
// self-heal) and UpnpRcvdContentItemInWrongState (the routine SetURI vs
// standby-wake race, and the expected teardown when /setZone kills an
// in-flight UPnP session during group forming, #70). Firing the self-heal on
// the second flavor killed the recall retry and forced a pointless re-pair on
// every wake race.
func TestLoginErrorFiresOnlyForNotLoggedIn1036(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	var fires atomic.Int64
	c.SetOnLoginError(func() { fires.Add(1) })

	// Wrong-state flavor first (it must not fire, and must not consume the
	// dedup window either).
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
