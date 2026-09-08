package boxws

// <setupAPUpdated>: the firmware's own warning that it is about to raise its
// setup access point and leave the LAN.
//
// #873, reporter A's log: shape=updates/setupAPUpdated with a body of "true",
// five seconds after the source flipped to SETUP, sitting in the unrecognized
// bucket. The source flip leads it; what this frame adds is that the speaker
// really is raising an access point, which a merely stuck SETUP source (#367)
// never does. The desktop app's install wait was running at that moment.

import (
	"context"
	"testing"
)

// setupAPHandler is the optional-hook half of the test: a handler that
// implements OnSetupAPRaised.
type setupAPHandler struct {
	recHandler
	raised int
}

func (h *setupAPHandler) OnSetupAPRaised(context.Context) { h.raised++ }

func TestSetupAPRaisedFiresTheHook(t *testing.T) {
	h := &setupAPHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(
		`<updates deviceID="AABBCCDDEEFF"><setupAPUpdated>true</setupAPUpdated></updates>`))
	if h.raised != 1 {
		t.Fatalf("OnSetupAPRaised fired %d times, want 1", h.raised)
	}
	// The frame must be RECOGNISED, or it also lands in the unrecognized ring
	// and keeps costing NAND log bytes on every episode.
	if n := len(c.unknownFrames); n != 0 {
		t.Errorf("setupAPUpdated still counted as unrecognized (%d shapes)", n)
	}
}

// The AP going DOWN is the same frame with a different body, and it must not
// fire the "the speaker is leaving the network" hook.
func TestSetupAPLoweredDoesNotFireTheHook(t *testing.T) {
	h := &setupAPHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(
		`<updates deviceID="AABBCCDDEEFF"><setupAPUpdated>false</setupAPUpdated></updates>`))
	if h.raised != 0 {
		t.Fatalf("OnSetupAPRaised fired for an AP going down (%d times)", h.raised)
	}
}

// sourcesUpdated proves the firmware uses both the wrapped and the bare-root
// form for the same event, so the bare one is covered too: missing it would
// mean missing the only warning STR gets.
func TestSetupAPRaisedBareRoot(t *testing.T) {
	h := &setupAPHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(`<setupAPUpdated>true</setupAPUpdated>`))
	if h.raised != 1 {
		t.Fatalf("bare-root setupAPUpdated fired %d times, want 1", h.raised)
	}
}

// The SELF-CLOSING bare root, which is the shape every other bare-root frame
// on this firmware has (<sourcesUpdated/>, <swUpdateStatusUpdated/>,
// <userActivityUpdate/>). A bodyless announcement IS the event; reading it as
// a boolean false would log "the firmware took its setup access point down
// again" for an AP that just went UP, and skip the hook entirely.
func TestSetupAPRaisedSelfClosingBareRoot(t *testing.T) {
	h := &setupAPHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(`<setupAPUpdated/>`))
	if h.raised != 1 {
		t.Fatalf("self-closing bare-root setupAPUpdated fired %d times, want 1", h.raised)
	}
}

// A "down" frame must not spend the budget the "up" frame needs: the firmware
// lowers the AP and raises it again inside the same minute, and the raise is
// the transition that warns STR the speaker is about to leave the network.
func TestSetupAPDownDoesNotStarveTheRaise(t *testing.T) {
	h := &setupAPHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(
		`<updates deviceID="AABBCCDDEEFF"><setupAPUpdated>false</setupAPUpdated></updates>`))
	c.handleMessage(context.Background(), []byte(
		`<updates deviceID="AABBCCDDEEFF"><setupAPUpdated>true</setupAPUpdated></updates>`))
	if h.raised != 1 {
		t.Fatalf("the raise right after a lowered frame fired %d times, want 1", h.raised)
	}
}

// A handler that does not implement the optional hook must still be fine: the
// frame is logged and dropped, exactly like OnSourcePlaying's contract.
func TestSetupAPRaisedWithoutTheOptionalHook(t *testing.T) {
	c := newTestClient(&recHandler{})
	c.handleMessage(context.Background(), []byte(
		`<updates><setupAPUpdated>true</setupAPUpdated></updates>`))
	if n := len(c.unknownFrames); n != 0 {
		t.Errorf("setupAPUpdated counted as unrecognized (%d shapes)", n)
	}
}

// The firmware re-raises setup every couple of minutes for a quarter of an
// hour, and the frame's own cadence on 27.0.6 has never been measured. So a
// burst must cost one log line and one hook call, not one of each per frame:
// the hook wakes the leave-setup watcher, and an unbounded stream of wakes
// would walk straight past the watcher's clear budget.
func TestSetupAPRaisedIsRateLimited(t *testing.T) {
	h := &setupAPHandler{}
	c := newTestClient(h)
	for i := 0; i < 5; i++ {
		c.handleMessage(context.Background(), []byte(
			`<updates deviceID="AABBCCDDEEFF"><setupAPUpdated>true</setupAPUpdated></updates>`))
	}
	if h.raised != 1 {
		t.Fatalf("a burst of five frames fired the hook %d times, want 1", h.raised)
	}
	// Once the gap has passed the next frame is heard again: the limit is a
	// rate limit, not a one-shot latch, or a second episode half an hour later
	// would be invisible.
	c.mu.Lock()
	c.lastSetupAPRaisedAt = c.lastSetupAPRaisedAt.Add(-2 * setupAPLogEvery)
	c.mu.Unlock()
	c.handleMessage(context.Background(), []byte(
		`<updates deviceID="AABBCCDDEEFF"><setupAPUpdated>true</setupAPUpdated></updates>`))
	if h.raised != 2 {
		t.Fatalf("after the gap the hook fired %d times in total, want 2", h.raised)
	}
}
