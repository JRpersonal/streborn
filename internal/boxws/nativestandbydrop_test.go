package boxws

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// dropCounter wires the native-dropped callback, which runs in its own
// goroutine, and waits for it rather than sleeping a fixed amount.
func dropCounter(c *Client) func() int64 {
	var n atomic.Int64
	c.SetOnNativeDropped(func() { n.Add(1) })
	return func() int64 {
		for i := 0; i < 100; i++ {
			if n.Load() > 0 {
				return n.Load()
			}
			time.Sleep(5 * time.Millisecond)
		}
		return n.Load()
	}
}

func srcFrame(src string) []byte {
	return []byte(`<updates deviceID="AA"><nowSelectionUpdated><ContentItem source="` + src + `" /></nowSelectionUpdated></updates>`)
}

// A speaker that cannot keep native stations drops them straight to STANDBY on
// some chassis, never touching INVALID_SOURCE. That route was not counted, so
// the speaker never learned it and went silent on every press: reported for a
// newly added ST10 on 2026-08-06, "normal playback does not work either, the
// device switches to standby immediately". Its station lasted 862 ms.
func TestNativeStationDroppedStraightToStandbyCounts(t *testing.T) {
	c := &Client{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), handler: &recHandler{}}
	drops := dropCounter(c)
	ctx := context.Background()

	c.handleMessage(ctx, srcFrame("LOCAL_INTERNET_RADIO"))
	time.Sleep(60 * time.Millisecond) // the station lasted a moment, like the report
	c.handleMessage(ctx, srcFrame("STANDBY"))

	if got := drops(); got == 0 {
		t.Error("a station abandoned to standby was not counted as a native failure")
	}
}

// The same transition is what a user switching the speaker off produces, so a
// station that actually played must NOT be counted. Time is the discriminator:
// the activity frame cannot be trusted for this (it fires from STR's own writes
// and appears with nobody near the box).
func TestAUserPoweringOffALongPlayingStationIsNotAFailure(t *testing.T) {
	c := &Client{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), handler: &recHandler{}}
	drops := dropCounter(c)
	ctx := context.Background()

	c.handleMessage(ctx, srcFrame("LOCAL_INTERNET_RADIO"))
	// Pretend the station ran well past the window before the user switched off.
	c.mu.Lock()
	c.nativeStartedAt = time.Now().Add(-2 * nativeStandbyDropWindow)
	c.mu.Unlock()
	c.handleMessage(ctx, srcFrame("STANDBY"))

	if got := drops(); got != 0 {
		t.Errorf("a user power-off after %v of playback counted as a native failure (%d)", 2*nativeStandbyDropWindow, got)
	}
}

// A standby that did not come from the native source is none of this rule's
// business.
func TestStandbyFromAnotherSourceIsIgnored(t *testing.T) {
	c := &Client{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), handler: &recHandler{}}
	drops := dropCounter(c)
	ctx := context.Background()

	c.handleMessage(ctx, srcFrame("BLUETOOTH"))
	c.handleMessage(ctx, srcFrame("STANDBY"))

	if got := drops(); got != 0 {
		t.Errorf("a bluetooth power-off counted as a native failure (%d)", got)
	}
}
