package presets

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// A single library track on a key keeps its object on the media server, and
// each track of a folder key its object id, so a server that renumbers its
// file URLs can be asked for the current ones (#919).
func TestTrackRefRoundTripsThroughTheStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := TrackRef{ServerUDN: "uuid:00000000-0000-0000-0000-000000000001", ObjectID: "obj-7", ParentID: "c-1", Title: "Song"}
	if err := s.SetSlot(Preset{Slot: 6, Name: "Song", Type: "radio", Source: "PC: Media",
		StreamURL: "http://192.0.2.10:10243/WMPNSSv4/1/x.mp3", Track: &want}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSlot(Preset{Slot: 2, Name: "Album", Type: "queue",
		Items: []PresetItem{{URL: "http://192.0.2.10:10243/WMPNSSv4/1/a.mp3", ObjectID: "a"}}}); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := again.Get(6)
	if got.Track == nil || *got.Track != want {
		t.Fatalf("reloaded track %+v, want %+v", got.Track, want)
	}
	q, _ := again.Get(2)
	if len(q.Items) != 1 || q.Items[0].ObjectID != "a" {
		t.Fatalf("folder track id lost: %+v", q.Items)
	}
	// A key without a ref writes no "track" key.
	b, _ := json.Marshal(Preset{Slot: 1, Name: "Radio", Type: "radio", StreamURL: "http://192.0.2.1/s"})
	if strings.Contains(string(b), `"track"`) || strings.Contains(string(b), `"object_id"`) {
		t.Fatalf("a preset without a ref writes one: %s", b)
	}
}

// A single library track on a key keeps the artist and album its media server
// named (#1839). A store written before the fields existed loads as it always
// did, and a key without them writes neither.
func TestArtistAndAlbumRoundTripThroughTheStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSlot(Preset{Slot: 6, Name: "Amanda", Type: "radio", Source: "MiniDLNA",
		StreamURL: "http://192.0.2.10:8200/MediaItems/42.flac", Artist: "Boston", Album: "Third Stage"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSlot(Preset{Slot: 1, Name: "1LIVE", Type: "radio", StreamURL: "http://example/1live"}); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := again.Get(6); got.Artist != "Boston" || got.Album != "Third Stage" {
		t.Fatalf("reloaded key %+v, want its artist and album", got)
	}
	station, _ := again.Get(1)
	b, err := json.Marshal(station)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"artist"`) || strings.Contains(string(b), `"album"`) {
		t.Errorf("a key without a label marshals the keys: %s", b)
	}

	old, err := parse([]byte(`{"presets":[{"slot":3,"name":"Song","type":"radio","source":"NAS",` +
		`"stream_url":"http://192.0.2.10:8200/MediaItems/7.mp3","duration_sec":200}]}`))
	if err != nil || len(old) != 1 {
		t.Fatalf("a store from before did not load: %v %+v", err, old)
	}
	if old[0].Artist != "" || old[0].Album != "" || old[0].DurationSec != 200 {
		t.Errorf("old key = %+v", old[0])
	}
}
