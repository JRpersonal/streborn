package webui

import (
	"strings"
	"testing"
)

// phoneFn returns the body of a top-level function on the phone page.
func phoneFn(t *testing.T, sig string) string {
	t.Helper()
	start := strings.Index(indexHTML, sig)
	if start < 0 {
		t.Fatalf("%s is gone; if it moved, move this test with it", sig)
	}
	fn := indexHTML[start:]
	if end := strings.Index(fn, "\n}\n"); end >= 0 {
		fn = fn[:end]
	}
	return fn
}

// The phone page's mute used to pull the volume to 0 and remember the old
// level. The desktop app presses the speaker's own MUTE, so neither could see
// the other's mute: muting in the app left the phone showing "on", and muting
// on the phone left the app showing "on" (#1748). Both now set and read the
// speaker's own flag.
func TestPhoneRemoteMuteIsTheSpeakersMute(t *testing.T) {
	sm := phoneFn(t, "async function setMute(want)")
	for _, want := range []string{
		"api('/api/box/mute', 'POST', { muted: want })",
		"typeof res.muted === 'boolean' ? res.muted : !want",
	} {
		if !strings.Contains(sm, want) {
			t.Errorf("setMute lost %q", want)
		}
	}
	tm := phoneFn(t, "function toggleMute()")
	if strings.Contains(tm, "onVol(") || strings.Contains(tm, "el.value = 0") {
		t.Error("toggleMute pulls the volume to 0 again; the app cannot see that mute")
	}
	if strings.Contains(indexHTML, "muteSaved") {
		t.Error("the remembered volume of the old volume-0 mute is back")
	}

	ls := phoneFn(t, "async function loadSettings()")
	i := strings.Index(ls, "renderMute(s.volume.muted)")
	if i < 0 {
		t.Fatal("loadSettings no longer draws the speaker's mute flag")
	}
	// It has to sit outside the single-speaker volume branch, so a grouped
	// speaker's glyph still follows its flag.
	if j := strings.Index(ls, "(volScope === 'one' || !zone.grouped)) {"); j >= 0 && j < i {
		t.Error("the mute glyph is only drawn in single-speaker scope")
	}
	if !strings.Contains(ls[:i], "Date.now() >= muteUntil") {
		t.Error("a settings poll right after a press can flip the glyph back")
	}

	ov := phoneFn(t, "function onVol(v)")
	b := strings.Index(ov, "sliderBlocked(")
	l := strings.Index(ov, "liftMuteForVolume()")
	if l < 0 || b < 0 || l < b {
		t.Error("onVol must lift a mute on a real volume gesture, after the scroll guard")
	}
}
