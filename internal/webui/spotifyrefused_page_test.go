package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The phone page's Spotify refused notice (#1671) says why and what to try,
// not only that it happened (GH #78, #1426, #1648, #1636, #973, discussion
// #311). A free account and a Premium account Spotify refuses on (STR) get
// different words, because the cure differs.

// spRefusedKeys are the strings the notice needs in every language the page
// speaks.
var spRefusedKeys = []string{
	"spRefused:", "spRefusedNamed:", "spRefusedFree:", "spRefusedFreeNamed:",
	"spRefusedMore:", "spRefusedWhy:", "spRefusedTry:", "spRefusedFreeWhy:", "spRefusedFreeTry:",
}

func TestPhoneSpotifyRefusedHelpIsTranslatedEverywhere(t *testing.T) {
	page := readIndexHTML(t)
	start := strings.Index(page, "var I18N2 = {")
	if start < 0 {
		t.Fatal("I18N2 dictionary not found")
	}
	end := strings.Index(page[start:], "\n};")
	lang := regexp.MustCompile(`^  ([a-z]{2}):\{`)
	seen := 0
	for _, line := range strings.Split(page[start:start+end], "\n") {
		m := lang.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		seen++
		for _, k := range spRefusedKeys {
			if !strings.Contains(line, k) {
				t.Errorf("language %q has no %s", m[1], strings.TrimSuffix(k, ":"))
			}
		}
		// The named forms fill in the account; the page's fmt takes {account}.
		for _, k := range []string{"spRefusedNamed:", "spRefusedFreeNamed:"} {
			i := strings.Index(line, k)
			if i >= 0 && !strings.Contains(line[i:i+400], "{account}") {
				t.Errorf("language %q: %s does not name the account", m[1], strings.TrimSuffix(k, ":"))
			}
		}
	}
	if seen < 12 {
		t.Errorf("only %d languages in I18N2", seen)
	}
}

func TestPhoneSpotifyRefusedSaysWhyAndTheTest(t *testing.T) {
	page := readIndexHTML(t)
	en := regexp.MustCompile(`(?m)^  en:\{.*$`).FindAllString(page[strings.Index(page, "var I18N2 = {"):], 1)
	if len(en) == 0 {
		t.Fatal("English I18N2 line not found")
	}
	line := en[0]
	for _, want := range []string{
		// The Premium case: Spotify's rule, with no claim about which
		// accounts it hits (account age is not a reliable sign), and the
		// decisive test on the (STR) entry with another account.
		`spRefusedWhy:"Spotify refuses some accounts on players like (STR), even with Premium. This is Spotify's rule, not a fault of your speaker."`,
		`spRefusedTry:"To check, play once from another Spotify account on the entry ending in (STR).`,
		// The free case: Premium is the cure, and nothing about the speaker's
		// own entry, which is not known to play free accounts.
		`spRefusedFree:"Spotify won't stream this account on (STR) because it is a free account."`,
		`spRefusedFreeWhy:"Spotify on (STR) only plays with Spotify Premium."`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("English line lacks %q", want)
		}
	}
	free := regexp.MustCompile(`spRefusedFree(?:Named|Why|Try)?:"[^"]*"`).FindAllString(line, -1)
	for _, s := range free {
		if strings.Contains(s, "without (STR)") {
			t.Errorf("the free-account text promises the speaker's own entry: %s", s)
		}
	}
	for _, want := range []string{
		"function spFreePlan(info)",
		"free: spFreePlan(info)",
		"sum.textContent = T.spRefusedMore;",
		"el.getAttribute('data-sig') === sig",
		`<div class="sp-refused" id="spRefused" role="status" hidden></div>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

// Spotify presets carry the account they were saved under and switch back to
// it before each recall, so the free-account cure "play once from a Premium
// account" only sticks once the Spotify presets are saved again. Every
// language must keep that step after the (STR) entry.
func TestPhoneSpotifyRefusedFreeCureSavesThePresetsAgain(t *testing.T) {
	page := readIndexHTML(t)
	start := strings.Index(page, "var I18N2 = {")
	if start < 0 {
		t.Fatal("I18N2 dictionary not found")
	}
	end := strings.Index(page[start:], "\n};")
	lang := regexp.MustCompile(`^  ([a-z]{2}):\{`)
	try := regexp.MustCompile(`spRefusedFreeTry:"([^"]*)"`)
	for _, line := range strings.Split(page[start:start+end], "\n") {
		m := lang.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		tm := try.FindStringSubmatch(line)
		if tm == nil {
			t.Errorf("language %q has no spRefusedFreeTry", m[1])
			continue
		}
		after := tm[1][strings.Index(tm[1], "(STR)")+len("(STR)"):]
		if !strings.Contains(after, "Spotify") {
			t.Errorf("language %q: spRefusedFreeTry does not say to save the Spotify presets again: %q", m[1], tm[1])
		}
		if m[1] == "en" && !strings.Contains(tm[1], "then save your Spotify presets again.") {
			t.Errorf("English spRefusedFreeTry lost the re-save step: %q", tm[1])
		}
	}
}

