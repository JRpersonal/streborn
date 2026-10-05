package presets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativePresetRoundTripsThroughTheStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := NewNativePreset(1, NativeItem{
		Source: "pandora", SourceAccount: "listener@example.com", Location: "4071226281950183516",
		ItemType: "stationurl", ItemName: "Little Big Town Radio", ContainerArt: "https://example.com/a.jpg",
	}, "")
	if !ok {
		t.Fatal("NewNativePreset refused a Pandora item")
	}
	if err := s.SetSlot(p); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := again.Get(1)
	if !got.IsNative() || got.Type != TypeNative || got.Name != "Little Big Town Radio" || got.Source != "Pandora" ||
		got.Art != "https://example.com/a.jpg" {
		t.Fatalf("reloaded %+v", got)
	}
	if *got.Native != (NativeItem{Source: "PANDORA", SourceAccount: "listener@example.com", Location: "4071226281950183516",
		ItemType: "stationurl", ItemName: "Little Big Town Radio", ContainerArt: "https://example.com/a.jpg"}) {
		t.Fatalf("native item %+v", *got.Native)
	}
}

// A store written before native presets existed loads unchanged, and a radio
// preset never grows a "native" key on disk.
func TestNativeFieldIsBackwardCompatible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets.json")
	old := `{"presets":[{"slot":1,"name":"1LIVE","stream_url":"https://example.com/1live.mp3","type":"radio"}]}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.Get(1)
	if p.Native != nil || p.IsNative() {
		t.Fatalf("old radio preset reads as native: %+v", p)
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "native") {
		t.Fatalf("radio preset serialises a native field: %s", b)
	}
	// type native without an item is not usable.
	if (Preset{Type: TypeNative}).IsNative() {
		t.Fatal("a native type without an item must not count as native")
	}
}

func TestNativeServiceLabelAndSameItem(t *testing.T) {
	for src, want := range map[string]string{"PANDORA": "Pandora", "iheart": "iHeartRadio", "IHEARTRADIO": "iHeartRadio"} {
		if got, ok := NativeServiceLabel(src); !ok || got != want {
			t.Errorf("%s -> %q/%v", src, got, ok)
		}
	}
	for _, src := range []string{"DEEZER", "LOCAL_INTERNET_RADIO", "SPOTIFY", ""} {
		if _, ok := NativeServiceLabel(src); ok {
			t.Errorf("%s must not be a native service", src)
		}
	}
	a := &NativeItem{Source: "PANDORA", Location: "1", SourceAccount: "listener@example.com", ItemName: "A"}
	b := &NativeItem{Source: "pandora", Location: "1", SourceAccount: "LISTENER@example.com", ItemName: "renamed"}
	if !SameNativeItem(a, b) {
		t.Error("same station, different name: must be the same item")
	}
	if SameNativeItem(a, &NativeItem{Source: "PANDORA", Location: "2", SourceAccount: "listener@example.com"}) {
		t.Error("different location must differ")
	}
	if _, ok := NewNativePreset(1, NativeItem{Source: "PANDORA"}, "x"); ok {
		t.Error("an item without a location must be refused")
	}
}
