package presets

import (
	"path/filepath"
	"testing"
)

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
