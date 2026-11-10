package main

import (
	"reflect"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// TestResyncSafeSource pins the allowlist: input source names differ per model
// (CineMate says LOCAL where an ST10 says AUX, an SA-5 answers AUX1..AUX3), so
// anything we do not explicitly know must count as the user's choice and block
// the key-registration write, which would otherwise switch the box to UPNP.
func TestResyncSafeSource(t *testing.T) {
	for _, src := range []string{"UPNP", "INVALID_SOURCE"} {
		if !resyncSafeSource(src) {
			t.Errorf("%s must allow the re-sync (idle or our own source)", src)
		}
	}
	for _, src := range []string{
		"BLUETOOTH", "AUX", "AUX1", "LOCAL", "SPOTIFY", "DEEZER",
		"AIRPLAY", "TUNEIN", "STORED_MUSIC", "QPLAY", "PRODUCT", "HDMI_1",
		"STANDBY", "",
	} {
		if resyncSafeSource(src) {
			t.Errorf("%s is a user-chosen, idle-unknown or unknown source: the re-sync must not run", src)
		}
	}
}

// boxPresetEntries keeps handing the reconcile, the store recovery and the
// webui cache the spelling the regex reader produced: attribute values as the
// speaker's document escapes them, the name unescaped, keys 1 to 6 only.
func TestBoxPresetEntriesKeepTheLegacySpelling(t *testing.T) {
	const body = `<?xml version="1.0" encoding="UTF-8" ?>
<presets>
  <preset id="1" createdOn="1700000000" updatedOn="1700000001">
    <ContentItem source="UPNP" type="audio" location="http://192.0.2.1:8888/stream/raw?u=abc&amp;x=1" sourceAccount="UPnPUserName" isPresetable="true">
      <itemName>Pop &amp; Rock</itemName>
    </ContentItem>
  </preset>
  <preset id="2"><ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="/station?data=eyJuYW1lIjoiV0RSIDUifQ==" sourceAccount="" isPresetable="true"><itemName>WDR 5</itemName></ContentItem></preset>
  <preset id="3"><ContentItem source="DEEZER" type="tracklisturl" location="/playlist/1234?a=1&amp;b=2" sourceAccount="a&amp;b@example.com" isPresetable="true"><itemName>New York&apos;s Finest</itemName></ContentItem></preset>
  <preset id="4"><ContentItem source="SPOTIFY" type="uri" location="spotify:playlist:37i9dQZF1DXcBWIGoYBM5M" sourceAccount="spotify-user"><itemName>Playlist</itemName></ContentItem></preset>
  <preset id="5"></preset>
  <preset id="7"><ContentItem source="TUNEIN" location="/v1/playback/station/s1"><itemName>Not a key</itemName></ContentItem></preset>
</presets>`
	doc := boxapi.ParsePresets([]byte(body))
	if !doc.Parsed {
		t.Fatal("fixture is not well-formed")
	}
	want := []boxPresetEntry{
		{Slot: 1, Location: "http://192.0.2.1:8888/stream/raw?u=abc&amp;x=1", Name: "Pop & Rock", Source: "UPNP", Type: "audio", Account: "UPnPUserName"},
		{Slot: 2, Location: "/station?data=eyJuYW1lIjoiV0RSIDUifQ==", Name: "WDR 5", Source: "LOCAL_INTERNET_RADIO", Type: "stationurl"},
		{Slot: 3, Location: "/playlist/1234?a=1&amp;b=2", Name: "New York's Finest", Source: "DEEZER", Type: "tracklisturl", Account: "a&amp;b@example.com"},
		{Slot: 4, Location: "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M", Name: "Playlist", Source: "SPOTIFY", Type: "uri", Account: "spotify-user"},
		{Slot: 5},
	}
	got := boxPresetEntries(doc)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries:\n got  %+v\n want %+v", got, want)
	}

	// What the consumers of that spelling make of it: the unescape in
	// foreignpresets.NoteBoxList and nativeslots gives back the decoded value
	// the typed reader had, for every attribute.
	for i, p := range doc.Items[:4] {
		if xmlEntityUnescape(got[i].Location) != p.ContentItem.Location ||
			xmlEntityUnescape(got[i].Account) != p.ContentItem.SourceAccount {
			t.Errorf("slot %d: unescaping the entry does not give the decoded value back: %+v vs %+v", p.ID, got[i], p.ContentItem)
		}
	}

	// A document the decoder refuses (a bare & in a station URL) is salvaged
	// by the regexes and passed through as written, name unescaped: exactly
	// what the regex reader returned for it.
	const broken = `<presets><preset id="2"><ContentItem source="UPNP" type="audio" location="http://192.0.2.1:8888/stream/raw?u=abc&x=1" sourceAccount="a&amp;b"><itemName>Pop &amp; Rock</itemName></ContentItem></preset>`
	doc = boxapi.ParsePresets([]byte(broken))
	if doc.Parsed {
		t.Fatal("the broken fixture parsed")
	}
	wantBroken := []boxPresetEntry{{Slot: 2, Location: "http://192.0.2.1:8888/stream/raw?u=abc&x=1", Name: "Pop & Rock", Source: "UPNP", Type: "audio", Account: "a&amp;b"}}
	if got := boxPresetEntries(doc); !reflect.DeepEqual(got, wantBroken) {
		t.Errorf("salvaged entries:\n got  %+v\n want %+v", got, wantBroken)
	}

	// An empty list is no entries, not an error.
	if got := boxPresetEntries(boxapi.ParsePresets([]byte(`<presets />`))); len(got) != 0 {
		t.Errorf("empty list gave %+v", got)
	}
}

// The re-escape is the exact inverse of the unescape the consumers apply.
func TestBoxPresetAttrEscapeRoundTrips(t *testing.T) {
	for _, s := range []string{"", "plain", "a&b", `<"'>`, "http://192.0.2.1:8888/stream/raw?u=abc&x=1&y=2", "&amp;", "a&amp=1"} {
		if got := xmlEntityUnescape(boxPresetAttrEscape(s)); got != s {
			t.Errorf("round trip of %q gave %q", s, got)
		}
	}
}
