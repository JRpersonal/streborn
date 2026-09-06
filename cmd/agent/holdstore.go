// The speaker's own hold-to-store gesture, mapped onto STR's preset store.
//
// Holding a preset key stores the playing item on that key inside the
// firmware, which then PUTs the item to marge (see internal/marge/presetstore.go).
// This keeper is what marge hands the item to. It turns the native station the
// firmware plays into the preset STR would have saved from the app - origin
// stream, origin artwork, same slot - so the app shows the key the user just
// set and the reconcile keeps it registered.

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/webui"
)

// errNotKeepable is the reason class for items the speaker may hold but STR
// has no preset form for (a UPnP push, a Deezer playlist, an unreadable
// station descriptor).
var errNotKeepable = errors.New("not a station STR can keep")

// newHeldPresetKeeper builds the marge PresetKeeper over the agent's store.
//
// It never writes to the box. The firmware is in the middle of its own store
// gesture while this runs (it waits for marge's answer before it updates its
// slot), and a TAP AddPreset into that window would start a second gesture.
// The box slot is the firmware's to write here; the reconcile later rewrites
// it once onto STR's own form (see reconcileOnce).
func newHeldPresetKeeper(store *presets.Store, logger *slog.Logger) marge.PresetKeeper {
	return func(item marge.HeldItem) error {
		candidate, changed, err := heldPresetCandidate(store, item)
		if err != nil {
			return err
		}
		if !changed {
			// The boot-time sync PUTs every native slot the box holds, and a
			// re-hold of a key while its own station plays lands here too: the
			// store already says exactly this, so no NAND write and no noise.
			logger.Debug("hold-to-store: the box re-stated a slot the store already holds",
				"slot", item.Slot, "name", candidate.Name)
			return nil
		}
		prev, had := store.Get(item.Slot)
		if err := store.SetSlot(candidate); err != nil {
			return fmt.Errorf("preset store write: %w", err)
		}
		was := ""
		if had {
			was = prev.Name
		}
		logger.Info("hold-to-store: kept the station the speaker stored on a key with its own hold gesture",
			"slot", item.Slot, "was", was, "now", candidate.Name, "stream", candidate.StreamURL)
		return nil
	}
}

// heldPresetCandidate maps the held item onto the preset STR should hold in
// its slot. changed is false when the store already holds that preset.
//
// Only a native radio station qualifies. Its descriptor names the stream the
// speaker fetches, which is one of three things: this agent's per-slot proxy
// (the station is one of STR's own keys; the origin lives in that slot of the
// store), the ad-hoc raw proxy an app play routes through (the origin is
// inside it), or a plain origin URL. The artwork is unwound the same way.
//
// A station already on ANOTHER key is refused, the rule the app's save path
// enforces (#836): the desktop app's own hold-to-save fires on the same key
// press and answers "already on key N", and the speaker must not quietly do
// what the app just declined. Refusing is loss-free; the firmware keeps the
// key as it was.
func heldPresetCandidate(store *presets.Store, item marge.HeldItem) (candidate presets.Preset, changed bool, err error) {
	if !strings.EqualFold(strings.TrimSpace(item.Source), "LOCAL_INTERNET_RADIO") {
		return presets.Preset{}, false, fmt.Errorf("%w: source %s", errNotKeepable, item.Source)
	}
	st, ok := webui.DecodeNativeStation(item.Location)
	if !ok {
		return presets.Preset{}, false, fmt.Errorf("%w: unreadable station descriptor", errNotKeepable)
	}
	name := st.Name
	if name == "" {
		name = item.ItemName
	}
	switch {
	case st.ProxySlot > 0:
		src, have := store.Get(st.ProxySlot)
		if !have || src.StreamURL == "" && src.URI == "" && len(src.Items) == 0 {
			return presets.Preset{}, false, fmt.Errorf("%w: the descriptor points at key %d's proxy, and the store has no station on that key",
				errNotKeepable, st.ProxySlot)
		}
		if st.ProxySlot == item.Slot {
			return src, false, nil
		}
		// Another key's station: the preset is copied whole (codec, bitrate,
		// homepage, queue items ride along), only the slot changes.
		candidate = src
		candidate.Slot = item.Slot
	case st.OriginStreamURL != "":
		candidate = presets.Preset{
			Slot:      item.Slot,
			Name:      name,
			StreamURL: st.OriginStreamURL,
			Type:      "radio",
			Art:       st.Art,
		}
	default:
		return presets.Preset{}, false, fmt.Errorf("%w: the descriptor's stream is not a station origin (%s)",
			errNotKeepable, st.StreamURL)
	}
	if candidate.Name == "" {
		candidate.Name = name
	}
	for _, other := range store.All() {
		if other.Slot == item.Slot {
			continue
		}
		dup := (candidate.URI != "" && other.URI == candidate.URI) ||
			(candidate.StreamURL != "" && other.StreamURL == candidate.StreamURL)
		if dup {
			return presets.Preset{}, false, fmt.Errorf("%w: %q is already on key %d",
				errNotKeepable, other.Name, other.Slot)
		}
	}
	if cur, have := store.Get(item.Slot); have && samePresetContent(cur, candidate) {
		return cur, false, nil
	}
	return candidate, true, nil
}

// samePresetContent reports whether two presets describe the same station the
// same way, ignoring nothing that the app would show differently.
func samePresetContent(a, b presets.Preset) bool {
	return a.Name == b.Name && a.StreamURL == b.StreamURL && a.URI == b.URI &&
		a.Type == b.Type && a.Art == b.Art && len(a.Items) == len(b.Items)
}
