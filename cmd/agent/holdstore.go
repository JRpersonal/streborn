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
	"sync/atomic"

	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/webui"
)

// errNotKeepable is the reason class for items the speaker may hold but STR
// has no preset form for (a UPnP push, a Deezer playlist, an unreadable
// station descriptor).
var errNotKeepable = errors.New("not a station STR can keep")

// heldLive is what the keeper reads from the running agent beyond the preset
// store. The func may be nil (tests, or a hold that lands before the web
// server is up), which means "nothing known".
type heldLive struct {
	// queue builds the folder preset for a slot from the media-server folder
	// the agent plays as a queue right now; ok is false when none plays
	// (webui.Server.LiveQueuePreset, #1030).
	queue func(slot int) (presets.Preset, bool)
}

// heldWebui is the running web server, stored once main has created it.
var heldWebui atomic.Pointer[webui.Server]

// heldLiveFrom binds heldLive to a web server that is created after the
// marge stand-in the keeper is handed to.
func heldLiveFrom(srv *atomic.Pointer[webui.Server]) heldLive {
	return heldLive{
		queue: func(slot int) (presets.Preset, bool) {
			if s := srv.Load(); s != nil {
				return s.LiveQueuePreset(slot)
			}
			return presets.Preset{}, false
		},
	}
}

// newHeldPresetKeeper builds the marge PresetKeeper over the agent's store.
//
// It never writes to the box. The firmware is in the middle of its own store
// gesture while this runs (it waits for marge's answer before it updates its
// slot), and a TAP AddPreset into that window would start a second gesture.
// The box slot is the firmware's to write here; the reconcile later rewrites
// it once onto STR's own form (see reconcileOnce).
func newHeldPresetKeeper(store *presets.Store, live heldLive, logger *slog.Logger) marge.PresetKeeper {
	return func(item marge.HeldItem) error {
		candidate, changed, err := heldPresetCandidate(store, live, item)
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
		if candidate.IsNative() {
			logger.Info("hold-to-store: kept a station of a service the speaker plays itself",
				"slot", item.Slot, "was", was, "now", candidate.Name,
				"source", candidate.Native.Source, "type", candidate.Native.ItemType,
				"location", candidate.Native.Location)
			return nil
		}
		if candidate.Type == "queue" {
			logger.Info("hold-to-store: kept the music-library folder that plays on the key the speaker stored",
				"slot", item.Slot, "was", was, "now", candidate.Name, "source", candidate.Source,
				"tracks", len(candidate.Items), "shuffle", candidate.Shuffle)
			return nil
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
//
// A media-server folder STR plays as a queue is a UPnP push to the speaker,
// so the item names one track. The folder is what the user hears, so the
// live queue's folder preset is kept instead (#1030), the same preset the
// Library star button stores.
func heldPresetCandidate(store *presets.Store, live heldLive, item marge.HeldItem) (candidate presets.Preset, changed bool, err error) {
	if _, native := presets.NativeServiceLabel(item.Source); native {
		return heldNativeCandidate(store, item)
	}
	if strings.EqualFold(strings.TrimSpace(item.Source), "UPNP") && live.queue != nil {
		if q, ok := live.queue(item.Slot); ok {
			if cur, have := store.Get(item.Slot); have && samePresetContent(cur, q) && cur.Source == q.Source {
				return cur, false, nil
			}
			return q, true, nil
		}
	}
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

// heldNativeCandidate maps a held station of a music service the speaker plays
// by itself (Pandora, iHeartRadio) onto a native preset: the item is kept as
// the speaker described it, because STR has nothing to proxy and the firmware
// is the only thing that can play it (docs/streaming/us-services.md).
//
// The rules are the station rules: a re-statement of what the key already
// holds is no write (the boot-time sync PUTs every slot, and the speaker's own
// name must not undo a rename made in the app), and a station already on
// another key is refused (#836).
func heldNativeCandidate(store *presets.Store, item marge.HeldItem) (presets.Preset, bool, error) {
	native := presets.NativeItem{
		Source:        item.Source,
		SourceAccount: item.SourceAccount,
		Location:      item.Location,
		ItemType:      item.Type,
		ItemName:      item.ItemName,
		ContainerArt:  item.ContainerArt,
	}
	candidate, ok := presets.NewNativePreset(item.Slot, native, "")
	if !ok {
		return presets.Preset{}, false, fmt.Errorf("%w: %s item without a location", errNotKeepable, item.Source)
	}
	for _, other := range store.All() {
		if other.Slot != item.Slot && other.IsNative() && presets.SameNativeItem(other.Native, candidate.Native) {
			return presets.Preset{}, false, fmt.Errorf("%w: %q is already on key %d",
				errNotKeepable, other.Name, other.Slot)
		}
	}
	if cur, have := store.Get(item.Slot); have && cur.IsNative() && presets.SameNativeItem(cur.Native, candidate.Native) {
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
