package main

import (
	"testing"

	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/webui"
)

// A station saved with the speaker's own hold gets the logo chain STR knows for
// that stream, the one an app save stores, not the descriptor's single picture
// (#968). Without a known chain the descriptor's picture stays.
func TestHeldStationTakesTheKnownArtChain(t *testing.T) {
	const origin = "https://stream.example.com/jazz/mp3-128/"
	const chain = "https://example.com/jazz-logo.svg|https://example.com/jazz-logo.png|https://example.com/favicon.ico"
	loc := webui.OrionStationLocation(boxurl.RawStream(origin), "Jazz FM", "https://example.com/jazz-logo.png")

	store := holdTestStore(t)
	lookup := func(u string) string {
		if u == origin {
			return chain
		}
		return ""
	}
	got, changed, err := heldPresetCandidate(store, heldLive{artChain: lookup}, heldRadio(3, loc, "Jazz FM"))
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if got.Art != chain {
		t.Fatalf("Art = %q, want the full chain", got.Art)
	}

	got, _, err = heldPresetCandidate(store, heldLive{artChain: func(string) string { return "" }}, heldRadio(3, loc, "Jazz FM"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Art != "https://example.com/jazz-logo.png" {
		t.Fatalf("fallback Art = %q, want the descriptor picture", got.Art)
	}
}
