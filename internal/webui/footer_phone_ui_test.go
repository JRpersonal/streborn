package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The footer line "Made with love in Germany" is markup plus one localized
// string per bundle. A bundle that loses the key falls back to English, which
// is harmless, but a lost element or set(...) call would drop the line or
// leave it untranslated in every language.
func TestPhoneRemoteFooterSaysMadeWithLove(t *testing.T) {
	if !strings.Contains(indexHTML, `id="lblMadeWith"`) {
		t.Fatal("footer lost the lblMadeWith element")
	}
	if !strings.Contains(indexHTML, `set('lblMadeWith', T.madeWith)`) {
		t.Fatal("lblMadeWith is no longer localized via set(...)")
	}
	bundles := strings.Count(indexHTML, "now:\"")
	if bundles == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	vals := regexp.MustCompile(`madeWith:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1)
	if len(vals) != bundles {
		t.Fatalf("madeWith: %d locale bundles but %d keys", bundles, len(vals))
	}
	for _, v := range vals {
		if !strings.Contains(v[1], "♥") {
			t.Errorf("madeWith %q lost the heart", v[1])
		}
	}
}

// The trademark notice is a legal requirement (CLAUDE.md: visible in the app's
// About). The phone has no About dialog, so the footer carries it in every
// layout and in every locale bundle.
func TestPhoneRemoteFooterCarriesTrademarkNotice(t *testing.T) {
	if !strings.Contains(indexHTML, `id="lblTrademark"`) {
		t.Fatal("footer lost the lblTrademark element")
	}
	if !strings.Contains(indexHTML, `set('lblTrademark', T.trademark)`) {
		t.Fatal("lblTrademark is no longer localized via set(...)")
	}
	bundles := strings.Count(indexHTML, "now:\"")
	vals := regexp.MustCompile(`trademark:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1)
	if len(vals) != bundles {
		t.Fatalf("trademark: %d locale bundles but %d keys", bundles, len(vals))
	}
	for _, v := range vals {
		if !strings.Contains(v[1], "Bose") || !strings.Contains(v[1], "SoundTouch") {
			t.Errorf("trademark notice %q does not name Bose and SoundTouch", v[1])
		}
	}
}
