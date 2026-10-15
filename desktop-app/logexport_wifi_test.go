package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The bundle carries each speaker's stored networks as tags hashed with the
// PC's salt, so the same network on two speakers carries the same tag and no
// name ever ships (support case 2026-10-14).
func TestBundleWiFiFrom_TagsSharedAcrossSpeakersNoNames(t *testing.T) {
	a := bundleWiFiFrom(`{"networks":[{"ssid":"HomeNet","current":true,"sources":["firmware"]},{"ssid":"Vodafone Homespot","open":true,"publicHotspot":true}]}`)
	b := bundleWiFiFrom(`{"networks":[{"ssid":"Vodafone Homespot","current":true,"open":true,"publicHotspot":true}]}`)
	if len(a) != 2 || len(b) != 1 {
		t.Fatalf("a=%v b=%v", a, b)
	}
	if a[1].Tag != b[0].Tag {
		t.Fatal("the same network on two speakers must carry the same tag")
	}
	if a[0].Tag == a[1].Tag || !strings.HasPrefix(a[0].Tag, "SSID#") {
		t.Fatalf("tags %q %q", a[0].Tag, a[1].Tag)
	}
	if !a[0].Current || !a[1].PublicHotspot || !a[1].Open {
		t.Fatalf("flags lost: %+v", a)
	}
	raw, _ := json.Marshal(a)
	for _, leak := range []string{"HomeNet", "Vodafone", "ssid\""} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("%q leaked into the bundle: %s", leak, raw)
		}
	}
	if bundleWiFiFrom("404 page not found") != nil || bundleWiFiFrom("") != nil {
		t.Fatal("an old agent's answer must leave the field empty")
	}
}
