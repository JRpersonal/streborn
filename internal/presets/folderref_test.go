package presets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A folder key keeps the folder's identity apart from its file paths, so a
// server that changes every path (a USB stick swapped on a router's media
// server) can later be browsed again by id.
func TestFolderRefRoundTripsThroughTheStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := FolderRef{ServerUDN: "uuid:00000000-0000-0000-0000-000000000001", ContainerID: "64$1$2"}
	if err := s.SetSlot(Preset{Slot: 1, Name: "All tracks", Type: "queue", Folder: &want,
		Items: []PresetItem{{URL: "http://192.0.2.10:49200/AUDIO/DLNA-1-0/a.mp3"}}}); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := again.Get(1)
	if got.Folder == nil || *got.Folder != want {
		t.Fatalf("reloaded folder %+v, want %+v", got.Folder, want)
	}
}

// Backward compatible both ways: a store written before the field loads with
// no folder, and a preset without one writes no "folder" key at all.
func TestFolderRefIsOptional(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets.json")
	old := `{"presets":[{"slot":2,"name":"Old key","type":"queue","items":[{"url":"http://192.0.2.10:49200/x.mp3"}]}]}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(2)
	if !ok || got.Folder != nil || len(got.Items) != 1 {
		t.Fatalf("old store loaded as %+v", got)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"folder"`) {
		t.Fatalf("a preset without a folder writes one: %s", b)
	}
}
