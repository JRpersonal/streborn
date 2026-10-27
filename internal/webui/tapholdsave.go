package webui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/JRpersonal/streborn/internal/presets"
)

// Tap a preset key, then hold it: save what played before the tap onto that
// key's second row (#1110, cmd/agent/tapholdsave.go).
//
// The tap makes the firmware switch to the key's own station at once, so by
// the time the hold is recognised (about 2.5 s later) the speaker plays
// something else. The agent therefore takes a snapshot of what played the
// moment the tap's selection frame arrives (SnapshotPlaying, from the last
// now-playing document the bus carried before the tap, and the agent's own
// in-memory record of the play), and stores it once the hold is confirmed
// (SaveSnapshot). Both use the very paths a held key and the phone page's
// "save what plays" use, so every kind of item is kept the way it is there.

// tapHoldUA marks the internal PUT of a tap+hold in the "preset write
// accepted" line. Like holdSaveUA it shows no app notice: the agent puts the
// outcome on the display itself.
const tapHoldUA = "STR speaker key tap+hold"

// speakerKeyUA reports whether ua is one of the agent's own saves from a key
// on the speaker, which draw their own display notice.
func speakerKeyUA(ua string) bool { return ua == holdSaveUA || ua == tapHoldUA }

// PlayingSnapshot is what the speaker played at one moment, as the preset a
// save would store, with the slot left open.
type PlayingSnapshot struct {
	// Kind is spotify, queue, library, copy, stream or native; "" when
	// nothing can be kept (Code says why).
	Kind   string
	Preset presets.Preset
	// Source is the speaker's source at the time (UPNP, AUX, STANDBY, ...).
	Source string
	// FromSlot is, for a copy, the key whose own preset played.
	FromSlot int
	// Code says why nothing can be kept: nothing-playing,
	// nowplaying-unknown, spotify-context-unknown, nothing-saveable, ...
	Code string
	// live marks a Spotify save of the engine's own context (?save=live).
	live bool
}

// Savable reports whether the snapshot holds something to store.
func (p PlayingSnapshot) Savable() bool { return p.Kind != "" && p.Code == "" }

// Name is the name the snapshot would be saved under.
func (p PlayingSnapshot) Name() string { return p.Preset.Name }

// nothingSources are the speaker's sources that never carry an item a key
// can keep.
var nothingSources = map[string]bool{
	"": true, "STANDBY": true, "INVALID_SOURCE": true, "AUX": true, "BLUETOOTH": true,
	"PRODUCT": true, "SETUP": true, "NOTIFICATION": true, "QPLAY": true,
}

// SnapshotPlaying turns a now-playing document (doc, as :8090/now_playing or
// the bus's <nowPlaying> carries it) into what a save of it would store. It
// reads only memory: the document, the live queue, the agent's record of its
// last play, the Spotify engine's context and the preset store. A nil doc is
// "not known".
func (s *Server) SnapshotPlaying(doc []byte) PlayingSnapshot {
	if len(doc) == 0 {
		return PlayingSnapshot{Code: "nowplaying-unknown"}
	}
	now, ok := parseBoxNowItem(doc)
	if !ok {
		return PlayingSnapshot{Code: "nowplaying-unreadable"}
	}
	snap := PlayingSnapshot{Source: now.Source}
	if nothingSources[now.Source] || now.Location == "" {
		snap.Code = "nothing-playing"
		return snap
	}
	loc := now.Location
	if now.Source == "SPOTIFY" {
		// The firmware's own Spotify session: the engine STR runs knows
		// nothing about what it plays, and an old engine context would name
		// the wrong playlist.
		snap.Code = "spotify-native"
		return snap
	}
	if looksLikeSpotifyStreamURL(loc) {
		uri := ""
		if s.spotifyContext != nil {
			uri = normalizeSpotifyURI(s.spotifyContext())
		}
		if uri == "" {
			uri = legacySpotifyURI(loc)
		}
		if uri == "" {
			snap.Code = "spotify-context-unknown"
			return snap
		}
		// "Spotify" is the placeholder the per-slot save replaces with the
		// playlist's own title, as for a held key.
		snap.Kind, snap.live = "spotify", true
		snap.Preset = presets.Preset{Name: "Spotify", Type: "spotify", URI: uri}
		return snap
	}
	if _, native := presets.NativeServiceLabel(presets.NormalizeNativeSource(now.Source)); native {
		item, ok := parseNowPlayingNativeItem(doc)
		var p presets.Preset
		if ok {
			p, ok = presets.NewNativePreset(0, item, "")
		}
		if !ok {
			snap.Code = "nothing-saveable"
			return snap
		}
		snap.Kind, snap.Preset = "native", p
		return snap
	}
	if now.Source == "UPNP" {
		if q, ok := s.LiveQueuePreset(0); ok {
			snap.Kind, snap.Preset = "queue", q
			return snap
		}
		if p, ok := s.liveLibraryFilePreset(0, loc); ok {
			snap.Kind, snap.Preset = "library", p
			return snap
		}
	}
	p, kind, ref, refusal := s.playingStreamPreset(now)
	if refusal != nil {
		snap.Code = refusal.Code
		return snap
	}
	snap.Kind, snap.Preset, snap.FromSlot = kind, p, ref
	return snap
}

// SaveSnapshot stores snap on slot through the paths a save from the app
// takes, with their rules: the one-item-one-key refusal (#836) comes back as
// a HoldSaveError with code already-on-slot and the other key, which the
// caller reports; nothing is moved or swapped.
func (s *Server) SaveSnapshot(ctx context.Context, snap PlayingSnapshot, slot int) (HoldSaved, error) {
	if s.presets == nil {
		return HoldSaved{}, errors.New("presets store not initialized")
	}
	if !presets.ValidSlot(slot) {
		return HoldSaved{}, fmt.Errorf("invalid slot %d", slot)
	}
	if !snap.Savable() {
		return HoldSaved{}, fmt.Errorf("nothing to save (%s)", snap.Code)
	}
	p := snap.Preset
	p.Slot = slot
	switch snap.Kind {
	case "queue":
		if cur, have := s.presets.Get(slot); have && cur.Type == "queue" &&
			samePresetItems(cur, p) && cur.Name == p.Name && cur.Source == p.Source {
			return HoldSaved{Slot: slot, Kind: "queue", Name: cur.Name, Unchanged: true}, nil
		}
		for _, other := range s.presets.All() {
			if other.Slot != slot && other.Type == "queue" && samePresetItems(other, p) &&
				strings.EqualFold(other.Source, p.Source) {
				return HoldSaved{}, &HoldSaveError{Code: "already-on-slot", OtherSlot: other.Slot, OtherName: other.Name}
			}
		}
		if err := s.storeQueuePreset(ctx, p); err != nil {
			return HoldSaved{}, err
		}
		s.logger.Info("queue preset save: stored the folder that played before the tap on a key",
			"slot", p.Slot, "name", p.Name, "source", p.Source, "tracks", len(p.Items),
			"shuffle", p.Shuffle, "from", "speaker key tap+hold")
		return HoldSaved{Slot: slot, Kind: "queue", Name: p.Name}, nil
	case "copy":
		if snap.FromSlot == slot {
			return HoldSaved{Slot: slot, Kind: "copy", Name: p.Name, Unchanged: true}, nil
		}
	}
	return s.saveThroughPut(ctx, p, snap.Kind, snap.live, tapHoldUA, "speaker-key")
}
