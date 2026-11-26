package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The phone page's Deezer card and link handling (docs/streaming/deezer.md):
// every string in English and German, German says du, no dashes, the card
// talks to the agent's endpoints, offers no sign-in form, and a Deezer link
// never goes to the radio directory.
func TestPhonePageDeezer(t *testing.T) {
	start := strings.Index(indexHTML, "var I18N7 = {")
	if start < 0 {
		t.Fatal("the Deezer dictionary I18N7 is missing")
	}
	dict := indexHTML[start : start+strings.Index(indexHTML[start:], "\n};")]
	en := regexp.MustCompile(`(?m)^  en:\{.*$`).FindString(dict)
	de := regexp.MustCompile(`(?m)^  de:\{.*$`).FindString(dict)
	keys := regexp.MustCompile(`[,{](dz\w+):"`).FindAllStringSubmatch(en, -1)
	if len(keys) < 20 {
		t.Fatalf("only %d Deezer strings in English", len(keys))
	}
	for _, k := range keys {
		if !regexp.MustCompile(`[,{]` + k[1] + `:"[^"]+"`).MatchString(de) {
			t.Errorf("%s has no German string", k[1])
		}
		if !strings.Contains(indexHTML, "T."+k[1]) {
			t.Errorf("%s is never used", k[1])
		}
	}
	if strings.ContainsAny(dict, "–—") {
		t.Error("the Deezer strings use an en or em dash")
	}
	if strings.Contains(de, " Sie ") || strings.Contains(de, "Ihr") {
		t.Error("the German Deezer strings must use du")
	}
	for _, want := range []string{
		"var base7 = I18N7.en, sel7 = I18N7[code] || base7;",
		`<div class="card" id="dzCard" hidden>`,
		"apiDetail('/api/deezer/recheck', 'POST', {}, 30000);",
		"apiDetail('/api/deezer/account', 'DELETE', null, 30000);",
		"if (dz) { renderDeezerLink(q, dz); return; }",
		"apiDetail('/api/deezer/play', 'POST', { url: q }, 30000);",
		"src === 'IHEARTRADIO' || src === 'DEEZER'",
		"if (name === 'More') loadDeezerCard();",
		// The Deezer logins of the home: read once with the card, Copy and
		// "Use the older login" only on a press.
		`<div id="dzHome"></div>`,
		"apiDetail('/api/deezer/household', 'GET', null, 20000);",
		"apiDetail('/api/deezer/copy', 'POST', target ? { target: target } : { all: true }, 70000);",
		"apiDetail('/api/deezer/use-older', 'POST', {}, 40000);",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("phone page lacks %q", want)
		}
	}
	// Every locale carries the overview's strings, translated.
	for _, loc := range []string{"nl", "fr", "es", "pl", "tr", "ar", "ja", "lt", "lv", "uk"} {
		line := regexp.MustCompile(`(?m)^  ` + loc + `:\{.*$`).FindString(dict)
		for _, k := range []string{"dzHomeTitle", "dzHomeCopyTo", "dzHomeUseOlder", "dzHomeHint", "dzHomeCopied"} {
			got := regexp.MustCompile(`[,{]` + k + `:"([^"]+)"`).FindStringSubmatch(line)
			want := regexp.MustCompile(`[,{]` + k + `:"([^"]+)"`).FindStringSubmatch(en)
			if got == nil || want == nil || got[1] == want[1] {
				t.Errorf("%s: %s is missing or untranslated", loc, k)
			}
		}
	}
	// The household list is read with the card, never on a timer: the only
	// setTimeout around the card is the re-read after Check again.
	if strings.Contains(indexHTML, "setTimeout(dzLoadHome") || strings.Contains(indexHTML, "setInterval(dzLoadHome") {
		t.Error("the Deezer logins list is polled")
	}
	// A new sign-in is not possible without Bose: no form, no POST of a login.
	for _, gone := range []string{`id="dzPass"`, `id="dzUser"`, "password: pass", "btnDzConnect"} {
		if strings.Contains(indexHTML, gone) {
			t.Errorf("the page still carries the Deezer sign-in: %s", gone)
		}
	}
	// The password is never kept by the page.
	for _, bad := range []string{"localStorage.setItem('str.dz", "sessionStorage.setItem('str.dz"} {
		if strings.Contains(indexHTML, bad) {
			t.Errorf("the page stores Deezer input: %s", bad)
		}
	}
}
