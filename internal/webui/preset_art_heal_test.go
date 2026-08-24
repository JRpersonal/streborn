package webui

import (
	"encoding/base64"
	"strings"
	"testing"
)

// The #696 shape, verbatim from the reporter's bundle: a hold-to-save in the
// v0.9.56 desktop app copied the playing ORION descriptor's imageUrl into the
// preset art. That imageUrl is the loopback art-proxy wrapper stationImageURL
// builds for the SPEAKER's display, so from the user's machine the art is a
// dead URL and the tile ends on the grey-chevron icon-service placeholder.
// The store must hold the ORIGIN image URL instead; the box side re-wraps at
// write time, so the display loses nothing.

// wrapLoopbackArt builds the exact wrapper the agent writes into a descriptor
// (ArtProxyURL with the loopback authority), so the tests exercise the real
// shape rather than a hand-typed imitation.
func wrapLoopbackArt(imageURL string) string {
	return "http://127.0.0.1:8888/art?u=" +
		base64.RawURLEncoding.EncodeToString([]byte(imageURL))
}

func TestHealSelfArtProxy(t *testing.T) {
	const origin = "https://icons.duckduckgo.com/ip3/epic-classical.com.ico"
	cases := []struct {
		name, in, want string
	}{
		{"unwraps the loopback wrapper (#696 bundle, slot 5)",
			wrapLoopbackArt(origin), origin},
		{"unwraps a LAN-addressed wrapper too (dies with the next DHCP lease)",
			"http://192.0.2.60:8888/art?u=" + base64.RawURLEncoding.EncodeToString([]byte(origin)), origin},
		{"drops the /icon.png stand-in, STR's logo is not the station's art",
			"http://127.0.0.1:8888/icon.png", ""},
		{"drops a broken wrapper instead of storing a dead URL",
			"http://127.0.0.1:8888/art?u=%%%not-base64", ""},
		{"a clean chain passes byte-identical",
			"https://static.example/logo.png|https://icons.duckduckgo.com/ip3/example.com.ico",
			"https://static.example/logo.png|https://icons.duckduckgo.com/ip3/example.com.ico"},
		{"heals only the wrapped candidate inside a mixed chain",
			"https://static.example/logo.png|" + wrapLoopbackArt(origin),
			"https://static.example/logo.png|" + origin},
		{"empty stays empty", "", ""},
		{"a data: URI is not ours to judge",
			"data:image/svg+xml;utf8,x", "data:image/svg+xml;utf8,x"},
	}
	for _, c := range cases {
		if got := healSelfArtProxy(c.in); got != c.want {
			t.Errorf("%s: healSelfArtProxy(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// The heal must actually sit on the save path: a PUT carrying the poisoned
// art (what a v0.9.56 app sends on hold-to-save) stores the origin image URL.
// This is the request-level regression for #696; it failed before the heal
// was added to handlePresetSlot.
func TestPresetSaveHealsBoxLoopbackArt(t *testing.T) {
	s := newSaveGateServer(t)
	art := wrapLoopbackArt("https://cdn-profiles.tunein.com/s74982/images/logod.png")
	w := putPreset(t, s, 5,
		`{"name":"EPIC CLASSICAL - Classical Guitar","type":"radio",`+
			`"stream_url":"https://stream.epic-classical.example/classical-guitar",`+
			`"art":"`+art+`"}`)
	if w.Code != 200 {
		t.Fatalf("save with wrapped art: status = %d (body %s)", w.Code, w.Body.String())
	}
	p, ok := s.presets.Get(5)
	if !ok {
		t.Fatal("preset was not stored")
	}
	if p.Art != "https://cdn-profiles.tunein.com/s74982/images/logod.png" {
		t.Errorf("stored art = %q, want the unwrapped origin URL", p.Art)
	}
	if strings.Contains(p.Art, "/art?u=") || strings.Contains(p.Art, "127.0.0.1") {
		t.Errorf("stored art still points at the agent: %q", p.Art)
	}
}
