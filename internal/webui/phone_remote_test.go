package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The phone remote is the self-contained indexHTML page the agent serves on "/".
// These tests guard the client-side behaviour reported in #294 and #295, which
// lives only as embedded JS and so has no other automated coverage.

// TestPhoneRemoteDecodesNowPlayingEntities guards #295: a now_playing title the
// box serves entity-encoded (e.g. New York&apos;s) must be decoded before it is
// re-escaped for display, otherwise the leading & is doubled and the remote
// shows a literal "&apos;". The fix adds decodeEntities and runs the captured
// itemName/track through it.
func TestPhoneRemoteDecodesNowPlayingEntities(t *testing.T) {
	if !strings.Contains(indexHTML, "function decodeEntities(") {
		t.Fatal("indexHTML is missing the decodeEntities helper (#295)")
	}
	// The captured now-playing name must be decoded, not used raw.
	if !strings.Contains(indexHTML, "const name = m ? decodeEntities(m[1]) : '';") {
		t.Fatal("indexHTML must decode entities on the now_playing name before display (#295)")
	}
	if strings.Contains(indexHTML, "const name = m ? m[1] : '';") {
		t.Fatal("indexHTML still uses the raw, un-decoded now_playing name (#295 regression)")
	}
}

// TestPhoneRemotePauseStopHaveIcons guards #382: the Pause and Stop buttons carry
// a media glyph plus a localized label span (like Prev/Next), not bare text, and
// the label swap keeps the glyph.
func TestPhoneRemotePauseStopHaveIcons(t *testing.T) {
	for _, id := range []string{"btnPauseLbl", "btnStopLbl", "btnPauseIcon"} {
		if !strings.Contains(indexHTML, `id="`+id+`"`) {
			t.Fatalf("phone remote missing %s span (#382)", id)
		}
	}
	// The label swap must target the label span, never the whole button (which
	// would wipe the icon).
	if !strings.Contains(indexHTML, "getElementById('btnPauseLbl')") {
		t.Fatal("applyTransportUI must set the label span, not the button text (#382)")
	}
	if strings.Contains(indexHTML, "b.textContent = paused") {
		t.Fatal("applyTransportUI still overwrites the whole Pause button, wiping its icon (#382 regression)")
	}
}

// TestPhoneRemoteHidesRawSource guards #384: a stopped/idle box reports source
// INVALID_SOURCE / STANDBY with no track name, and that raw firmware string must
// never be shown as the now-playing title.
func TestPhoneRemoteHidesRawSource(t *testing.T) {
	if strings.Contains(indexHTML, "setNow(name || src || T.idle") {
		t.Fatal("phone remote still shows the raw source (INVALID_SOURCE) as the title (#384 regression)")
	}
	if !strings.Contains(indexHTML, "INVALID_SOURCE") || !strings.Contains(indexHTML, "idleSrc") {
		t.Fatal("phone remote must map an idle INVALID_SOURCE/STANDBY source to the friendly idle text (#384)")
	}
}

// TestPhoneRemotePlayPauseToggle guards #294: the single Pause button must double
// as Play/Pause so a stream paused from the remote can be resumed from the remote
// (via the existing /api/resume endpoint) instead of only from the app or the
// physical Bose remote.
func TestPhoneRemotePlayPauseToggle(t *testing.T) {
	if !strings.Contains(indexHTML, "onclick=\"togglePlayPause(this)\"") {
		t.Fatal("the Pause button must call togglePlayPause (#294)")
	}
	if !strings.Contains(indexHTML, "async function togglePlayPause(") {
		t.Fatal("indexHTML is missing the togglePlayPause function (#294)")
	}
	if !strings.Contains(indexHTML, "'/api/resume'") {
		t.Fatal("togglePlayPause must resume via /api/resume when paused (#294)")
	}
	if !strings.Contains(indexHTML, "function applyTransportUI(") {
		t.Fatal("indexHTML is missing applyTransportUI to reflect the paused state (#294)")
	}
	// The old, resume-less wiring must be gone.
	if strings.Contains(indexHTML, "pp(this,'/api/pause')") {
		t.Fatal("the Pause button still hard-wires /api/pause with no resume path (#294 regression)")
	}
}

// TestPhoneRemoteLocalesHavePlayLabel guards that the new Play/Resume button
// label is translated for every locale bundle, not left to fall through to the
// English "Play". Each bundle carries exactly one now:"..." and must carry one
// play:"..." beside it.
func TestPhoneRemoteLocalesHavePlayLabel(t *testing.T) {
	nowCount := strings.Count(indexHTML, "now:\"")
	// play appears once per bundle, and once as the applyTransportUI reference
	// (T.play). Count only the bundle keys via the play:" object-key form.
	playKeys := regexp.MustCompile(`play:"`).FindAllString(indexHTML, -1)
	if nowCount == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	if len(playKeys) != nowCount {
		t.Fatalf("expected one play label per locale bundle: %d bundles but %d play keys", nowCount, len(playKeys))
	}
}

