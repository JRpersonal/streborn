package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The copy-presets flow (CopyPresetsAcrossBoxes) decodes each source preset
// into the app-side Preset and PUTs it verbatim to the target. Every field
// the agent persists (internal/presets.Preset) must survive that round trip:
// the queue fields (shuffle, items) were once missing here, so a saved DLNA
// folder arrived at the target as an empty folder and was rejected with a
// nonsensical error, aborting the whole transfer.
func TestPresetRoundTripKeepsQueueFields(t *testing.T) {
	// Agent-shaped queue preset. The "gapless" key inside an item does not
	// exist in any current schema: it stands in for a future agent-side field
	// and must round-trip too (items are raw JSON on purpose).
	src := `{
		"slot": 3,
		"name": "Vinyl rips",
		"type": "queue",
		"art": "http://192.0.2.9:8200/art/folder.jpg",
		"source": "NAS Media",
		"shuffle": true,
		"folder": {"server_udn": "uuid:00000000-0000-0000-0000-000000000009", "container_id": "64$1"},
		"items": [
			{"url": "http://192.0.2.9:8200/a.flac", "title": "Track A", "mime": "audio/flac", "duration_sec": 241},
			{"url": "http://192.0.2.9:8200/b.flac", "title": "Track B", "gapless": true}
		]
	}`

	var p Preset
	if err := json.Unmarshal([]byte(src), &p); err != nil {
		t.Fatalf("decode agent preset: %v", err)
	}
	if !p.Shuffle {
		t.Errorf("shuffle flag dropped on decode")
	}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("re-encode preset: %v", err)
	}

	var want, got map[string]any
	if err := json.Unmarshal([]byte(src), &want); err != nil {
		t.Fatalf("parse source: %v", err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("parse round-tripped: %v", err)
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("field %q dropped in the round trip", k)
			continue
		}
		if !reflect.DeepEqual(w, g) {
			t.Errorf("field %q changed in the round trip: got %v, want %v", k, g, w)
		}
	}
}

// A single library track carries its media server and object id (#919), and
// a box-to-box copy must hand it on, or the copied key dies with the next
// renumbering of the server's file URLs.
func TestPresetRoundTripKeepsTheTrackRef(t *testing.T) {
	src := `{"slot":6,"name":"Song","stream_url":"http://192.0.2.9:10243/WMPNSSv4/1/x.mp3","type":"radio",` +
		`"source":"PC: Media","track":{"server_udn":"uuid:00000000-0000-0000-0000-000000000009","object_id":"obj-7","parent_id":"c-1","title":"Song"}}`
	var p Preset
	if err := json.Unmarshal([]byte(src), &p); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	_ = json.Unmarshal([]byte(src), &want)
	_ = json.Unmarshal(out, &got)
	if !reflect.DeepEqual(want["track"], got["track"]) {
		t.Fatalf("track ref changed in the round trip: got %v, want %v", got["track"], want["track"])
	}
}

// A non-queue preset must not grow spurious queue fields on the wire: the
// agent's queue-empty validation gate keys off type=queue, and old agents
// reject unknown null-ish payload shapes.
func TestPresetRoundTripOmitsQueueFieldsForRadio(t *testing.T) {
	p := Preset{Slot: 1, Name: "Radio X", StreamURL: "http://example.com/x.mp3", Type: "radio"}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := m["items"]; ok {
		t.Errorf("radio preset must not carry an items field, got %s", out)
	}
	if _, ok := m["shuffle"]; ok {
		t.Errorf("radio preset must not carry a shuffle field, got %s", out)
	}
}

// A single library track's artist and album (#1839) ride on its key. The copy
// to another speaker, the backup and a rename all decode the key into the
// app-side Preset and write it back, so both must survive that, and a key
// without them must not grow empty ones.
func TestPresetRoundTripKeepsArtistAndAlbum(t *testing.T) {
	src := `{"slot":6,"name":"Amanda","stream_url":"http://192.0.2.9:8200/MediaItems/42.flac","type":"radio",` +
		`"source":"MiniDLNA","duration_sec":256,"artist":"Boston","album":"Third Stage"}`
	var p Preset
	if err := json.Unmarshal([]byte(src), &p); err != nil {
		t.Fatal(err)
	}
	if p.Artist != "Boston" || p.Album != "Third Stage" {
		t.Fatalf("decoded key lost its label: %+v", p)
	}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	_ = json.Unmarshal([]byte(src), &want)
	_ = json.Unmarshal(out, &got)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("the key changed in the round trip: got %v, want %v", got, want)
	}

	bare, err := json.Marshal(Preset{Slot: 1, Name: "Radio X", StreamURL: "http://example.com/x.mp3", Type: "radio"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(bare, &m)
	for _, k := range []string{"artist", "album"} {
		if _, ok := m[k]; ok {
			t.Errorf("a key without a label carries %q: %s", k, bare)
		}
	}
}

// The Recently-played ring is redeclared in the app: a field missing here is
// dropped before the frontend sees it, which is how the MIME was lost (#817).
func TestRecentItemKeepsArtistAndAlbum(t *testing.T) {
	var it RecentItem
	if err := json.Unmarshal([]byte(`{"ts":"2026-11-11T10:00:00Z","source":"upnp","cardKey":"k","cardName":"Amanda",`+
		`"cardURL":"http://192.0.2.9:8200/MediaItems/42.flac","mime":"audio/flac","artist":"Boston","album":"Third Stage"}`), &it); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(it)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["artist"] != "Boston" || m["album"] != "Third Stage" {
		t.Fatalf("the app drops the card's label: %s", out)
	}
}
