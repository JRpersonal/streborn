package main

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/JRpersonal/streborn/internal/marge"
)

// The #1101 field case: a Pandora station played on a US speaker, key 1 held.
// The ContentItem shape is a reconstruction (the real one was never seen):
// what matters is that the item is kept verbatim.
func heldPandora(slot int, location, name string) marge.HeldItem {
	return marge.HeldItem{Slot: slot, Source: "PANDORA", SourceID: "200", Type: "stationurl",
		Location: location, SourceAccount: "listener@example.com", ItemName: name,
		ContainerArt: "https://example.com/art.jpg", Form: "flat"}
}

func TestHeldPandoraStationIsKeptAsANativePreset(t *testing.T) {
	store := holdTestStore(t)
	keep := newHeldPresetKeeper(store, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := keep(heldPandora(1, "4071226281950183516", "Little Big Town Radio")); err != nil {
		t.Fatalf("Pandora item refused: %v", err)
	}
	got, ok := store.Get(1)
	if !ok || !got.IsNative() {
		t.Fatalf("slot 1 = %+v, want a native preset", got)
	}
	if got.Name != "Little Big Town Radio" || got.Source != "Pandora" || got.StreamURL != "" ||
		got.Art != "https://example.com/art.jpg" {
		t.Fatalf("stored %+v", got)
	}
	n := got.Native
	if n.Source != "PANDORA" || n.Location != "4071226281950183516" || n.ItemType != "stationurl" ||
		n.SourceAccount != "listener@example.com" || n.ItemName != "Little Big Town Radio" {
		t.Fatalf("native item %+v", n)
	}
}

func TestHeldIHeartStationIsKeptToo(t *testing.T) {
	store := holdTestStore(t)
	keep := newHeldPresetKeeper(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	item := marge.HeldItem{Slot: 2, Source: "IHEART", Type: "stationurl", Location: "live:1234", ItemName: "Z100"}
	if err := keep(item); err != nil {
		t.Fatalf("iHeart item refused: %v", err)
	}
	if got, _ := store.Get(2); !got.IsNative() || got.Source != "iHeartRadio" || got.Native.Source != "IHEART" {
		t.Fatalf("stored %+v", got)
	}
}

// The boot-time sync re-states every slot. A key the user renamed in the app
// must keep its name, and no write may happen.
func TestHeldNativeReStateIsNoWriteAndKeepsARename(t *testing.T) {
	store := holdTestStore(t)
	keep := newHeldPresetKeeper(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := keep(heldPandora(1, "4071226281950183516", "Little Big Town Radio")); err != nil {
		t.Fatal(err)
	}
	p, _ := store.Get(1)
	p.Name = "Country"
	if err := store.SetSlot(p); err != nil {
		t.Fatal(err)
	}
	_, changed, err := heldPresetCandidate(store, heldPandora(1, "4071226281950183516", "Little Big Town Radio"))
	if err != nil || changed {
		t.Fatalf("re-state: changed=%v err=%v, want no change", changed, err)
	}
	if err := keep(heldPandora(1, "4071226281950183516", "Little Big Town Radio")); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get(1); got.Name != "Country" {
		t.Fatalf("rename lost: %+v", got)
	}
}

func TestHeldNativeAlreadyOnAnotherKeyIsRefused(t *testing.T) {
	store := holdTestStore(t)
	keep := newHeldPresetKeeper(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := keep(heldPandora(5, "4071226281950183516", "Little Big Town Radio")); err != nil {
		t.Fatal(err)
	}
	err := keep(heldPandora(2, "4071226281950183516", "Little Big Town Radio"))
	if !errors.Is(err, errNotKeepable) {
		t.Fatalf("duplicate on key 2: err=%v, want errNotKeepable", err)
	}
	if _, ok := store.Get(2); ok {
		t.Fatal("key 2 written despite the refusal")
	}
}

// Sources STR does not keep stay refused, and the radio path is unchanged.
func TestHeldUnknownSourcesStillRefused(t *testing.T) {
	store := holdTestStore(t)
	for _, src := range []string{"UPNP", "DEEZER", "SPOTIFY", "SOURCE#150", ""} {
		_, _, err := heldPresetCandidate(store, marge.HeldItem{Slot: 2, Source: src, Location: "x", ItemName: "x"})
		if !errors.Is(err, errNotKeepable) {
			t.Fatalf("source %q: err=%v, want errNotKeepable", src, err)
		}
	}
	if _, _, err := heldPresetCandidate(store, marge.HeldItem{Slot: 2, Source: "PANDORA", ItemName: "x"}); !errors.Is(err, errNotKeepable) {
		t.Fatalf("Pandora item without a location: err=%v", err)
	}
	// A radio station still goes the radio way, never the native one.
	_, _, err := heldPresetCandidate(store, heldRadio(2, "/station?data=not-base64", "x"))
	if !errors.Is(err, errNotKeepable) {
		t.Fatalf("unreadable radio descriptor: err=%v", err)
	}
	if p, _ := store.Get(1); p.Type != "radio" || p.Native != nil {
		t.Fatalf("radio slot changed: %+v", p)
	}
}

// #1101: the app's hold-to-save stored the key with the account from
// now_playing, and the speaker's own store record for the same press (which
// names no account) then replaced it. The record re-states the key now.
func TestHeldNativeRecordWithoutAccountKeepsTheAppsAccount(t *testing.T) {
	store := holdTestStore(t)
	keep := newHeldPresetKeeper(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := keep(heldPandora(2, "175477659894029190", "Chris Stapleton Radio")); err != nil {
		t.Fatalf("app-saved item refused: %v", err)
	}
	fromSpeaker := heldPandora(2, "175477659894029190", "Chris Stapleton Radio")
	fromSpeaker.SourceAccount = ""
	if err := keep(fromSpeaker); err != nil {
		t.Fatalf("speaker record refused: %v", err)
	}
	if got, _ := store.Get(2); got.Native == nil || got.Native.SourceAccount != "listener@example.com" {
		t.Fatalf("stored %+v, want the app's account kept", got.Native)
	}
	// And it still counts as the same station for the one-key rule.
	fromSpeaker.Slot = 4
	if err := keep(fromSpeaker); !errors.Is(err, errNotKeepable) {
		t.Fatalf("same station on another key: err = %v", err)
	}
}
