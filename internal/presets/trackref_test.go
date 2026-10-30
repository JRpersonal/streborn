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