// Only Spotify can lift its refusal of a Premium account, so when the page is
// certain that is what happened it offers Spotify's contact page and a ready
// message for Spotify support. Certain means the speaker's persistent
// per-account record (accountRefused, not an older agent's audioKeyRefused)
// and a plan reported as exactly Premium; the desktop app's
// refusalSupportFor (spotifyrefused.js) holds the same rule.
var spSupportKeys = []string{
	"spRefusedSupport:", "spRefusedSupportOpen:", "spRefusedSupportCopy:",
	"spRefusedSupportCopied:", "spRefusedSupportMessage:",
}

func TestPhoneSpotifySupportIsTranslatedEverywhere(t *testing.T) {
	page := readIndexHTML(t)
	start := strings.Index(page, "var I18N2 = {")
	if start < 0 {
		t.Fatal("I18N2 dictionary not found")
	}
	end := strings.Index(page[start:], "\n};")
	lang := regexp.MustCompile(`^  ([a-z]{2}):\{`)
	value := regexp.MustCompile(`(spRefusedSupport(?:Open|Copy|Copied|Message)?):"((?:[^"\\]|\\.)*)"`)
	dash := regexp.MustCompile(`[–—]| - `)
	seen := 0
	for _, line := range strings.Split(page[start:start+end], "\n") {
		m := lang.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		seen++
		got := map[string]string{}
		for _, v := range value.FindAllStringSubmatch(line, -1) {
			got[v[1]+":"] = v[2]
		}
		for _, k := range spSupportKeys {
			v, ok := got[k]
			if !ok || strings.TrimSpace(v) == "" {
				t.Errorf("language %q has no %s", m[1], strings.TrimSuffix(k, ":"))
				continue
			}
			if dash.MatchString(v) {
				t.Errorf("language %q: %s has a dash: %q", m[1], strings.TrimSuffix(k, ":"), v)
			}
		}
		msg := got["spRefusedSupportMessage:"]
		for _, fact := range []string{"Premium", "Bose", "SoundTouch", "go-librespot", "2026", `\n\n`} {
			if !strings.Contains(msg, fact) {
				t.Errorf("language %q: the message for Spotify lacks %q", m[1], fact)
			}
		}
	}
	if seen < 12 {
		t.Errorf("only %d languages in I18N2", seen)
	}
}

func TestPhoneSpotifySupportOnlyWhenCertain(t *testing.T) {
	page := readIndexHTML(t)
	for _, want := range []string{
		"var SP_SUPPORT_URL = 'https://support.spotify.com/article/contact-us/';",
		"premium: String(info.product || '').toLowerCase() === 'premium',",
		"sticky: !!info.accountRefused",
		"return !!(ref && ref.sticky && ref.premium && !ref.free);",
		// The render signature carries it, so the notice is rebuilt when it
		// comes or goes, and left alone (toggle open) otherwise.
		"var support = spSupportFor(ref);",
		"[lead].concat(help, [support ? 'support' : '']).join('|')",
		"if (support) more.appendChild(spSupportBlock());",
		// A new tab for Spotify's page, and the plain-http-safe copy.
		"open.target = '_blank';",
		"open.rel = 'noopener';",
		"await shareCopy(T.spRefusedSupportMessage)",
		"copy.textContent = T.spRefusedSupportCopied;",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}
