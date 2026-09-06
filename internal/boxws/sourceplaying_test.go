// The default-group trigger fires on the edge into real playback, not on a
// bare source change. A rebooted speaker flips STANDBY -> LOCAL_INTERNET_RADIO
// in STOP_STATE while its presets are re-registered; treating that as "started
// music" woke every member of a permanent group at 03:28 after a fleet update
// (2026-09-06).

package boxws

import (
	"context"
	"sync/atomic"
	"testing"
)

type sourcePlayingHandler struct {
	recHandler
	starts atomic.Int32
}

func (h *sourcePlayingHandler) OnSourcePlaying(context.Context, string) { h.starts.Add(1) }

func TestHandleMessage_SourcePlayingFiresOnPlayEdgeNotOnSourceFlip(t *testing.T) {
	h := &sourcePlayingHandler{}
	c := newTestClient(h)
	frame := func(src, status string) []byte {
		return []byte(`<updates deviceID="x"><nowPlayingUpdated><nowPlaying source="` + src + `">` +
			`<ContentItem source="` + src + `"/><playStatus>` + status + `</playStatus></nowPlaying></nowPlayingUpdated></updates>`)
	}
	ctx := context.Background()
	c.handleMessage(ctx, frame("STANDBY", "STOP_STATE"))
	c.handleMessage(ctx, frame("LOCAL_INTERNET_RADIO", "STOP_STATE"))
	if h.starts.Load() != 0 {
		t.Fatalf("a source flip in STOP_STATE must not count as a start, got %d", h.starts.Load())
	}
	c.handleMessage(ctx, frame("LOCAL_INTERNET_RADIO", "BUFFERING_STATE"))
	c.handleMessage(ctx, frame("LOCAL_INTERNET_RADIO", "PLAY_STATE"))
	if h.starts.Load() != 1 {
		t.Fatalf("the edge into playback must fire exactly once, got %d", h.starts.Load())
	}
	c.handleMessage(ctx, frame("LOCAL_INTERNET_RADIO", "STOP_STATE"))
	c.handleMessage(ctx, frame("SPOTIFY", "PLAY_STATE"))
	if h.starts.Load() != 2 {
		t.Fatalf("a new start after a stop must fire again, got %d", h.starts.Load())
	}
	c.handleMessage(ctx, frame("AUX", "PLAY_STATE"))
	if h.starts.Load() != 2 {
		t.Fatalf("a non-streaming source must not fire, got %d", h.starts.Load())
	}
}
