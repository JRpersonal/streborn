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

// The speaker's own hold-to-store record carries no account (#1101). It is
// the same station as the key the app saved with the account.
func TestSameNativeItemWithoutAnAccount(t *testing.T) {
	saved := &NativeItem{Source: "PANDORA", Location: "1", SourceAccount: "listener@example.com"}
	held := &NativeItem{Source: "PANDORA", Location: "1"}
	if !SameNativeItem(saved, held) || !SameNativeItem(held, saved) {
		t.Fatal("an item without an account must match on service and station")
	}
	if SameNativeItem(saved, &NativeItem{Source: "PANDORA", Location: "1", SourceAccount: "other@example.com"}) {
		t.Fatal("two different accounts must not match")
	}
}

func TestResolveNativeAccount(t *testing.T) {
	listed := NativeSourceAccounts([]byte(`<sources deviceID="device-id-here">` +
		`<sourceItem source="PANDORA" sourceAccount="old@example.com" status="UNAVAILABLE">x</sourceItem>` +
		`<sourceItem source="PANDORA" sourceAccount="listener@example.com" status="READY">x</sourceItem>` +
		`<sourceItem source="IHEARTRADIO" sourceAccount="12345" status="READY">x</sourceItem>` +
		`<sourceItem source="LOCAL_INTERNET_RADIO" status="READY">x</sourceItem></sources>`))
	cases := []struct {
		item NativeItem
		want string
	}{
		// v1.0.5 stored the station name as the account: healed.
		{NativeItem{Source: "PANDORA", SourceAccount: "Chris Stapleton Radio"}, "listener@example.com"},
		// No account at all (the speaker's own store record): filled in.
		{NativeItem{Source: "PANDORA"}, "listener@example.com"},
		// An account the speaker lists is kept, even a non-READY one.
		{NativeItem{Source: "PANDORA", SourceAccount: "OLD@example.com"}, "OLD@example.com"},
		{NativeItem{Source: "IHEART", SourceAccount: "Country 102.5"}, "12345"},
		// Nothing listed for the service: the stored value stays.
		{NativeItem{Source: "DEEZER", SourceAccount: "kept"}, "kept"},
	}
	for _, c := range cases {
		if got := ResolveNativeAccount(c.item, listed); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.item, got, c.want)
		}
	}
	if len(NativeSourceAccounts([]byte("not xml"))) != 0 {
		t.Fatal("garbage must read as no accounts")
	}
}
