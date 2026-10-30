package webui

import (
	"strings"
	"testing"
)

// A Pandora or iHeartRadio key has no stream URL, only the station's location,
// and the speaker reports exactly that location as playing. The phone page
// matched keys by stream URL alone, so a tapped Pandora key went dark once the
// tap grace ran out and a key saved by a hold never lit (#1599, #1600).
func TestPhoneRemoteLightsTheNativeKey(t *testing.T) {
	start := strings.Index(indexHTML, "function nativeSlot(")
	if start < 0 {
		t.Fatal("nativeSlot is gone; if it moved, move this test with it")
	}
	fn := indexHTML[start:]
	fn = fn[:strings.Index(fn, "\n}\n")]
	for _, want := range []string{"p.type === 'native'", "p.native.location === loc", "return p.slot"} {
		if !strings.Contains(fn, want) {
			t.Errorf("nativeSlot lost %q: it must match a native key by its stored location", want)
		}
	}
	mp := indexHTML[strings.Index(indexHTML, "function markPlaying("):]
	mp = mp[:strings.Index(mp, "\n}\n")]
	if !strings.Contains(mp, "nativeSlot(loc, lastPresetList)") {
		t.Error("markPlaying does not light a native key")
	}
}
