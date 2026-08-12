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

// TestPhoneRemoteHidesUnavailableSources guards #417/#418: a speaker must never
// be offered an input it does not have, such as the Wave whose pedestal has no
// selectable AUX.
//
// The mechanism changed when soundbar inputs arrived: instead of two fixed
// buttons whose visibility was toggled, the buttons ARE the box's own list, so
// an input that is absent from that list cannot be rendered in the first place.
// The guarantee is the same and stronger, so this test now guards the source of
// the buttons rather than the old visibility toggles.
func TestPhoneRemoteHidesUnavailableSources(t *testing.T) {
	if !strings.Contains(indexHTML, "s.sources") || !strings.Contains(indexHTML, "renderInputs(s.sources)") {
		t.Fatal("the input buttons are no longer built from the box's own source list (#417/#418)")
	}
	// Nothing may render an input that is not in the list the box reported.
	if strings.Contains(indexHTML, `id="btnSrcAux"`) && !strings.Contains(indexHTML, "card.innerHTML = ''") {
		t.Fatal("the static fallback buttons are never replaced by the box's real list (#417/#418)")
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

// TestPhoneRemoteCanFormAndUndoGroups guards the feature #400 asked for: from
// the phone, pull another speaker into a group and drop it again.
func TestPhoneRemoteCanFormAndUndoGroups(t *testing.T) {
	for _, want := range []string{
		"function peerRow(",       // the name plus a join control beside it
		"function canJoinPeer(",   // who may be joined at all
		"function joinPeer(",      // POST /api/box/zone
		"function leavePeer(",     // drop one member
		"peerRow(p)",              // actually used by the peer list
		"'/api/box/zone', 'POST'", // forms the zone
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("phone group editing is missing %q", want)
		}
	}
	// The tap on the name must keep switching to that speaker: the join control
	// is additive, and overloading the tap was the thing this design rejected.
	if !strings.Contains(indexHTML, "if (online) { a.href = p.url; }") {
		t.Error("tapping a peer no longer switches the remote to it")
	}
}

// The two cases that deliberately cannot join (decided 2026-08-09): a speaker
// that is half of a stereo pair, where a third speaker has no clear meaning,
// and a follower, which cannot take zone commands at all.
func TestPhoneRemoteRefusesToGroupPairsAndFollowers(t *testing.T) {
	i := strings.Index(indexHTML, "function canJoinPeer(")
	if i < 0 {
		t.Fatal("canJoinPeer is missing")
	}
	body := indexHTML[i : i+700]
	if !strings.Contains(body, "if (zone.stereo) return false") {
		t.Error("a stereo pair half can be pulled into a group, which has no defined meaning")
	}
	if !strings.Contains(body, "if (zone.grouped && !zone.master) return false") {
		t.Error("a follower offers a join control it cannot honour")
	}
	if !strings.Contains(body, "!p.deviceID") {
		t.Error("a peer with no deviceID is offered, but /setZone cannot name it")
	}
}

// The lesson from the 2026-08-08 twelve-speaker log, now in the client that is
// most exposed to it: group edits run one at a time and a repeat on a speaker
// whose change is still running is refused, not queued.
func TestPhoneRemoteSerialisesGroupEdits(t *testing.T) {
	if !strings.Contains(indexHTML, "function queueGroupOp(") {
		t.Fatal("group edits are not serialised")
	}
	if !strings.Contains(indexHTML, "if (groupBusy[key]) return groupChain;") {
		t.Error("a repeat tap during a pending change is not refused, so taps can stack into overlapping /setZone drives")
	}
	if !strings.Contains(indexHTML, "groupChain = groupChain.then(") {
		t.Error("group edits are not chained, so two can run at once")
	}
}

// Every string the feature adds must exist in all twelve locales.
func TestPhoneRemoteLocalesCarryTheGroupKeys(t *testing.T) {
	bundles := strings.Count(indexHTML, "now:\"")
	if bundles == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	for _, key := range []string{"joinTitle", "joinAria", "joining", "joinFail", "leaveAria"} {
		got := len(regexp.MustCompile(key+`:"`).FindAllString(indexHTML, -1))
		if got != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, got)
		}
	}
}

