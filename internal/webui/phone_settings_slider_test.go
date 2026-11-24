package webui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// #2066: on the phone page a thumb scrolling down to Software update rested on
// the start-volume slider and set it, every time. The start volume and the
// alarm volume are settings, so they go through the scroll guard in its
// settings mode: only a sideways drag changes them, a tap changes nothing,
// and the start volume is saved once, on release.

func TestPhoneSettingsSlidersAreGuarded(t *testing.T) {
	for _, want := range []string{
		"scrollSafeSlider(sl, saveStartVol, svPaint, { settings: true });",
		"sl.addEventListener('change', function(){ if (!sliderJustTouched(sl)) saveStartVol(sl.value); });",
		"scrollSafeSlider(vol, function() {}, paintVol, { settings: true });",
		"vol.addEventListener('input', function() { if (!sliderBlocked(vol)) paintVol(); });",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("phone page is missing: %s", want)
		}
	}
	// The old unguarded wiring saved whatever 'change' carried, including the
	// value a scroll left behind.
	if strings.Contains(indexHTML, "sl.addEventListener('change', function(){ saveStartVol(sl.value); });") {
		t.Error("the start volume still saves on every change event")
	}
}

// guardSource cuts the slider guard out of the page, from its first variable
// to the end of scrollSafeSlider.
func guardSource(t *testing.T) string {
	t.Helper()
	a := strings.Index(indexHTML, "var sliderGuards = ")
	b := strings.Index(indexHTML, "// The slider moves whatever the scope row says")
	if a < 0 || b < a {
		t.Fatal("cannot find the slider guard in index.html")
	}
	return indexHTML[a:b]
}

// The behaviour, run in node against a fake range input. Skipped where node
// is not installed; the frontend CI job has it.
const guardHarness = `
function fakeSlider(v) {
  var ls = {};
  return { value: String(v), addEventListener: function(n, f) { (ls[n] = ls[n] || []).push(f); },
           fire: function(n, e) { (ls[n] || []).forEach(function(f) { f(e || {}); }); } };
}
var fails = [];
function check(name, got, want) { if (String(got) !== String(want)) fails.push(name + ": got " + got + ", want " + want); }

// 1. Settings slider, a scroll starting on the bar: the browser jumps the
// value, then claims the gesture. The value goes back, nothing is saved, and
// the 'change' that Chrome fires afterwards is ignored.
(function() {
  var el = fakeSlider(20), sent = [];
  scrollSafeSlider(el, function(v) { sent.push(v); }, null, { settings: true });
  el.fire('pointerdown', { pointerType: 'touch', clientX: 300, clientY: 100 });
  el.value = '100';
  check('scroll: blocked while undecided', sliderBlocked(el), true);
  el.fire('pointermove', { clientX: 301, clientY: 130 });
  el.fire('pointercancel');
  check('scroll: value restored', el.value, '20');
  check('scroll: nothing sent', sent.length, 0);
  check('scroll: change right after is the guard\'s', sliderJustTouched(el), true);
})();

// 2. Settings slider, a tap on the end of the bar: no change.
(function() {
  var el = fakeSlider(20), sent = [];
  scrollSafeSlider(el, function(v) { sent.push(v); }, null, { settings: true });
  el.fire('pointerdown', { pointerType: 'touch', clientX: 300, clientY: 100 });
  el.value = '100';
  el.fire('pointerup');
  check('tap: value restored', el.value, '20');
  check('tap: nothing sent', sent.length, 0);
})();

// 3. Settings slider, a real sideways drag: saved once, on release.
(function() {
  var el = fakeSlider(20), sent = [];
  scrollSafeSlider(el, function(v) { sent.push(v); }, null, { settings: true });
  el.fire('pointerdown', { pointerType: 'touch', clientX: 100, clientY: 100 });
  el.fire('pointermove', { clientX: 120, clientY: 101 });
  el.value = '35';
  check('drag: not sent while dragging', sent.length, 0);
  el.fire('pointerup');
  check('drag: sent once on release', sent.join(','), '35');
})();

// 4. Settings slider, a mouse click on the track still sets it.
(function() {
  var el = fakeSlider(20), sent = [];
  scrollSafeSlider(el, function(v) { sent.push(v); }, null, { settings: true });
  el.fire('pointerdown', { pointerType: 'mouse', clientX: 100, clientY: 100 });
  el.value = '60';
  el.fire('pointerup');
  check('mouse: sent', sent.join(','), '60');
})();

// 5. The main volume keeps its behaviour: a tap raises by at most a step,
// a drag sends live.
(function() {
  var el = fakeSlider(20), sent = [];
  scrollSafeSlider(el, function(v) { sent.push(v); }, null);
  el.fire('pointerdown', { pointerType: 'touch', clientX: 300, clientY: 100 });
  el.value = '100';
  el.fire('pointerup');
  check('volume tap: capped raise', sent.join(','), String(20 + TAP_MAX_RAISE));
  var el2 = fakeSlider(20), sent2 = [];
  scrollSafeSlider(el2, function(v) { sent2.push(v); }, null);
  el2.fire('pointerdown', { pointerType: 'touch', clientX: 100, clientY: 100 });
  el2.value = '30';
  el2.fire('pointermove', { clientX: 120, clientY: 100 });
  check('volume drag: live send on arm', sent2.join(','), '30');
})();

if (fails.length) { console.log(fails.join('\n')); process.exit(1); }
console.log('ok');
`

func TestPhoneSliderGuardBehaviour(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	f := filepath.Join(t.TempDir(), "guard.js")
	if err := os.WriteFile(f, []byte(guardSource(t)+guardHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("guard behaviour: %v\n%s", err, out)
	}
}
