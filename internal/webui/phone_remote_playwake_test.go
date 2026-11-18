package webui

import (
	"strings"
	"testing"
)

// Discussion #1920: the Play key of the Bose remote starts a speaker with
// what it played last. The phone page's one Play/Pause button sent
// /api/resume on a speaker in standby, which reaches a transport with nothing
// to resume, so the press did nothing. It now takes the power button's "on"
// there (/api/box/power wakes the speaker and puts the last item back, see
// handleBoxPower), with no second button and without touching the volume.
func TestPhoneRemotePlayWakesASpeakerWithNothingLoaded(t *testing.T) {
	page := strings.ReplaceAll(indexHTML, "\r\n", "\n")
	for _, want := range []string{
		// Standby and "nothing loaded" are told from the source, before the
		// play state: a speaker in standby reports STOP_STATE.
		"if (up === 'STANDBY' || up === 'INVALID_SOURCE') return 'wake';",
		// The button offers Play there, from every status poll.
		"ppAction = playButtonAction(state, src);\n  paused = ppAction !== 'pause';",
		"applyTransportUI(state, src);",
		// The press is the power-on that resumes, once at a time.
		"if (ppAction === 'wake') {",
		"try { await api('/api/box/power', 'POST', { on: true }); } finally { ppBusy = false; }",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the phone page lost a part of Play on a sleeping speaker: %q", want)
		}
	}
	if n := strings.Count(page, `onclick="togglePlayPause(this)"`); n != 1 {
		t.Errorf("the transport must keep exactly one Play/Pause button, found %d", n)
	}
	at := strings.Index(page, "async function togglePlayPause(")
	if at < 0 {
		t.Fatal("togglePlayPause not found on the phone page")
	}
	end := strings.Index(page[at:], "\n}\n")
	if end < 0 {
		t.Fatal("togglePlayPause has no end on the phone page")
	}
	if body := page[at : at+end]; strings.Contains(strings.ToLower(body), "volume") {
		t.Error("Play on a sleeping speaker must never change the volume")
	}
}
