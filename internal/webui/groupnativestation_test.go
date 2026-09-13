package webui

import (
	"strings"
	"testing"
)

// Eileen Wilson's three grouped SoundTouch 10s, 2026-09-13. She pressed key 6
// in the app and got a solid amber LED and silence on all three; the same key
// played twenty seconds after the group was undone.
//
// The recall went to the speaker that holds "Exclusively Rush" on key 6. That
// speaker was a follower, so the firmware handed the content item to the master
// to play, and the location in it named the station by slot:
// http://127.0.0.1:8888/stream/6. On the master, 127.0.0.1 is the master, and
// its key 6 was empty. Its own proxy answered "box fetched a slot with no
// playable preset slot=6 found=false" and 404ed, and the box reported
// AUDIO_ERROR_BAD_URL.
//
// A slot number is not a name another speaker can resolve. The follower's own
// LAN address would not do either, because two rhino ST10s cannot reach each
// other's web port at all. So the station itself travels in the URL.
func TestAGroupFollowersStationCarriesTheStreamNotTheSlot(t *testing.T) {
	const (
		slotURL = "http://127.0.0.1:8888/stream/6"
		stream  = "https://exclusive.example/rush"
	)

	if got := nativeStationURLFor(slotURL, stream, false); got != slotURL {
		t.Errorf("standalone speaker: got %q, want the slot form %q", got, slotURL)
	}

	got := nativeStationURLFor(slotURL, stream, true)
	if got == slotURL {
		t.Fatal("a follower still named the slot, which only the master can resolve")
	}
	if !strings.Contains(got, "/stream/raw?u=") {
		t.Fatalf("follower URL = %q, want the raw form that carries the station", got)
	}

	// The marge keeper and the reconcile both read a native location back to
	// decide what a slot holds. If the raw form did not unwind to the origin
	// stream, a grouped recall would quietly rewrite the user's preset.
	if back := unwrapRawStreamProxy(got); back != stream {
		t.Errorf("unwrapRawStreamProxy(%q) = %q, want the origin %q", got, back, stream)
	}
}

// A preset with no stream URL has nothing to put in the raw form. Better to
// leave the recall exactly as it was than to invent a URL for it.
func TestAStationWithNoStreamKeepsTheSlotForm(t *testing.T) {
	const slotURL = "http://127.0.0.1:8888/stream/2"
	for _, stream := range []string{"", "   "} {
		if got := nativeStationURLFor(slotURL, stream, true); got != slotURL {
			t.Errorf("stream %q: got %q, want %q", stream, got, slotURL)
		}
	}
}
