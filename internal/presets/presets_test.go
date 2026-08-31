package presets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

// makeItems builds n distinct queue items for the cap tests.
func makeItems(n int) []PresetItem {
	out := make([]PresetItem, n)
	for i := range out {
		out[i] = PresetItem{URL: "http://nas/" + strconv.Itoa(i) + ".mp3"}
	}
	return out
}

// A queue preset larger than MaxQueueItems must be trimmed on save, so one saved
// library folder cannot fill the box's flash and strand the next OTA. The kept
// slice must be the FIRST MaxQueueItems items, and other preset types untouched.
func TestSetSlotCapsQueueItems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	items := makeItems(MaxQueueItems + 600)
	if err := s.SetSlot(Preset{Slot: 3, Name: "Whole Library", Type: "queue", Items: items}); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}
	got, _ := s.Get(3)
	if len(got.Items) != MaxQueueItems {
		t.Fatalf("queue not capped: kept %d, want %d", len(got.Items), MaxQueueItems)
	}
	if got.Items[0].URL != "http://nas/0.mp3" || got.Items[MaxQueueItems-1].URL != "http://nas/"+strconv.Itoa(MaxQueueItems-1)+".mp3" {
		t.Errorf("cap dropped the wrong end: first=%s last=%s", got.Items[0].URL, got.Items[MaxQueueItems-1].URL)
	}
	// A radio preset carries no Items and must be unaffected.
	if err := s.SetSlot(Preset{Slot: 1, Name: "NDR2", Type: "radio", StreamURL: "http://x/ndr2.mp3"}); err != nil {
		t.Fatalf("SetSlot radio: %v", err)
	}
	if r, _ := s.Get(1); len(r.Items) != 0 {
		t.Errorf("radio preset gained items: %+v", r.Items)
	}
}

// A presets.json written before the cap existed (a whole-library queue preset)
// must be trimmed on Load and the smaller file rewritten, so the box reclaims the
// flash on the next agent start without the user re-saving anything.
func TestLoadHealsOverCapQueue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "presets.json")
	// Seed an over-cap store directly on disk via a first Store that skips the cap
	// (write the raw JSON so the fixture mirrors a pre-cap file).
	big := struct {
		Presets []Preset `json:"presets"`
	}{Presets: []Preset{{Slot: 3, Name: "All Music", Type: "queue", Items: makeItems(MaxQueueItems + 1600)}}}
	raw, _ := json.MarshalIndent(big, "", "  ")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	sizeBefore := len(raw)

	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := s.Get(3)
	if !ok || len(got.Items) != MaxQueueItems {
		t.Fatalf("over-cap queue not healed on load: kept %d, want %d (ok=%v)", len(got.Items), MaxQueueItems, ok)
	}
	// The on-disk file must have shrunk (the heal re-saved the trimmed store).
	b, _ := os.ReadFile(path)
	if len(b) >= sizeBefore {
		t.Errorf("healed file not rewritten smaller: before=%d after=%d", sizeBefore, len(b))
	}
	// A fresh Load must now be a no-op heal (already at the cap).
	s2, err := Load(path)
	if err != nil {
		t.Fatalf("Load(reload): %v", err)
	}
	if g2, _ := s2.Get(3); len(g2.Items) != MaxQueueItems {
		t.Errorf("second load changed the count: %d", len(g2.Items))
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
