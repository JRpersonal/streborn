package boxws

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// A remote skip key is a concrete event. The firmware sends a generic
// userActivityUpdate alongside it, and reading THAT as a thumb press fired the
// webhook on every skip: "my ST20s do skip through songs using the remote, but
// doing so also triggers the webhook" (#536, 2026-08-06).
func TestRemoteSkipDoesNotAlsoFireTheThumbWebhook(t *testing.T) {
	for _, frame := range []string{
		`<updates deviceID="AA"><errorUpdate><error name="QPLAY_SKIP_NEXT_FAILED"/></errorUpdate></updates>`,
		`<updates deviceID="AA"><errorUpdate><error name="QPLAY_SKIP_PREV_FAILED"/></errorUpdate></updates>`,
	} {
		h := &recHandler{}
		c := &Client{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), handler: h}
		ctx := context.Background()

		c.handleMessage(ctx, []byte(frame))
		// The activity ping the firmware sends with the key press.
		c.handleMessage(ctx, []byte(`<userActivityUpdate deviceID="AA" />`))
		// Long enough for the thumb settle timer to have fired if it was armed.
		time.Sleep(thumbSettle + 250*time.Millisecond)

		h.mu.Lock()
		thumbs, skips := h.thumbs, len(h.skips)
		h.mu.Unlock()
		if thumbs != 0 {
			t.Errorf("%s: thumb webhook fired %d time(s) for a skip key", frame[:60], thumbs)
		}
		if skips == 0 {
			t.Errorf("%s: the skip itself was lost", frame[:60])
		}
	}
}

// A lone activity frame is still a thumb press: the fix must not silence the
// feature it protects.
func TestALoneActivityFrameStillFiresTheThumbWebhook(t *testing.T) {
	h := &recHandler{}
	c := &Client{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), handler: h}
	c.handleMessage(context.Background(), []byte(`<userActivityUpdate deviceID="AA" />`))
	time.Sleep(thumbSettle + 250*time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.thumbs != 1 {
		t.Errorf("thumb webhook fired %d times for a lone activity frame, want 1", h.thumbs)
	}
}
