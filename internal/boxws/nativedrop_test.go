package boxws

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// A normal preset change also leaves the native source for a moment
// (LOCAL_INTERNET_RADIO -> UPNP -> LOCAL_INTERNET_RADIO within a few hundred
// milliseconds). Counting that as the box abandoning the station would switch
// a perfectly healthy speaker back to the slower preset form, so the signal
// must be reaching INVALID_SOURCE, not merely leaving the native source.
func TestNativeDropOnlyFiresOnInvalidSource(t *testing.T) {
	frame := func(src string) []byte {
		return []byte(`<updates><nowPlayingUpdated><nowPlaying source="` + src +
			`"><ContentItem source="` + src + `"/></nowPlaying></nowPlayingUpdated></updates>`)
	}

	t.Run("normal preset change does not count as a drop", func(t *testing.T) {
		var drops atomic.Int32
		c := newTestClientForDrop(&drops)
		ctx := context.Background()
		c.handleMessage(ctx, frame("LOCAL_INTERNET_RADIO"))
		c.handleMessage(ctx, frame("UPNP"))
		c.handleMessage(ctx, frame("LOCAL_INTERNET_RADIO"))
		if n := settle(&drops); n != 0 {
			t.Fatalf("a preset change counted %d drops, want 0", n)
		}
	})

	t.Run("box abandoning the station counts, via UPNP", func(t *testing.T) {
		var drops atomic.Int32
		c := newTestClientForDrop(&drops)
		ctx := context.Background()
		c.handleMessage(ctx, frame("LOCAL_INTERNET_RADIO"))
		c.handleMessage(ctx, frame("UPNP"))
		c.handleMessage(ctx, frame("INVALID_SOURCE"))
		if n := settle(&drops); n != 1 {
			t.Fatalf("the observed ST20 failure counted %d drops, want 1", n)
		}
	})

	t.Run("an unrelated invalid source does not count", func(t *testing.T) {
		var drops atomic.Int32
		c := newTestClientForDrop(&drops)
		ctx := context.Background()
		c.handleMessage(ctx, frame("SPOTIFY"))
		c.handleMessage(ctx, frame("INVALID_SOURCE"))
		if n := settle(&drops); n != 0 {
			t.Fatalf("an INVALID_SOURCE with no native station before it counted %d drops, want 0", n)
		}
	})
}

// newTestClientForDrop wires the counter. The callback runs in its own
// goroutine (the read loop must never block on it), so the helper below waits
// for it instead of reading the counter straight after the frame.
func newTestClientForDrop(drops *atomic.Int32) *Client {
	c := New(slog.New(slog.NewTextHandler(io.Discard, nil)), "ws://test", nopHandlerForDrop{})
	c.SetOnNativeDropped(func() { drops.Add(1) })
	return c
}

// settle waits briefly for any fired callback goroutine to land, then returns
// the count. A "want 0" case still waits, so it cannot pass merely by being
// read too early.
func settle(drops *atomic.Int32) int32 {
	for i := 0; i < 100; i++ {
		if drops.Load() > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
	return drops.Load()
}

type nopHandlerForDrop struct{}

func (nopHandlerForDrop) OnPresetSelected(context.Context, int, string, string) {}
func (nopHandlerForDrop) OnRemoteSkip(context.Context, bool)                    {}
func (nopHandlerForDrop) OnUserStop(context.Context)                            {}
func (nopHandlerForDrop) OnThumbActivity(context.Context)                       {}
func (nopHandlerForDrop) OnPowerKey(context.Context)                            {}
func (nopHandlerForDrop) OnSourceAux(context.Context)                           {}
func (nopHandlerForDrop) OnZoneChanged(context.Context, ZoneState)              {}
func (nopHandlerForDrop) OnGroupChanged(context.Context, GroupState)            {}
func (nopHandlerForDrop) OnPowerWake(context.Context)                           {}
func (nopHandlerForDrop) OnPresetsChanged(context.Context, []BoxPreset)         {}
