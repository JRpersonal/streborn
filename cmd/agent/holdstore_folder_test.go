package main

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/webui"
)

// The speaker's own hold-to-store follows the one-folder-one-key rule of the
// app's saves (#1695): a music-library folder already on another key is
// refused, the way a station is (#836).

func heldFolder(slot int) presets.Preset {
	return presets.Preset{
		Slot: slot, Name: "Power Windows", Type: presets.TypeQueue, Source: "MinimServer",
		Folder: &presets.FolderRef{ServerUDN: "uuid:00000000-0000-0000-0000-00000000000a", ContainerID: "0$1$7"},
		Items: []presets.PresetItem{
			{URL: "http://192.0.2.10:9790/minimserver/*/music/01.flac"},
			{URL: "http://192.0.2.10:9790/minimserver/*/music/02.flac"},
		},
	}
}

func TestHeldFolderAlreadyOnAnotherKeyIsRefused(t *testing.T) {
	store := holdTestStore(t)
	if err := store.SetSlot(heldFolder(3)); err != nil {
		t.Fatal(err)
	}
	live := heldLive{queue: func(slot int) (presets.Preset, bool) { return heldFolder(slot), true }}
	keep := newHeldPresetKeeper(store, live, slog.New(slog.NewTextHandler(io.Discard, nil)))
	upnp := marge.HeldItem{Slot: 6, Source: "UPNP", Location: "http://192.0.2.10:9790/minimserver/*/music/01.flac", ItemName: "The Big Money"}

	err := keep(upnp)
	var dup *alreadyOnKeyError
	if !errors.As(err, &dup) || dup.slot != 3 || dup.name != "Power Windows" {
		t.Fatalf("err = %v, want already on key 3", err)
	}
	if _, ok := store.Get(6); ok {
		t.Fatal("key 6 must stay empty")
	}
	// Onto its own key it is no collision.
	upnp.Slot = 3
	if err := keep(upnp); err != nil {
		t.Fatalf("own key: %v", err)
	}
	// A folder key's own proxy copied onto another key is a duplicate too.
	if err := keep(heldRadio(6, webui.OrionStationLocation(boxurl.StreamSlot(3), "Power Windows", ""), "Power Windows")); !errors.As(err, &dup) || dup.slot != 3 {
		t.Fatalf("proxy copy: err = %v, want already on key 3", err)
	}
}
