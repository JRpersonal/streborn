package main

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/presets"
)

// Holding a preset key down while THAT key's own Spotify playlist is playing
// used to destroy the preset.
//
// The speaker's station descriptor names the stream it is fetching, which for a
// Spotify preset is the agent's own per-slot Ogg proxy. That URL was not
// recognised as one of STR's keys, so it fell through as if it were a station
// origin, and the slot was rewritten as a radio preset whose stream URL was the
// proxy: the playlist URI gone, the type wrong, and a "station" that only
// resolves while that slot happens to be playing Spotify. A reporter's bundle
// (2026-09-09) carried exactly that damage on two of three speakers, slots 3
// and 4 as type=radio pointing at .../spotify/stream-N.ogg, against a third box
// whose slot 4 was still a proper spotify preset.
//
// The right answer for a key held down while its own content plays is "nothing
// changes".
func TestHoldingASpotifyKeyDoesNotTurnItIntoARadioPreset(t *testing.T) {
	store := newHoldTestStore(t)
	want := presets.Preset{
		Slot: 4,
		Name: "Regen fuer die Nacht",
		Type: "spotify",
		URI:  "spotify:playlist:2YGwyf6VcOavvyPdPiYJg2",
	}
	if err := store.SetSlot(want); err != nil {
		t.Fatal(err)
	}

	item := marge.HeldItem{
		Slot:     4,
		Source:   "LOCAL_INTERNET_RADIO",
		ItemName: want.Name,
		Location: nativeStationLocation(t, want.Name, "http://127.0.0.1:8888/spotify/stream-4.ogg"),
	}

	got, changed, err := heldPresetCandidate(store, item)
	if err != nil {
		t.Fatalf("holding the key errored: %v", err)
	}
	if changed {
		t.Fatalf("holding a key down while its OWN preset plays rewrote it: %+v", got)
	}
	if got.Type != "spotify" || got.URI != want.URI {
		t.Fatalf("the Spotify preset was damaged: type=%q uri=%q, want type=spotify uri=%q", got.Type, got.URI, want.URI)
	}
	if got.StreamURL != "" {
		t.Fatalf("the preset picked up STR's own proxy URL as its station: %q", got.StreamURL)
	}
}

// The same gesture on ANOTHER key's Spotify preset is refused, the rule the
// app's own save path enforces (#836): the desktop app answers "already on
// key N" on the same press, and the speaker must not quietly do what the app
// just declined.
//
// Before the Spotify slot was recognised this could not even be judged: the
// candidate was built out of the proxy URL, so it did not look like the
// existing preset and the duplicate check waved it through, leaving the same
// playlist on two keys with one of them broken.
func TestHoldingAKeyRefusesAnotherKeysSpotifyPreset(t *testing.T) {
	store := newHoldTestStore(t)
	src := presets.Preset{
		Slot: 2,
		Name: "Regen fuer die Nacht",
		Type: "spotify",
		URI:  "spotify:playlist:2YGwyf6VcOavvyPdPiYJg2",
	}
	if err := store.SetSlot(src); err != nil {
		t.Fatal(err)
	}

	item := marge.HeldItem{
		Slot:     5,
		Source:   "LOCAL_INTERNET_RADIO",
		ItemName: src.Name,
		Location: nativeStationLocation(t, src.Name, "http://127.0.0.1:8888/spotify/stream-2.ogg"),
	}

	got, changed, err := heldPresetCandidate(store, item)
	if err == nil {
		t.Fatalf("holding a key over another key's playlist was accepted: %+v changed=%v", got, changed)
	}
	if changed {
		t.Fatal("a refused hold still reported a change")
	}
	// And the source key is untouched.
	if cur, _ := store.Get(2); cur.Type != "spotify" || cur.URI != src.URI {
		t.Fatalf("the source preset was damaged: %+v", cur)
	}
}

// nativeStationLocation builds the "/station?data=<base64 JSON>" descriptor the
// firmware reports for a station STR put on a key, so these tests exercise the
// same decode path the speaker's own frame goes through.
func nativeStationLocation(t *testing.T, name, streamURL string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"name": name, "streamUrl": streamURL, "isRealtime": true})
	if err != nil {
		t.Fatal(err)
	}
	return "/station?data=" + base64.RawURLEncoding.EncodeToString(b)
}

// newHoldTestStore is a preset store backed by a temp file, because SetSlot
// persists and refuses a store with no path.
func newHoldTestStore(t *testing.T) *presets.Store {
	t.Helper()
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatalf("presets.Load: %v", err)
	}
	return store
}
