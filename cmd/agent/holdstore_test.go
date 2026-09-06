package main

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/webui"
)

func holdTestStore(t *testing.T) *presets.Store {
	t.Helper()
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	must := func(p presets.Preset) {
		if err := store.SetSlot(p); err != nil {
			t.Fatal(err)
		}
	}
	must(presets.Preset{Slot: 1, Name: "1LIVE", StreamURL: "http://wdr-1live-live.icecast.wdr.de/wdr/1live/live/mp3/128/stream.mp3", Type: "radio", Codec: "MP3", Art: "https://example.com/1live.png"})
	must(presets.Preset{Slot: 4, Name: "Nickelback Radio", Type: "spotify", URI: "spotify:playlist:abc"})
	return store
}

func heldRadio(slot int, loc, name string) marge.HeldItem {
	return marge.HeldItem{Slot: slot, Source: "LOCAL_INTERNET_RADIO", Type: "stationurl", Location: loc, ItemName: name}
}

// An app play of a searched station runs through the ad-hoc raw proxy; the
// hold gesture on key 3 then lands that station's ORIGIN on key 3.
func TestHeldPresetFromAdHocPlayStoresTheOrigin(t *testing.T) {
	store := holdTestStore(t)
	keep := newHeldPresetKeeper(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	loc := webui.OrionStationLocation(boxurl.RawStream("https://stream.sunshine-live.de/90er/mp3-192/"), "Sunshine Live - Die 90er", "https://example.com/sl.png")

	if err := keep(heldRadio(3, loc, "Sunshine Live - Die 90er")); err != nil {
		t.Fatal(err)
	}
	got, ok := store.Get(3)
	if !ok {
		t.Fatal("slot 3 not stored")
	}
	if got.Name != "Sunshine Live - Die 90er" || got.StreamURL != "https://stream.sunshine-live.de/90er/mp3-192/" ||
		got.Type != "radio" || got.Art != "https://example.com/sl.png" {
		t.Fatalf("stored %+v", got)
	}
	// The same PUT again (the boot-time sync re-states every native slot)
	// changes nothing and must not fail.
	if err := keep(heldRadio(3, loc, "Sunshine Live - Die 90er")); err != nil {
		t.Fatal(err)
	}
	if n := len(store.All()); n != 3 {
		t.Fatalf("store has %d presets, want 3", n)
	}
}

// The boot-time sync PUTs each native slot with its own proxy: a no-op.
func TestHeldPresetOwnSlotProxyIsANoOp(t *testing.T) {
	store := holdTestStore(t)
	keep := newHeldPresetKeeper(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	loc := webui.OrionStationLocation(boxurl.StreamSlot(1), "1LIVE", "")
	if err := keep(heldRadio(1, loc, "1LIVE")); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(1)
	if got.StreamURL != "http://wdr-1live-live.icecast.wdr.de/wdr/1live/live/mp3/128/stream.mp3" || got.Codec != "MP3" {
		t.Fatalf("slot 1 was rewritten: %+v", got)
	}
}

// Holding key 3 while key 1's station plays: the station is already on a key,
// which the app's own hold-to-save answers with "already on key 1" (#836).
// The speaker gets the same refusal, and nothing in the store moves.
func TestHeldPresetAlreadyOnAnotherKeyIsRefused(t *testing.T) {
	store := holdTestStore(t)
	keep := newHeldPresetKeeper(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	loc := webui.OrionStationLocation(boxurl.StreamSlot(1), "1LIVE", "")
	err := keep(heldRadio(3, loc, "1LIVE"))
	if !errors.Is(err, errNotKeepable) {
		t.Fatalf("err = %v, want errNotKeepable", err)
	}
	if _, ok := store.Get(3); ok {
		t.Fatal("slot 3 must stay empty")
	}
	// Same station reached by origin URL (an app play of the station that is
	// on key 1): also a duplicate.
	loc = webui.OrionStationLocation(boxurl.RawStream("http://wdr-1live-live.icecast.wdr.de/wdr/1live/live/mp3/128/stream.mp3"), "1LIVE", "")
	if err := keep(heldRadio(3, loc, "1LIVE")); !errors.Is(err, errNotKeepable) {
		t.Fatalf("origin duplicate: err = %v", err)
	}
}

// A proxy slot the store does not back cannot be resolved to a station.
func TestHeldPresetUnknownProxySlotIsRefused(t *testing.T) {
	store := holdTestStore(t)
	keep := newHeldPresetKeeper(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	loc := webui.OrionStationLocation(boxurl.StreamSlot(6), "Ghost", "")
	if err := keep(heldRadio(6, loc, "Ghost")); !errors.Is(err, errNotKeepable) {
		t.Fatalf("err = %v", err)
	}
}

// What the firmware refuses itself for UPnP arrives here for other sources
// (a Deezer playlist, an unreadable descriptor): not keepable, store untouched.
func TestHeldPresetForeignOrUnreadableIsRefused(t *testing.T) {
	store := holdTestStore(t)
	keep := newHeldPresetKeeper(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cases := []marge.HeldItem{
		{Slot: 2, Source: "UPNP", Type: "audio", Location: boxurl.StreamSlot(4), SourceAccount: "UPnPUserName"},
		{Slot: 2, Source: "DEEZER", Type: "playlist", Location: "123456789", SourceAccount: "1456373802"},
		heldRadio(2, "/station?data=@@@", "x"),
		heldRadio(2, "http://example.com/live.mp3", "x"),
	}
	for _, item := range cases {
		if err := keep(item); !errors.Is(err, errNotKeepable) {
			t.Errorf("%s/%s: err = %v", item.Source, item.Location, err)
		}
	}
	if _, ok := store.Get(2); ok {
		t.Fatal("slot 2 must stay empty")
	}
}

// A slot the store already holds is overwritten by the gesture (that is what
// the firmware did: DeletePreset, then AddPreset).
func TestHeldPresetOverwritesTheKeysOldStation(t *testing.T) {
	store := holdTestStore(t)
	keep := newHeldPresetKeeper(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	loc := webui.OrionStationLocation(boxurl.RawStream("http://stream.example.com/new.mp3"), "New Station", "")
	if err := keep(heldRadio(1, loc, "New Station")); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(1)
	if got.Name != "New Station" || got.StreamURL != "http://stream.example.com/new.mp3" || got.Codec != "" {
		t.Fatalf("slot 1 = %+v", got)
	}
}
