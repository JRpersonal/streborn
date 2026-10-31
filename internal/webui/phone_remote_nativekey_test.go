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

// A stopped speaker still reports the station's location. The desktop app keeps
// that key dark (#1526); the phone page lit it from the location alone, so a
// group wake that left the main speaker stopped on a station lit its key here
// (#1629). The gate has to sit before the pending-press branch, so a key just
// pressed still lights at once.
func TestPhoneRemoteKeepsAStoppedSpeakersKeyDark(t *testing.T) {
	mp := indexHTML[strings.Index(indexHTML, "function markPlaying("):]
	mp = mp[:strings.Index(mp, "\n}\n")]
	if !strings.HasPrefix(mp, "function markPlaying(loc, queue, spKey, state)") {
		t.Fatal("markPlaying no longer takes the play state")
	}
	gate := strings.Index(mp, "if (state === 'STOP_STATE') n = 0;")
	if gate < 0 {
		t.Fatal("markPlaying lights a key on a stopped speaker")
	}
	if pend := strings.Index(mp, "if (pendingStart.slot)"); pend < 0 || pend < gate {
		t.Error("the STOP_STATE gate must come before the pending-press branch")
	}
}
