package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// A US speaker playing a Pandora station. The ContentItem attributes are the
// shape every other source reports in /now_playing; Pandora's own values were
// never observed (#1101), so only the field mapping is pinned here.
const pandoraNowPlaying = `<?xml version="1.0" encoding="UTF-8" ?>
<nowPlaying deviceID="device-id-here" source="PANDORA" sourceAccount="listener@example.com">
  <ContentItem source="PANDORA" type="stationurl" location="4071226281950183516" sourceAccount="listener@example.com" isPresetable="true">
    <itemName>Little Big Town Radio</itemName>
    <containerArt>https://example.com/art.jpg</containerArt>
  </ContentItem>
  <track>Boondocks</track><artist>Little Big Town</artist>
  <stationName>Little Big Town Radio</stationName>
  <playStatus>PLAY_STATE</playStatus>
</nowPlaying>`

func TestParseNowPlayingNativeItem(t *testing.T) {
	item, ok := parseNowPlayingNativeItem([]byte(pandoraNowPlaying))
	if !ok {
		t.Fatal("Pandora now-playing not recognised")
	}
	want := presets.NativeItem{Source: "PANDORA", SourceAccount: "listener@example.com", Location: "4071226281950183516",
		ItemType: "stationurl", ItemName: "Little Big Town Radio", ContainerArt: "https://example.com/art.jpg"}
	if item != want {
		t.Fatalf("got %+v, want %+v", item, want)
	}
	radio := `<nowPlaying source="LOCAL_INTERNET_RADIO"><ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="/station?data=x"/></nowPlaying>`
	if _, ok := parseNowPlayingNativeItem([]byte(radio)); ok {
		t.Fatal("a radio station must not be taken as a native-service item")
	}
	if _, ok := parseNowPlayingNativeItem([]byte(`<nowPlaying source="PANDORA"><ContentItem source="PANDORA"/></nowPlaying>`)); ok {
		t.Fatal("an item without a location must be refused")
	}
}

func TestValidNativeItem(t *testing.T) {
	good := &presets.NativeItem{Source: "IHEART", Location: "live:1234", ItemType: "stationurl"}
	if err := validNativeItem(good); err != nil {
		t.Fatalf("good item refused: %v", err)
	}
	for name, bad := range map[string]*presets.NativeItem{
		"nil":            nil,
		"unknown source": {Source: "DEEZER", Location: "x"},
		"no location":    {Source: "PANDORA"},
		"space":          {Source: "PANDORA", Location: "a b"},
		"quote":          {Source: "PANDORA", Location: `a"b`},
		"markup account": {Source: "PANDORA", Location: "1", SourceAccount: "<x>"},
	} {
		if validNativeItem(bad) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNativeContentItemXMLEscapes(t *testing.T) {
	got := nativeContentItemXML(presets.NativeItem{Source: "PANDORA", ItemType: "stationurl", Location: "1&2",
		SourceAccount: "listener@example.com"}, `Rock & "Roll"`)
	want := `<ContentItem source="PANDORA" type="stationurl" location="1&amp;2" sourceAccount="listener@example.com" isPresetable="true"><itemName>Rock &amp; "Roll"</itemName></ContentItem>`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

const pandoraPresetBody = `{"name":"Country","type":"native","native":{"source":"PANDORA","sourceAccount":"listener@example.com","location":"4071226281950183516","itemType":"stationurl","itemName":"Little Big Town Radio"}}`

// A box-to-box copy carries the item: it is stored as a native preset with
// the service label as its badge, and the same station on another key is
// refused like any other duplicate.
func TestPresetPutStoresANativeItem(t *testing.T) {
	s := newMoveServer(t)
	rec := putPreset(t, s, 2, pandoraPresetBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := s.presets.Get(2)
	if !got.IsNative() || got.Name != "Country" || got.Source != "Pandora" || got.Native.Location != "4071226281950183516" {
		t.Fatalf("stored %+v", got)
	}
	dup := putPreset(t, s, 4, pandoraPresetBody)
	if dup.Code != http.StatusConflict {
		t.Fatalf("duplicate: status %d, want 409", dup.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(dup.Body.Bytes(), &body)
	if body["code"] != "already-on-slot" || body["slot"] != float64(2) {
		t.Fatalf("duplicate answer %v", body)
	}
}

// The apps' hold gesture sends no item; without a speaker to read it from the
// save is refused, never stored half-empty.
func TestPresetPutNativeWithoutItemNeedsTheSpeaker(t *testing.T) {
	s := newMoveServer(t)
	rec := putPreset(t, s, 2, `{"name":"x","type":"native"}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "native-nowplaying-unreadable") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := s.presets.Get(2); ok {
		t.Fatal("stored without an item")
	}
	bad := putPreset(t, s, 2, `{"name":"x","type":"native","native":{"source":"DEEZER","location":"1"}}`)
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown source: status %d", bad.Code)
	}
}

func TestBulkPutCarriesNativePresets(t *testing.T) {
	s := newMoveServer(t)
	body := `[` + strings.Replace(pandoraPresetBody, `{"name"`, `{"slot":3,"name"`, 1) +
		`,{"slot":1,"name":"1LIVE","type":"radio","stream_url":"https://example.com/1live.mp3"}]`
	rec := httptest.NewRecorder()
	s.handlePresetsBulkPut(rec, httptest.NewRequest(http.MethodPut, "/api/presets", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := s.presets.Get(3); !got.IsNative() || got.Source != "Pandora" {
		t.Fatalf("slot 3 = %+v", got)
	}
}