// TestPhoneRemoteSlidersSurviveScrolling guards the group screen: it is a column
// of volume sliders, so a thumb coming down to scroll lands on one, and a range
// input takes that touch as a value change. Users scrolled a member list and
// made the room louder (Jens, 2026-08-13).
//
// Two halves have to be present. The CSS gives the vertical direction back to
// the page, which is what makes the browser send the slider a pointercancel
// instead of a drag; the guard decides when a touch may reach the speaker at
// all. Either one alone leaves the bug in place, so both are pinned here.
func TestPhoneRemoteSlidersSurviveScrolling(t *testing.T) {
	if !strings.Contains(indexHTML, "input[type=range] { touch-action: pan-y; }") {
		t.Fatal("range inputs must leave vertical panning to the page (touch-action: pan-y)")
	}
	if !strings.Contains(indexHTML, "function scrollSafeSlider(") ||
		!strings.Contains(indexHTML, "function sliderBlocked(") {
		t.Fatal("indexHTML is missing the slider scroll guard")
	}
	// A guard nothing consults is decoration. Every path that can move a
	// speaker from a slider has to ask it: the main volume, the bass, and the
	// per-member sliders of a group.
	for _, call := range []string{
		"if (sliderBlocked(document.getElementById('vol'))) return;",
		"if (sliderBlocked(document.getElementById('bass'))) return;",
		"if (!sliderBlocked(sl)) memberVol(m.ip, sl.value);",
	} {
		if !strings.Contains(indexHTML, call) {
			t.Fatalf("a slider send path does not consult the scroll guard: %s", call)
		}
	}
	// The cancel path is the one the browser uses when it claims the gesture.
	if !strings.Contains(indexHTML, "'pointercancel'") {
		t.Fatal("the guard must react to pointercancel, which is how the browser announces a scroll")
	}
}

// TestPhoneRemoteDoesNotZoom guards the app feel: a stray pinch used to leave
// the remote at 1.4x with half the controls off screen, and it is the one thing
// that gives a web page away on a phone (Jens, 2026-08-13).
//
// Three parts, because no single one covers every engine: the touch-action rule
// (Chromium and friends), the viewport meta (Android browsers), and the WebKit
// gesture events, which is the only lever on iOS since Safari ignores
// user-scalable by design.
func TestPhoneRemoteDoesNotZoom(t *testing.T) {
	if !strings.Contains(indexHTML, "html { touch-action: pan-x pan-y; }") {
		t.Fatal("the page must allow panning only, so pinch and double-tap zoom are off")
	}
	if !strings.Contains(indexHTML, "user-scalable=no") || !strings.Contains(indexHTML, "maximum-scale=1") {
		t.Fatal("the viewport meta must refuse scaling")
	}
	if !strings.Contains(indexHTML, "'gesturestart', 'gesturechange', 'gestureend'") {
		t.Fatal("iOS needs the WebKit gesture events prevented; it ignores user-scalable")
	}
	// Taking zoom away is only acceptable because the page carries its own
	// text sizes. If those ever go, this test should fail and force the
	// decision to be made again.
	if !strings.Contains(indexHTML, "a11y-scale-xl") {
		t.Fatal("the built-in text sizes are what make refusing pinch-zoom acceptable")
	}
}

// TestPhoneRemoteTabbarStaysAtTheBottom guards two ways the bar left the bottom
// edge: a zoomed ANCESTOR (the text-size setting used to zoom body, which made
// the fixed bar resolve bottom:0 against the zoomed viewport and float roughly
// a quarter of the screen up at 1.3x), and a page shorter than the display,
// where a browser that cannot scroll keeps its own toolbar expanded.
func TestPhoneRemoteTabbarStaysAtTheBottom(t *testing.T) {
	if !strings.Contains(indexHTML, "position:fixed; left:0; right:0; bottom:0") {
		t.Fatal("the tab bar must be pinned to the viewport")
	}
	// The zoom must never sit on an ANCESTOR of the fixed bar.
	if strings.Contains(indexHTML, "html.a11y-scale-l  body { zoom") ||
		strings.Contains(indexHTML, "html.a11y-scale-xl body { zoom") {
		t.Fatal("the text-size zoom is back on body, which unpins the fixed tab bar")
	}
	if !strings.Contains(indexHTML, "html.a11y-scale-xl body > * { zoom:1.30; }") {
		t.Fatal("the text-size zoom must apply to body's children, not to body")
	}
	if !strings.Contains(indexHTML, "min-height:100dvh") {
		t.Fatal("a short page must still fill the display, or the browser toolbar lifts the bar")
	}
}