// TestPhoneRemoteHidesUnavailableSources guards #417/#418: the input buttons
// must be gated on the box's own /sources list so a Wave (no selectable AUX
// through the pedestal) does not offer a dead AUX button.
func TestPhoneRemoteHidesUnavailableSources(t *testing.T) {
	for _, tok := range []string{`id="btnSrcAux"`, `id="btnSrcBt"`, "have.AUX", "have.BLUETOOTH", "s.sources"} {
		if !strings.Contains(indexHTML, tok) {
			t.Fatalf("phone remote missing source-availability gating token %q (#417/#418)", tok)
		}
	}
}

// TestPhoneRemoteNamesAStereoPairAsAPair guards the 2026-08-07 report: a stereo
// pair IS a firmware group internally, and the Speakers card said so out loud,
// heading a working pair with "Group / 2 speakers playing together / Dissolve
// group". The owner read that as STR having misunderstood their setup. The
// volume scope row already distinguished the two, so only the card was wrong.
func TestPhoneRemoteNamesAStereoPairAsAPair(t *testing.T) {
	for _, want := range []string{
		"lbl.textContent = zone.stereo ? T.scPair : T.grp",
		"lblUn.textContent = zone.stereo ? T.unpair : T.ungroup",
		"sum.textContent = zone.stereo ? T.pairSum : fmt(T.grpSum",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("the Speakers card does not switch to pair wording: missing %q", want)
		}
	}
	// The scope hint sits above the slider and described a pair as a group too.
	if !strings.Contains(indexHTML, "hint.textContent = zone.stereo ? T.pairSum : fmt(T.grpSum") {
		t.Error("the volume scope hint still calls a stereo pair a group")
	}
}

// TestPhoneRemoteSleepFailureIsVisible guards the other half of the same sweep:
// api() answers null on an error status, and a path an older agent does not know
// falls through to the index handler and answers 200 with the page itself as a
// string. Both used to leave sleepEndsAt at 0 and silently reset the card, so a
// tap that failed was indistinguishable from one that did nothing (#487).
func TestPhoneRemoteSleepFailureIsVisible(t *testing.T) {
	if !strings.Contains(indexHTML, "typeof st.active === 'boolean'") {
		t.Error("setSleep does not check that the reply is actually a timer state (the 200+HTML trap)")
	}
	if !strings.Contains(indexHTML, "sum.textContent = T.sleepFail") {
		t.Error("a failed arming attempt still says nothing to the user")
	}
}

// TestPhoneRemoteLocalesCarryTheNewKeys keeps the three strings added for the
// two fixes above translated everywhere, rather than falling through to English
// on nine of the twelve bundles.
func TestPhoneRemoteLocalesCarryTheNewKeys(t *testing.T) {
	bundles := strings.Count(indexHTML, "now:\"")
	if bundles == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	for _, key := range []string{"pairSum", "unpair", "sleepFail"} {
		got := len(regexp.MustCompile(key+`:"`).FindAllString(indexHTML, -1))
		if got != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, got)
		}
	}
}

// TestPhoneRemoteSleepArmingIsNotSelfCancelling guards the defect that made the
// sleep timer unusable from the day it shipped: press() adds .active as a
// 600 ms tap flash, and the click handler read .active as "this choice is
// already running". The test was therefore always true, every tap took the
// cancel branch and sent minutes=0, and no timer could ever be armed. The
// speaker stayed silent about it too, because cancelling nothing is a no-op
// (#487, bundle 2026-08-08 with not one sleep line in the agent log).
func TestPhoneRemoteSleepArmingIsNotSelfCancelling(t *testing.T) {
	i := strings.Index(indexHTML, "function wireSleep(")
	if i < 0 {
		t.Fatal("phone remote has no sleep wiring")
	}
	end := strings.Index(indexHTML[i:], "})();")
	if end < 0 {
		t.Fatal("could not delimit wireSleep")
	}
	wire := indexHTML[i : i+end]

	if strings.Contains(wire, "classList.contains('active')") {
		t.Error("the armed check still reads .active, the class press() sets on every tap: every press cancels instead of arming")
	}
	if !strings.Contains(wire, "classList.contains('armed')") {
		t.Error("the armed state is not kept in its own class")
	}
	if !strings.Contains(wire, "setSleep(mins)") {
		t.Error("the handler never arms the chosen duration")
	}
}

// The armed highlight must be painted from the state, not left behind by the
// click handler: press() removes its class after 600 ms, so a highlight applied
// at click time vanished while the timer was still running.
func TestPhoneRemoteSleepHighlightComesFromState(t *testing.T) {
	if !strings.Contains(indexHTML, `b.classList.toggle('armed', on)`) {
		t.Error("renderSleep does not paint the armed choice from the timer state")
	}
	if !strings.Contains(indexHTML, "button.btn.armed") {
		t.Error("the armed choice has no styling of its own")
	}
}
