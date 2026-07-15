package presets

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A primary presets.json truncated to 0 bytes (the overnight-standby power-cut
// loss) must be recovered from the durable backup on the next Load, and the
// primary rewritten from it.
func TestBackupRecoversZeroedPrimary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load(new): %v", err)
	}
	if err := s.SetSlot(Preset{Slot: 1, Name: "NDR2", Type: "radio", StreamURL: "http://example/ndr2.mp3"}); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}
	// The save must have produced a durable backup.
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("backup not written: %v", err)
	}
	// Simulate the power-cut loss: primary is now 0 bytes.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load(after zeroing): %v", err)
	}
	got, ok := reloaded.Get(1)
	if !ok || got.Name != "NDR2" || got.StreamURL != "http://example/ndr2.mp3" {
		t.Fatalf("preset not recovered from backup: %+v ok=%v", got, ok)
	}
	// The primary must have been rewritten (non-empty) so the box is whole again.
	if b, _ := os.ReadFile(path); len(b) == 0 {
		t.Fatal("primary was not restored from backup")
	}
}

// An explicit empty preset set ({"presets":null}) is a valid state and must NOT
// be overridden by the backup, or clearing presets would be impossible.
func TestExplicitEmptyNotRecovered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "presets.json")
	// A backup exists with content...
	if err := os.WriteFile(path+".bak", []byte(`{"presets":[{"slot":1,"name":"Old"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// ...but the primary is a deliberate empty set, not a 0-byte loss.
	if err := os.WriteFile(path, []byte(`{"presets":null}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if n := len(s.All()); n != 0 {
		t.Fatalf("explicit empty set was overridden by backup: %d presets", n)
	}
}

// normalize must carry the Spotify fields (Type/URI/Account) through, and
// keep defaulting Type to "radio" for legacy entries that omit it.
func TestNormalizeSpotifyFields(t *testing.T) {
	out := normalize([]rawPreset{
		{Slot: 6, Name: "Jens Chill", Type: "spotify", URI: "spotify:playlist:0DpRrxVcm2yvD3iEW1kH5E", Account: "jensukk"},
		{Slot: 1, Name: "1LIVE", URL: "http://example/stream.mp3"}, // legacy: no type, url alias
	})
	if len(out) != 2 {
		t.Fatalf("want 2 presets, got %d", len(out))
	}
	sp := out[0]
	if sp.Type != "spotify" || sp.URI != "spotify:playlist:0DpRrxVcm2yvD3iEW1kH5E" || sp.Account != "jensukk" {
		t.Errorf("spotify preset not mapped: %+v", sp)
	}
	radio := out[1]
	if radio.Type != "radio" || radio.StreamURL != "http://example/stream.mp3" {
		t.Errorf("legacy radio preset not mapped: %+v", radio)
	}
}

// A Spotify preset must survive a Save -> Load round trip with its URI and
// Account intact (the persisted on-NAND format the desktop app reads back).
func TestSaveLoadSpotifyRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load(new): %v", err)
	}
	want := Preset{Slot: 6, Name: "Jens Chill", Type: "spotify", URI: "spotify:playlist:0DpRrxVcm2yvD3iEW1kH5E", Account: "jensukk", Art: "https://i.scdn.co/image/x"}
	if err := s.SetSlot(want); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load(reload): %v", err)
	}
	got, ok := reloaded.Get(6)
	if !ok {
		t.Fatal("slot 6 missing after reload")
	}
	if got.Type != "spotify" || got.URI != want.URI || got.Account != want.Account || got.Name != want.Name {
		t.Errorf("round trip lost fields: %+v", got)
	}
}

// A queue preset (a saved DLNA folder) must survive Save -> Load with its
// Shuffle flag and the full ordered Items list intact, so a recall restarts the
// same folder.
func TestSaveLoadQueueRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load(new): %v", err)
	}
	want := Preset{
		Slot: 3, Name: "Jazz Folder", Type: "queue", Shuffle: true,
		Source: "Living Room NAS",
		Items: []PresetItem{
			{URL: "http://nas/1.flac", Title: "One", Art: "http://nas/1.jpg", Mime: "audio/flac", DurationSec: 210},
			{URL: "http://nas/2.mp3", Title: "Two", Mime: "audio/mpeg", DurationSec: 0},
		},
	}
	if err := s.SetSlot(want); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load(reload): %v", err)
	}
	got, ok := reloaded.Get(3)
	if !ok {
		t.Fatal("slot 3 missing after reload")
	}
	if got.Type != "queue" || !got.Shuffle || got.Name != want.Name || got.Source != want.Source {
		t.Errorf("round trip lost scalar fields: %+v", got)
	}
	if !reflect.DeepEqual(got.Items, want.Items) {
		t.Errorf("round trip lost items:\n got  %+v\n want %+v", got.Items, want.Items)
	}
}
