package boxws

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// recHandler records which gabbo events the parser dispatched so tests can
// assert that marker words in user-supplied text no longer mis-fire.
type recHandler struct {
	presets    []int
	userStops  int
	powerKeys  int
	powerWakes int
	sourceAux  int
	skips      []bool
	zones      []ZoneState
	mu         sync.Mutex
	thumbs     int // guarded by mu (fired from the debounce timer goroutine)
}

func (h *recHandler) thumbCount() int { h.mu.Lock(); defer h.mu.Unlock(); return h.thumbs }

func (h *recHandler) OnPresetSelected(_ context.Context, slot int, _ string, _ string) {
	h.presets = append(h.presets, slot)
}
func (h *recHandler) OnRemoteSkip(_ context.Context, forward bool) {
	h.skips = append(h.skips, forward)
}
func (h *recHandler) OnUserStop(context.Context)                   { h.userStops++ }
func (h *recHandler) OnThumbActivity(context.Context)              { h.mu.Lock(); h.thumbs++; h.mu.Unlock() }
func (h *recHandler) OnPowerKey(context.Context)                   { h.powerKeys++ }
func (h *recHandler) OnSourceAux(context.Context)                  { h.sourceAux++ }
func (h *recHandler) OnZoneChanged(_ context.Context, z ZoneState) { h.zones = append(h.zones, z) }
func (h *recHandler) OnPowerWake(context.Context)                  { h.powerWakes++ }

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

func TestHandleMessage_DoNotResumeIsRespected(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	// A standby wake / source teardown the box marks DO_NOT_RESUME must NOT make
	// STR resume playback (boxes playing on their own; AirPlay not stopping).
	frame := `<updates><nowSelectionUpdated><preset id="0">` +
		`<ContentItem source="INVALID_SOURCE" type="DO_NOT_RESUME" location="http://x">` +
		`<itemName>x</itemName></ContentItem></preset></nowSelectionUpdated></updates>`
	c.handleMessage(context.Background(), []byte(frame))
	if len(h.presets) != 0 {
		t.Fatalf("DO_NOT_RESUME must not play a preset, got %v", h.presets)
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

func TestHandleMessage_ZoneUpdatedParsed(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	// A box-formed stereo pair / zone (Klaus #70): previously this fell through
	// as an "unrecognized frame"; it must now parse into a typed ZoneState.
	frame := `<updates><zoneUpdated><zone master="B0D5CCC4D6CB" senderIPAddress="192.0.2.38" senderIsMaster="true">` +
		`<member ipaddress="192.0.2.39" role="right">B0D5CCC4D7AA</member></zone></zoneUpdated></updates>`
	c.handleMessage(context.Background(), []byte(frame))
	if len(h.zones) != 1 {
		t.Fatalf("expected one zone event, got %d", len(h.zones))
	}
	z := h.zones[0]
	if z.Master != "B0D5CCC4D6CB" || !z.SenderIsMaster || len(z.Members) != 1 {
		t.Fatalf("zone parsed wrong: %+v", z)
	}
	if z.Members[0].DeviceID != "B0D5CCC4D7AA" || z.Members[0].IP != "192.0.2.39" || z.Members[0].Role != "right" {
		t.Fatalf("member parsed wrong: %+v", z.Members[0])
	}
}

func TestHandleMessage_ZoneDissolvedParsed(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(`<updates><zoneUpdated><zone /></zoneUpdated></updates>`))
	if len(h.zones) != 1 || h.zones[0].Master != "" {
		t.Fatalf("empty zone must fire one ZoneState with empty Master, got %+v", h.zones)
	}
}

// TestHandleMessage_PowerWake guards the power-on signal the resume binds to.
// Verified live on a Portable/taigan (2026-06-13): the box sends NO
// powerStateUpdated; a real power press surfaces as a DO_NOT_RESUME selection
// restore. So BOTH a powerStateUpdated (firmware that sends it) AND the
// DO_NOT_RESUME restore must fire OnPowerWake, while a power-OFF (STANDBY) fires
// the OnPowerKey webhook and never OnPowerWake. The self-wake vs user-press
// distinction is made downstream by zone membership, not here, because the two
// are identical on the wire.
func TestHandleMessage_PowerWake(t *testing.T) {
	// powerStateUpdated not STANDBY -> OnPowerWake (for firmware that sends it).
	h := &recHandler{}
	c := newTestClient(h)
	c.handleMessage(context.Background(), []byte(`<updates><powerStateUpdated>POWER_ON</powerStateUpdated></updates>`))
	if h.powerWakes != 1 || h.powerKeys != 0 {
		t.Fatalf("powerState ON must fire OnPowerWake once, not OnPowerKey: wakes=%d keys=%d", h.powerWakes, h.powerKeys)
	}

	// Power-OFF (STANDBY) -> OnPowerKey, never OnPowerWake.
	h = &recHandler{}
	c = newTestClient(h)
	c.handleMessage(context.Background(), []byte(`<updates><powerStateUpdated>STANDBY</powerStateUpdated></updates>`))
	if h.powerKeys != 1 || h.powerWakes != 0 {
		t.Fatalf("standby must fire OnPowerKey once, not OnPowerWake: keys=%d wakes=%d", h.powerKeys, h.powerWakes)
	}

	// The DO_NOT_RESUME selection restore (the only power-on signal on SoundTouch
	// firmware) must fire OnPowerWake, and must NOT be mistaken for a preset.
	h = &recHandler{}
	c = newTestClient(h)
	c.handleMessage(context.Background(), []byte(
		`<updates><nowSelectionUpdated><preset id="0">`+
			`<ContentItem source="INVALID_SOURCE" type="DO_NOT_RESUME"/>`+
			`</preset></nowSelectionUpdated></updates>`))
	if h.powerWakes != 1 || len(h.presets) != 0 {
		t.Fatalf("DO_NOT_RESUME restore must fire OnPowerWake only: wakes=%d presets=%v", h.powerWakes, h.presets)
	}
}

func TestRootLocalName(t *testing.T) {
	cases := map[string]string{
		`<userActivityUpdate deviceID="x" />`:              "userActivityUpdate",
		`<updates><volumeUpdated/></updates>`:              "updates",
		`<?xml version="1.0"?><errorUpdate></errorUpdate>`: "errorUpdate",
		``:        "",
		`not xml`: "",
	}
	for in, want := range cases {
		if got := rootLocalName(in); got != want {
			t.Errorf("rootLocalName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHandleMessage_BareUserActivityFiresThumb(t *testing.T) {
	h := &recHandler{}
	c := newTestClient(h)
	// A bare-root <userActivityUpdate/> (not wrapped in <updates>) must still be
	// recognised as a lone thumb ping, not dropped as an unrecognized frame.
	c.handleMessage(context.Background(), []byte(`<userActivityUpdate deviceID="x" />`))
	// noteUserActivity fires OnThumbActivity after a short settle window.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.thumbCount() == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	if h.thumbCount() != 1 {
		t.Fatalf("bare userActivityUpdate must fire one thumb, got %d", h.thumbCount())
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
