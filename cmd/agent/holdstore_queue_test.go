package main

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/presets"
)

func heldUPnP(slot int) marge.HeldItem {
	return marge.HeldItem{Slot: slot, Source: "UPNP", Type: "track", Location: "http://192.0.2.10:9000/disk/01.mp3", ItemName: "Track 1"}
}

func liveFolder(slot int) (presets.Preset, bool) {
	return presets.Preset{
		Slot: slot, Name: "Abbey Road", Type: "queue", Shuffle: true, Source: "Living Room NAS",
		Items: []presets.PresetItem{
			{URL: "http://192.0.2.10:9000/disk/01.mp3", Title: "Come Together"},
			{URL: "http://192.0.2.10:9000/disk/02.mp3", Title: "Something"},
		},
	}, true
}

// Holding a key while a media-server folder plays as a queue keeps the FOLDER,
// with its media server as the source (#1030), not a refusal.
func TestHeldKeyDuringFolderQueueKeepsTheFolder(t *testing.T) {
	store := holdTestStore(t)
	keep := newHeldPresetKeeper(store, heldLive{queue: liveFolder}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := keep(heldUPnP(3)); err != nil {
		t.Fatalf("folder hold refused: %v", err)
	}
	got, ok := store.Get(3)
	if !ok {
		t.Fatal("slot 3 not stored")
	}
	if got.Type != "queue" || got.Name != "Abbey Road" || got.Source != "Living Room NAS" ||
		!got.Shuffle || len(got.Items) != 2 || got.Items[1].Title != "Something" {
		t.Fatalf("stored %+v", got)
	}
	// A re-statement of the same folder is no write and no error.
	_, changed, err := heldPresetCandidate(store, heldLive{queue: liveFolder}, heldUPnP(3))
	if err != nil || changed {
		t.Fatalf("re-hold: changed=%v err=%v, want unchanged", changed, err)
	}
}

// With no queue playing, a UPnP item stays not keepable, as before.
func TestHeldUPnPWithoutQueueIsRefused(t *testing.T) {
	store := holdTestStore(t)
	none := func(int) (presets.Preset, bool) { return presets.Preset{}, false }
	for _, live := range []heldLive{{}, {queue: none}} {
		if _, _, err := heldPresetCandidate(store, live, heldUPnP(3)); !errors.Is(err, errNotKeepable) {
			t.Fatalf("err = %v, want errNotKeepable", err)
		}
	}
}
