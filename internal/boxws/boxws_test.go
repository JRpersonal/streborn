package boxws

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

// recHandler records which gabbo events the parser dispatched so tests can
// assert that marker words in user-supplied text no longer mis-fire.
type recHandler struct {
	presets    []int
	userStops  int
	wakeResume int
	powerKeys  int
	sourceAux  int
	skips      []bool
}

func (h *recHandler) OnPresetSelected(_ context.Context, slot int, _ string, _ string) {
	h.presets = append(h.presets, slot)
}
func (h *recHandler) OnRemoteSkip(_ context.Context, forward bool) { h.skips = append(h.skips, forward) }
func (h *recHandler) OnUserStop(context.Context)                   { h.userStops++ }
func (h *recHandler) OnThumbActivity(context.Context)              {}
func (h *recHandler) OnWakeResume(context.Context)                 { h.wakeResume++ }
func (h *recHandler) OnPowerKey(context.Context)                   { h.powerKeys++ }
func (h *recHandler) OnSourceAux(context.Context)                  { h.sourceAux++ }

func newTestClient(h Handler) *Client {
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)), "ws://127.0.0.1:8080/", h)
}

func TestHandleMessage_StopStateInTitleDoesNotFireUserStop(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	// A station whose name literally contains STOP_STATE, but playback is
	// actively PLAY_STATE. The old whole-frame match fired OnUserStop here.
	frame := `<updates deviceID="x"><nowPlayingUpdated><nowPlaying source="UPNP">` +
		`<ContentItem source="UPNP" location="http://a"><itemName>STOP_STATE FM</itemName></ContentItem>` +
		`<playStatus>PLAY_STATE</playStatus></nowPlaying></nowPlayingUpdated></updates>`
	c.handleMessage(context.Background(), []byte(frame))
	if h.userStops != 0 {
		t.Fatalf("STOP_STATE in title must not fire OnUserStop, got %d", h.userStops)
	}
}

func TestHandleMessage_RealStopStateFiresUserStop(t *testing.T) {
	for _, frame := range []string{
		`<updates><nowPlayingUpdated><nowPlaying source="UPNP"><playStatus>STOP_STATE</playStatus></nowPlaying></nowPlayingUpdated></updates>`,
		`<updates><nowPlayingUpdated><nowPlaying source="UPNP" playStatus="STOP_STATE"/></nowPlayingUpdated></updates>`,
	} {
		h := &recHandler{}
		c := newTestClient(h)
		c.handleMessage(context.Background(), []byte(frame))
		if h.userStops != 1 {
			t.Fatalf("real STOP_STATE must fire OnUserStop once, got %d for %q", h.userStops, frame)
		}
	}
}

func TestHandleMessage_PresetRecall(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	frame := `<updates><nowSelectionUpdated><preset id="3">` +
		`<ContentItem source="UPNP" location="http://x"><itemName>NDR Info</itemName></ContentItem>` +
		`</preset></nowSelectionUpdated></updates>`
	c.handleMessage(context.Background(), []byte(frame))
	if len(h.presets) != 1 || h.presets[0] != 3 {
		t.Fatalf("expected preset slot 3, got %v", h.presets)
	}
}

func TestHandleMessage_WakeResumeOnInvalidSourceDoNotResume(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	frame := `<updates><nowSelectionUpdated><preset id="0">` +
		`<ContentItem source="INVALID_SOURCE" type="DO_NOT_RESUME" location="http://x">` +
		`<itemName>x</itemName></ContentItem></preset></nowSelectionUpdated></updates>`
	c.handleMessage(context.Background(), []byte(frame))
	if h.wakeResume != 1 {
		t.Fatalf("expected one wake-resume, got %d", h.wakeResume)
	}
}

func TestHandleMessage_FrameTypeWordInTitleNotMisclassified(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	// A nowPlaying frame whose title contains the literal "volumeUpdated".
	// Typed dispatch keys on the element, not the text, so this is handled as
	// nowPlaying (no user-stop, no crash) rather than a volume event.
	frame := `<updates><nowPlayingUpdated><nowPlaying source="UPNP">` +
		`<ContentItem source="UPNP" location="http://a"><itemName>volumeUpdated Live</itemName></ContentItem>` +
		`<playStatus>PLAY_STATE</playStatus></nowPlaying></nowPlayingUpdated></updates>`
	c.handleMessage(context.Background(), []byte(frame))
	if h.userStops != 0 || len(h.presets) != 0 {
		t.Fatalf("unexpected dispatch: stops=%d presets=%v", h.userStops, h.presets)
	}
}

func TestHandleMessage_QplaySkip(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(`<updates><errorUpdate>QPLAY_SKIP_NEXT_FAILED</errorUpdate></updates>`))
	c.handleMessage(context.Background(), []byte(`<updates><errorUpdate>QPLAY_SKIP_PREV_FAILED</errorUpdate></updates>`))
	if len(h.skips) != 2 || h.skips[0] != true || h.skips[1] != false {
		t.Fatalf("expected [next, prev] skips, got %v", h.skips)
	}
}
