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
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the phone page lost a part of tap-to-read-all: %q", want)
		}
	}
	// The two-line cut itself (#1783) is what there is to open.
	if !strings.Contains(page, `'<span class="now clamp2" title="'`) {
		t.Error("the now-playing title must keep its two-line cut and its title attribute")
	}
}
