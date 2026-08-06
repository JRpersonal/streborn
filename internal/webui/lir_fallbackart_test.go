package webui

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// decodeStation pulls the JSON back out of an ORION station location.
func decodeStation(t *testing.T, loc string) map[string]any {
	t.Helper()
	const p = "/station?data="
	if !strings.HasPrefix(loc, p) {
		t.Fatalf("location does not look like a station: %q", loc)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(loc, p))
	if err != nil {
		t.Fatalf("station payload is not base64: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("station payload is not JSON: %v", err)
	}
	return out
}

// A station with no logo must not leave the speaker's display blank. Live
// payload from a Portable, 2026-08-06: 1LIVE carried imageUrl:"" while WDR 5 on
// the next slot had its own icon, so the empty case is the common one, not an
// edge case.
func TestStationWithoutLogoFallsBackToTheSTRLogo(t *testing.T) {
	for _, art := range []string{"", "   ", "|", " | "} {
		loc := OrionStationLocation("http://box/stream/2", "1LIVE", art)
		got, _ := decodeStation(t, loc)["imageUrl"].(string)
		if !strings.HasSuffix(got, strLogoPath) {
			t.Errorf("art %q gave imageUrl %q, want the STR logo", art, got)
		}
		if !strings.HasPrefix(got, "http://") {
			t.Errorf("imageUrl %q is not plain http; the firmware cannot fetch https", got)
		}
	}
}

// A real logo must keep its place. Replacing a working picture with our own
// would be a worse bug than the blank one this fixes.
func TestStationWithARealLogoKeepsIt(t *testing.T) {
	cases := []string{
		"https://www1.wdr.de/img/apple-touch-icon.png",
		"https://cdn.example/logo.jpg",
		"https://cdn.example/logo.png?v=3",
		// No extension at all: unknown, but plenty of drawable logos look like
		// this, so it must NOT be replaced.
		"https://cdn.example/station/logo",
		// The chain's first entry is undrawable but a raster one follows.
		"https://cdn.example/logo.svg|https://cdn.example/logo.png",
	}
	for _, art := range cases {
		loc := OrionStationLocation("http://box/stream/1", "Test", art)
		got, _ := decodeStation(t, loc)["imageUrl"].(string)
		if strings.HasSuffix(got, strLogoPath) {
			t.Errorf("art %q was replaced by the STR logo, want the station's own", art)
		}
		if got == "" {
			t.Errorf("art %q produced an empty imageUrl", art)
		}
	}
}

// A chain made only of formats a display cannot draw is the same blank screen
// as no logo at all, so it gets the same treatment.
func TestStationWithOnlyUndrawableLogosFallsBack(t *testing.T) {
	for _, art := range []string{
		"https://cdn.example/logo.svg",
		"https://cdn.example/favicon.ico",
		"https://cdn.example/logo.svg|https://cdn.example/favicon.ico",
		"https://cdn.example/logo.SVG?v=2",
	} {
		loc := OrionStationLocation("http://box/stream/1", "Test", art)
		got, _ := decodeStation(t, loc)["imageUrl"].(string)
		if !strings.HasSuffix(got, strLogoPath) {
			t.Errorf("art %q gave imageUrl %q, want the STR logo", art, got)
		}
	}
}

func TestOnlyUndrawableArt(t *testing.T) {
	cases := []struct {
		art  string
		want bool
	}{
		{"", false}, // nothing at all is handled by the empty path, not here
		{"https://x/logo.svg", true},
		{"https://x/logo.ico", true},
		{"https://x/logo.png", false},
		{"https://x/logo", false},
		{"https://x/a.svg|https://x/b.png", false},
		{"https://x/a.svg|https://x/b.ico", true},
	}
	for _, tc := range cases {
		if got := onlyUndrawableArt(tc.art); got != tc.want {
			t.Errorf("onlyUndrawableArt(%q) = %v, want %v", tc.art, got, tc.want)
		}
	}
}
