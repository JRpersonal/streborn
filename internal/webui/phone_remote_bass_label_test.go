package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The phone page's bass slider is fixed at -9..0: it can only take bass away,
// so it is labelled "Bass reduction" and not "Bass", which read like a boost
// control (discussion #1339). The label and the slider's aria-label both come
// from T.bass, so every locale must carry a bass label that is not the bare
// word for bass.
func TestPhoneRemoteBassSliderIsLabelledAsAReduction(t *testing.T) {
	if !strings.Contains(indexHTML, `id="bass" min="-9" max="0"`) {
		t.Fatal("the phone bass slider is no longer fixed at -9..0: revisit its label, it may be able to add bass now")
	}
	for _, want := range []string{
		`<div class="label" id="lblBass">Bass reduction</div>`,
		`aria-label="Bass reduction"`,
		`,bass:"Bass reduction"`,
		`,bass:"Bassabsenkung"`,
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("phone page: %q is gone", want)
		}
	}
	labels := regexp.MustCompile(`\n  ([a-z]{2}):\{[^\n]*?,bass:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1)
	if len(labels) < 12 {
		t.Fatalf("found %d locale bass labels, want at least 12", len(labels))
	}
	plain := map[string]string{"en": "Bass", "de": "Bass", "nl": "Bas", "fr": "Basses", "es": "Graves",
		"pl": "Basy", "tr": "Bas", "ar": "الجهير", "ja": "低音", "lt": "Žemieji dažniai", "lv": "Basi", "uk": "Баси"}
	for _, m := range labels {
		if old, ok := plain[m[1]]; ok && m[2] == old {
			t.Errorf("%s: the bass slider is still labelled %q, not as a reduction", m[1], m[2])
		}
	}
}

// A spaced hyphen is not punctuation in user-facing text: the wedge hint says
// "plug it back in. After that" rather than "plug it back in - after that".
func TestPhoneRemoteWedgeTextHasNoDashPunctuation(t *testing.T) {
	texts := regexp.MustCompile(`wedgeText:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1)
	if len(texts) < 12 {
		t.Fatalf("found %d wedgeText strings, want at least 12", len(texts))
	}
	for _, m := range texts {
		if regexp.MustCompile(` [-\x{2013}\x{2014}] `).MatchString(m[1]) {
			t.Errorf("wedgeText uses a dash as punctuation: %q", m[1])
		}
	}
}
