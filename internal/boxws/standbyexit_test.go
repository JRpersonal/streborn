// Test for the #487 standby-exit heal hook: any source transition OUT of
// STANDBY must fire OnStandbyExit exactly once per transition, so the agent
// re-registers hardware keys the moment the user is back at the box.

package boxws

import (
	"context"
	"sync/atomic"
	"testing"
)

type standbyExitHandler struct {
	recHandler
	exits atomic.Int32
}

func (h *standbyExitHandler) OnStandbyExit(context.Context) { h.exits.Add(1) }

func TestHandleMessage_StandbyExitFiresOnWake(t *testing.T) {
	h := &standbyExitHandler{}
	c := newTestClient(h)
	frame := func(src string) []byte {
		return []byte(`<updates deviceID="x"><nowPlayingUpdated><nowPlaying source="` + src + `">` +
			`<ContentItem source="` + src + `"/></nowPlaying></nowPlayingUpdated></updates>`)
	}
	c.handleMessage(context.Background(), frame("STANDBY"))
	if h.exits.Load() != 0 {
		t.Fatal("entering standby must not fire OnStandbyExit")
	}
	c.handleMessage(context.Background(), frame("UPNP"))
	if h.exits.Load() != 1 {
		t.Fatalf("leaving standby must fire OnStandbyExit once, got %d", h.exits.Load())
	}
	c.handleMessage(context.Background(), frame("AUX"))
	if h.exits.Load() != 1 {
		t.Fatalf("a non-standby source change must not fire again, got %d", h.exits.Load())
	}
}
