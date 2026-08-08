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

// A logo whose URL merely LOOKS undrawable is still handed to the proxy, which
// is fetching it anyway and can see what it really is. Judging this by
// extension here threw away real pictures: the icon service STR falls back to
// answers .ico URLs with PNG bytes, so Sunshine Live lost its logo while
// MANGORADIO, whose logo happens to end in .webp, kept one (reported
// 2026-08-07). What a display cannot draw is now decided in drawableImage.
func TestUndrawableLookingURLsStillReachTheProxy(t *testing.T) {
	for _, art := range []string{
		"https://icons.duckduckgo.com/ip3/www.sunshine-live.de.ico",
		"https://cdn.example/favicon.ico",
		"https://cdn.example/logo.svg|https://cdn.example/favicon.ico",
		"https://cdn.example/logo.SVG?v=2",
	} {
		loc := OrionStationLocation("http://box/stream/1", "Test", art)
		got, _ := decodeStation(t, loc)["imageUrl"].(string)
		if strings.HasSuffix(got, strLogoPath) {
			t.Errorf("art %q was replaced by the STR logo before anything looked at the image", art)
		}
		if !strings.Contains(got, artProxyPath+"?u=") {
			t.Errorf("art %q gave imageUrl %q, want it routed through the art proxy", art, got)
		}
	}
}
