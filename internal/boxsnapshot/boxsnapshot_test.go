package boxsnapshot

import "testing"

func TestParseSources(t *testing.T) {
	// Sanitised from a live ST10 /sources dump (placeholder device ID).
	const body = `<?xml version="1.0" encoding="UTF-8"?>
<sources deviceID="AABBCCDDEEFF">
<sourceItem source="AUX" sourceAccount="AUX" status="READY" isLocal="true" multiroomallowed="true">AUX IN</sourceItem>
<sourceItem source="DEEZER" sourceAccount="1456373802" status="READY" isLocal="false" multiroomallowed="true">DeezerUser</sourceItem>
<sourceItem source="SPOTIFY" sourceAccount="SpotifyConnectUserName" status="UNAVAILABLE" isLocal="false" multiroomallowed="true">SpotifyConnectUserName</sourceItem>
</sources>`
	dev, sources, err := parseSources([]byte(body))
	if err != nil {
		t.Fatalf("parseSources: %v", err)
	}
	if dev != "AABBCCDDEEFF" {
		t.Errorf("deviceID = %q, want AABBCCDDEEFF", dev)
	}
	if len(sources) != 3 {
		t.Fatalf("got %d sources, want 3", len(sources))
	}
	if sources[1].Source != "DEEZER" || sources[1].SourceAccount != "1456373802" {
		t.Errorf("deezer source mis-parsed: %+v", sources[1])
	}
}

func TestParsePresets(t *testing.T) {
	const body = `<presets>
<preset id="1"><ContentItem source="UPNP" type="audio" location="http://127.0.0.1:8888/stream/1" sourceAccount="UPnPUserName" isPresetable="true"><itemName>1LIVE</itemName></ContentItem></preset>
<preset id="3"><ContentItem source="DEEZER" type="tracklisturl" location="/playlist/1234" sourceAccount="1456373802" isPresetable="true"><itemName>My Playlist</itemName></ContentItem></preset>
</presets>`
	presets, err := parsePresets([]byte(body))
	if err != nil {
		t.Fatalf("parsePresets: %v", err)
	}
	if len(presets) != 2 {
		t.Fatalf("got %d presets, want 2", len(presets))
	}
	if presets[0].Slot != 1 || presets[0].Name != "1LIVE" || presets[0].Source != "UPNP" {
		t.Errorf("preset 1 mis-parsed: %+v", presets[0])
	}
	if presets[1].Slot != 3 || presets[1].Source != "DEEZER" || presets[1].Name != "My Playlist" {
		t.Errorf("preset 3 mis-parsed: %+v", presets[1])
	}
}

func TestAnalyze(t *testing.T) {
	presets := []Preset{
		{Slot: 1, Source: "UPNP", Name: "1LIVE"},
		{Slot: 3, Source: "DEEZER", Name: "Playlist A"},
		{Slot: 4, Source: "DEEZER", Name: "Playlist B"},
	}
	sources := []Source{
		{Source: "AUX", Status: "READY"},
		{Source: "DEEZER", Status: "READY", SourceAccount: "1456373802"},
		{Source: "AMAZON", Status: "UNAVAILABLE"}, // unavailable -> not flagged via sources
		{Source: "SPOTIFY", Status: "UNAVAILABLE"},
	}
	services, lost := analyze(presets, sources)
	if len(services) != 1 || services[0] != "DEEZER" {
		t.Errorf("services = %v, want [DEEZER]", services)
	}
	if len(lost) != 2 {
		t.Errorf("lostPresets = %d, want 2 (slots 3,4)", len(lost))
	}
}

func TestIsCloudService(t *testing.T) {
	cases := map[string]bool{
		"DEEZER":      true,
		"DEEZER_HIFI": true,
		"deezer":      true,
		"AMAZON":      true,
		"UPNP":        false,
		"SPOTIFY":     false, // STR serves Spotify itself, never "lost"
		"AUX":         false,
		"":            false,
	}
	for in, want := range cases {
		if got := isCloudService(in); got != want {
			t.Errorf("isCloudService(%q) = %v, want %v", in, got, want)
		}
	}
}
