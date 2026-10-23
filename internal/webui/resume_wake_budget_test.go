// The wake resume decides inside a TIME budget, and this file pins it.
//
// The #197 power-off bounce guard is a 6 s window measured from the moment STR
// saw the box drop UPNP -> STANDBY (standbyBounceWakeWindow). ResumeLastPlay was
// written to spend 2 s of that budget on its settle sleep, which is what the
// window's own comment sizes it against. Anything else the goroutine does BEFORE
// that guard spends the same budget, and a box read is the one kind of work that
// can spend seconds of it without looking like it does.

package webui

import (
	"context"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// A box that is slow to answer :8090 at the wake instant must not cost the
// power-off bounce guard its window. The read that the foreign-content guard
// needs is taken before the settle sleep, and a box in the middle of a power
// transition is exactly the box that answers late: at the production readBox
// timeout the guard is already past 6 s when it is finally consulted, so STR
// re-wakes a speaker the user had just switched off (#197 deqw, and the ST10
// shape Svagerka reported as "off does not stick").
func TestASlowBoxReadDoesNotSpendThePowerOffBounceWindow(t *testing.T) {
	s, rec := resumeTestServer(t, `<nowPlaying source="UPNP"/>`)
	// This test is about the settle's share of the 6 s bounce budget, so it
	// runs with the production settle, not the short one resumeTestServer
	// installs for the decision tests (its cleanup restores the value anyway).
	resumeWakeSettle = 2 * time.Second
	// As slow as the production read can be: readBox's own HTTP timeout.
	s.boxNowReadFn = func(context.Context) (*boxapi.NowPlaying, error) {
		time.Sleep(4 * time.Second)
		return boxapi.ParseNowPlaying([]byte(`<nowPlaying source="UPNP"/>`)), nil
	}
	// The power-off STR saw, and the bounce restore that follows it ~200 ms
	// later. noteStandbyStop arms both stamps, so both are set here.
	s.standbyStopMu.Lock()
	s.lastStandbyStop = time.Now().Add(-200 * time.Millisecond)
	s.standbyStopMu.Unlock()
	s.lastUserStopMu.Lock()
	s.lastUserStop = time.Now().Add(-200 * time.Millisecond)
	s.lastUserStopMu.Unlock()

	s.ResumeLastPlay()
	// The regression this pins pushed at read (4 s) + settle (2 s) + a moment,
	// so a push, if it comes, is here within 8 s; the correct resume has
	// declined after the 2 s settle.
	if waitForSOAP(rec, 8*time.Second) {
		t.Fatalf("the bounce guard timed out and STR re-woke a box the user had just switched off: %v", rec.list())
	}
}
