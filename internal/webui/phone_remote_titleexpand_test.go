package webui

import (
	"strings"
	"testing"
)

// Discussion #1811: on the phone page a tap on a cut-off title opens it to its
// full length and the next tap closes it. Only the text reacts and only when
// it is cut off; a title inside a control (a key tile, a button) is left to
// the control, so no tap that plays something is taken away. No new control
// and no new string.
//
// A title that opens carries a small arrow at its end, down while closed and
// up while open, and only while it really is cut off: a title that fits shows
// nothing. It is a CSS shape in the muted colour, not a font glyph.
func TestPhoneRemoteOpensACutOffTitleOnATap(t *testing.T) {
	page := strings.ReplaceAll(indexHTML, "\r\n", "\n")
	for _, want := range []string{
		// What can be opened, and where the tap belongs to something else.
		"var TITLE_SEL = '.clamp2, .cut1';",
		`var TITLE_CTRL = 'button, a, input, select, textarea, label, summary, [role="button"], .preset';`,
		// The rule: never inside a control, never while selecting, open only
		// what is cut off.
		"function titlePress(inControl, open, cutOff, selecting) {\n  if (inControl || selecting) return '';\n  if (open) return 'close';\n  return cutOff ? 'open' : '';\n}",
		"var press = titlePress(!!el.closest(TITLE_CTRL), open, !open && titleCutOff(el), sel.trim() !== '');",
		// The open state, for the two-line and the one-line cut.
		".clamp2.title-open, .nowcard .now.clamp2.title-open { -webkit-line-clamp:unset; line-clamp:unset; overflow:visible; }",
		".cut1.title-open { overflow:visible; text-overflow:clip; white-space:normal; overflow-wrap:anywhere; }",
		// The now-playing card is rewritten by every status poll: an opened
		// title is opened again on what comes back.
		"if (openTitles.indexOf(titleName(el)) >= 0 && !el.closest(TITLE_CTRL)) el.classList.add('title-open');",
		// The arrow: on an opened title always, on a closed one only while
		// it is cut off, never inside a control.
		"function titleMark(inControl, open, cutOff) {\n  if (inControl) return false;\n  return open || cutOff;\n}",
		// A truncated row gets the mark and a fitting row does not. The mark
		// comes off before the measuring, so its own room never keeps it on.
		"el.classList.remove('title-cut');\n    closed.push(el);",
		"var cut = closed.map(titleCutOff);",
		"if (titleMark(false, false, cut[i])) el.classList.add('title-cut');",
		// An opened title is not measured and a closed one is measured again.
		"if (!document.documentElement.contains(el) || el.classList.contains('title-open') || el.closest(TITLE_CTRL)) return;",
		"if (press === 'close') titleMarkCut([el]);",
		// Kept true by size, not by a timer: once when a title is drawn and
		// again whenever its box changes (turning the phone, a shown list).
		// Measured on the next frame, never inside the observer's own report.
		"titleWatch = function (el) { titleSizes.observe(el, { box: 'border-box' }); titleMeasureSoon(el); };",
		"entries.forEach(function (e) { titleMeasureSoon(e.target); });",
		"window.requestAnimationFrame(function () { var els = titleDue; titleDue = null; titleMarkCut(els); });",
		"titlesIn(node).forEach(titleUnwatch);",
		// The shape: room for it only on a marked title, out of the flow, at
		// the inline end, muted, pointing down, and up while open.
		".clamp2.title-cut, .cut1.title-cut, .clamp2.title-open, .cut1.title-open { position:relative; padding-inline-end:26px; cursor:pointer; }",
		".clamp2.title-cut::after, .cut1.title-cut::after, .clamp2.title-open::after, .cut1.title-open::after { content:''; position:absolute; inset-inline-end:8px; bottom:.5em; bottom:calc(.5lh - 2px); width:7px; height:7px; box-sizing:border-box; border:solid var(--muted); border-width:0 2px 2px 0; transform:rotate(45deg); }",
		".clamp2.title-open::after, .cut1.title-open::after { bottom:.25em; bottom:calc(.5lh - 6px); transform:rotate(-135deg); }",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the phone page lost a part of tap-to-read-all: %q", want)
		}
	}
	// No polling for the arrow.
	at := strings.Index(page, "var TITLE_SEL = ")
	end := strings.Index(page, "// setNow renders the now-playing card")
	if at < 0 || end < at {
		t.Fatal("the title block of the phone page was not found")
	}
	if strings.Contains(page[at:end], "setInterval") {
		t.Error("the arrow must follow the title's size, not a timer")
	}
	// The two-line cut itself (#1783) is what there is to open.
	if !strings.Contains(page, `'<span class="now clamp2" title="'`) {
		t.Error("the now-playing title must keep its two-line cut and its title attribute")
	}
}
