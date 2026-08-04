// Package boxws connects to the Bose WebSocket notification stream on port
// 8080 (subprotocol "gabbo") and reacts to incoming events.
//
// When a user presses a physical preset button on the box, the BoseApp sends
// an `<updates>` message over this WebSocket with `presetSelectionUpdated` or
// `nowPlayingUpdated`. We hook the event and trigger our UPnP player with the
// associated stream URL.
//
// This is what makes the hardware preset buttons work even though Bose's own
// music services are disabled in the firmware.
package boxws

import (
	"context"
	"strings"
)

// PresetEvent is fired when the box reports that a preset slot was
// selected.
type PresetEvent struct {
	Slot int
}

// Handler receives incoming events from the box WebSocket.
type Handler interface {
	// OnPresetSelected is fired when the box reports that a preset slot was
	// actively selected (physical hardware button or API trigger). location
	// and title come from the box ContentItem and can be sent to the box over
	// UPnP.
	OnPresetSelected(ctx context.Context, slot int, location string, title string)

	// OnRemoteSkip is fired when the remote presses next/previous track (the
	// box cannot skip a UPnP source itself and reports QPLAY_SKIP_*_FAILED).
	// forward=true -> next, false -> prev.
	OnRemoteSkip(ctx context.Context, forward bool)

	// OnUserStop is fired when the box reports that playback was stopped
	// (playStatus STOP_STATE in a nowPlayingUpdated event), i.e. the user
	// deliberately stopped it via remote/box. The agent uses this to avoid
	// running the auto-resume against a deliberate stop.
	OnUserStop(ctx context.Context)

	// OnThumbActivity is fired when the box reports a "bare"
	// userActivityUpdate: a key press without an accompanying
	// volume/nowPlaying/preset event. On this firmware the remote thumb keys
	// only deliver this generic event with no up/down identity; a bare
	// userActivity is the best available approximation for a thumb press. The
	// agent uses it as a (single, non up/down-distinguishable) trigger for a
	// configured webhook. Debounced and filtered against volume/preset in
	// boxws; still heuristic, hence live tunable.
	OnThumbActivity(ctx context.Context)

	// OnPowerKey is fired on a powerStateUpdated (power button / standby
	// change). For the optional "power" webhook (additive only: STR cannot
	// suppress the firmware-side power on/off). Beta.
	OnPowerKey(ctx context.Context)

	// OnSourceAux is fired when the active source switches to AUX. For the
	// optional "aux" webhook (additive only; the firmware switches the input
	// anyway). Detected heuristically via the source change, hence beta.
	OnSourceAux(ctx context.Context)

	// OnZoneChanged is fired when the box changes its multiroom zone or its
	// stereo pair (zoneUpdated). This lets STR also know about groups that
	// were NOT formed in STR (e.g. a stereo pair defined in AfterTouch/Bose),
	// instead of discarding the frame as "unrecognized". z.Master == "" means
	// the zone was dissolved.
	OnZoneChanged(ctx context.Context, z ZoneState)

	// OnGroupChanged is fired when the box's STEREO PAIR changes (groupUpdated).
	// The firmware keeps pairs separate from zones, so this is the only event
	// that reports one. g.Paired() == false means the pair was torn down, from
	// whichever app did it - including the Bose app, which STR cannot observe
	// any other way.
	OnGroupChanged(ctx context.Context, g GroupState)

	// OnPowerWake is fired when the box comes out of standby: either via a
	// powerStateUpdated (NOT STANDBY) on firmware that sends it, OR, on
	// SoundTouch firmware that does NOT send a powerStateUpdated (Portable/
	// taigan, confirmed live 2026-06-13), via the DO_NOT_RESUME restore of the
	// last selection on wake. Driver for the optional "resume the last station
	// on power-on" default. A self-wake (stereo pair/zone) looks identical and
	// is caught downstream via the zone membership (webui.boxInZone), not here.
	OnPowerWake(ctx context.Context)

	// OnPresetsChanged fires when the box reports its own preset list
	// (presetsUpdated). It delivers ALL of the box's presets, including foreign
	// sources (DEEZER, LOCAL_INTERNET_RADIO, ...) that STR did not set. This lets
	// STR show and recall the box's existing presets (the box plays e.g. a Deezer
	// preset through its own cached account) instead of just logging the slot IDs.
	OnPresetsChanged(ctx context.Context, presets []BoxPreset)
}

// BoxPreset is one preset reported by the box's own presetsUpdated frame,
// including its source. STR uses it to detect, show and keep foreign presets
// (ones STR did not set), such as Deezer.
type BoxPreset struct {
	Slot          int    `json:"slot"`          // 1..6
	Source        string `json:"source"`        // DEEZER / LOCAL_INTERNET_RADIO / SPOTIFY / UPNP / ...
	Type          string `json:"type"`          // playlist / stationurl / tracklistRadio / ...
	Location      string `json:"location"`      // stream URL, Deezer playlist ID, ...
	SourceAccount string `json:"sourceAccount"` // linked account (e.g. Deezer account)
	Name          string `json:"name"`          // itemName
}

// attrValue pulls attr="VALUE" out of a raw XML fragment, or "".
func attrValue(s, attr string) string {
	key := attr + `="`
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	r := s[i+len(key):]
	if j := strings.IndexByte(r, '"'); j >= 0 {
		return r[:j]
	}
	return ""
}

func preview(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}
