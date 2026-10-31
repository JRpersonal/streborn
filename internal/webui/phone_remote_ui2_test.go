package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The phone remote's UI 2.0 preview (docs/UI-2.0.md, phase 5) sits behind its
// own switch, Aa > Layout, stored in localStorage str.ui2Preview. With the
// switch off the page must be exactly the current remote, so these pin what
// the preview adds AND that the lines the current layout depends on are
// still there verbatim. The behaviour is covered in the browser by
// e2e/specs/phone-ui2-*.spec.mjs and src/phoneui2switch.test.js.

// TestPhoneRemoteUI2SwitchIsOffByDefault: on only for exactly '1', every
// storage access guarded, applied with the theme before the body paints.
func TestPhoneRemoteUI2SwitchIsOffByDefault(t *testing.T) {
	head := indexHTML[:strings.Index(indexHTML, "</head>")]
	for _, want := range []string{
		"var UI2_KEY='str.ui2Preview';",
		"try { return localStorage.getItem(UI2_KEY)==='1'; } catch (e) {}",
		"window.ui2Set=function(on){",
		"window.ui2CanKeep=function(){",
		// Keeping the choice needs a write, not only a read.
		"localStorage.setItem(probe,'1');",
		"if (window.ui2Get()) el.classList.add('ui2'); else el.classList.remove('ui2');",
		"if (location.hash==='#ui2') {",
		"history.replaceState(null,'',location.pathname+location.search);",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("the <head> display-preferences script lost %q", want)
		}
	}
	// Every preview-only element is hidden unless html.ui2 is set.
	if !strings.Contains(indexHTML, "html:not(.ui2) .ui2-only { display:none !important; }") {
		t.Error("the .ui2-only elements are no longer hidden in the current layout")
	}
	// The Layout row offers both, Current pressed until the preview is on.
	for _, want := range []string{
		`<div class="a11y-group" id="a11yLayoutGroup">`,
		`<button type="button" data-layout="classic" aria-pressed="true">Current</button>`,
		`<button type="button" data-layout="ui2" aria-pressed="false">UI 2.0 preview</button>`,
		"if (lyGroup && !(window.ui2CanKeep && window.ui2CanKeep())) lyGroup.hidden = true;",
		"if (window.ui2Set && window.ui2Set(on)) { location.reload(); return; }",
		// A write refused after the menu was shown takes the row away.
		"if (lyGroup) lyGroup.hidden = true;",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("the Aa menu's Layout row lost %q", want)
		}
	}
	// The cards move once, from the boot path, never at module scope.
	if !strings.Contains(indexHTML, "if (ui2On) mountUi2();\napplyStaticI18n(); syncTabbarSpace();") {
		t.Error("mountUi2 is no longer called once from the boot line")
	}
}

// TestPhoneRemoteUI2LayoutLabelsInEveryLocale: the Aa menu dictionary carries
// the Layout row in each of its languages.
func TestPhoneRemoteUI2LayoutLabelsInEveryLocale(t *testing.T) {
	start := strings.Index(indexHTML, "  var A11Y_I18N = {")
	if start < 0 {
		t.Fatal("A11Y_I18N not found")
	}
	block := indexHTML[start : start+strings.Index(indexHTML[start:], "\n  };")]
	locales := regexp.MustCompile(`(?m)^    ([a-z]{2}):\{`).FindAllStringSubmatch(block, -1)
	if len(locales) != 12 {
		t.Fatalf("A11Y_I18N has %d locales, want 12", len(locales))
	}
	for _, key := range []string{"ly", "lc", "ln"} {
		got := len(regexp.MustCompile(`[,{]`+key+`:"[^"]+"`).FindAllString(block, -1))
		if got != len(locales) {
			t.Errorf("A11Y_I18N %s: %d locales but %d labels", key, len(locales), got)
		}
	}
}

// TestPhoneRemoteUI2LocalesCarryTheNewKeys: every new preview string is in
// every phone locale, like TestPhoneRemoteLocalesCarryTheNewKeys.
func TestPhoneRemoteUI2LocalesCarryTheNewKeys(t *testing.T) {
	bundles := strings.Count(indexHTML, "now:\"")
	if bundles == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	start := strings.Index(indexHTML, "var I18N4 = {")
	if start < 0 {
		t.Fatal("the preview dictionary I18N4 is missing")
	}
	dict := indexHTML[start : start+strings.Index(indexHTML[start:], "\n};")]
	for _, key := range []string{
		"tListen", "tSpk2", "keysOn", "findSumOn", "offlineOn", "tryAgain", "noKeysOn", "onlyOne",
		"helpInfo", "updates", "issues", "source", "worldMap", "supportLead", "about", "privacy",
		"imprint", "offlineWord", "withNames", "stereoTag",
	} {
		got := len(regexp.MustCompile(`[,{]`+key+`:"[^"]+"`).FindAllString(dict, -1))
		if got != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, got)
		}
	}
	// No en or em dash as punctuation in the new strings.
	if strings.ContainsAny(dict, "–—") {
		t.Error("the preview dictionary uses an en or em dash")
	}
	// German says du.
	de := regexp.MustCompile(`(?m)^  de:\{.*$`).FindString(dict)
	if strings.Contains(de, " Sie ") || strings.Contains(de, "Ihr") {
		t.Error("the German preview strings must use du")
	}
	// pickDict merges the dictionary.
	if !strings.Contains(indexHTML, "var base4 = I18N4.en, sel4 = I18N4[code] || base4;") {
		t.Error("pickDict does not merge the preview dictionary")
	}
}

// TestPhoneRemoteUI2KeepsThePinnedLines: the preview only adds branches next
// to these; the current layout's behaviour hangs on them unchanged.
func TestPhoneRemoteUI2KeepsThePinnedLines(t *testing.T) {
	for _, want := range []string{
		"if (runActive()) { showTab('More'); load().then(function(){ render(); drive(); }); }",
		"if (name === 'More') loadDisplayCard();",
		"setNow(T.offline || 'The speaker is not reachable right now.', '');",
		"if (netFails >= NET_FAIL_QUIET && !(window.suUpdateInFlight && window.suUpdateInFlight())) {",
		`grid.innerHTML = '<div class="grid-note">' + escapeHtml(T.offline) + '</div>';`,
		`grid.innerHTML = '<div class="grid-note">' + escapeHtml(T.noPresets) + '</div>';`,
		"    tab.hidden = !has;",
		"  peersList = list || [];",
		"else { a.setAttribute('aria-disabled', 'true'); a.style.opacity = '0.5'; a.style.pointerEvents = 'none'; a.title = (p.name || p.url); }",
		"  set('tlPlay', T.tPlay); set('tlBrowse', T.tFind); set('tlSpeakers', T.tSpk); set('tlMore', T.tMore);",
		"  set('lblPresets', T.presets); set('lblPeers', T.swSpk);",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("a line the current layout depends on changed: %q", want)
		}
	}
	// The preview's additions sit right beside them.
	for _, want := range []string{
		"  if (ui2On && name === 'Speakers') loadDisplayCard();\n",
		"      if (sc) sc.classList.add('offline');\n      if (ui2On) ui2ShowOffline(true);\n",
		"  if (ui2On && list == null) return;\n",
		"  if (ui2On) { ui2PeersKnown = true; ui2PaintPeers(); return; }\n",
		"  if (ui2On) ui2PeerLook(a, p, online);\n",
		"function showPlayNotice(msg) {\n  if (ui2On) ui2FindNotice(msg);\n",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("the preview branch %q is missing", want)
		}
	}
	// The preview's offline branch runs only behind the fleet-update guard.
	guard := strings.Index(indexHTML, "if (netFails >= NET_FAIL_QUIET && !(window.suUpdateInFlight && window.suUpdateInFlight())) {")
	branch := strings.Index(indexHTML, "if (ui2On) ui2ShowOffline(true);")
	if guard < 0 || branch < guard || branch-guard > 400 {
		t.Error("the preview's offline state is not behind the fleet-update guard")
	}
	// The current layout keeps "Could not start" for an unhandled refusal.
	if !strings.Contains(indexHTML, "function findFail(res) {\n  if (!ui2On) return T.cantStart;") {
		t.Error("findFail must answer the current layout's plain line when the preview is off")
	}
}

// TestPhoneRemoteUI2SupportLinksOnlyInHelp: the donate links live only in
// the sponsors card on More (Help and info), never in a banner or a success
// moment, in either layout.
func TestPhoneRemoteUI2SupportLinksOnlyInHelp(t *testing.T) {
	more := strings.Index(indexHTML, `<section class="panel" id="panelMore"`)
	end := strings.Index(indexHTML, "</main>")
	if more < 0 || end < more {
		t.Fatal("More panel not found")
	}
	for _, link := range []string{"github.com/sponsors/JRpersonal", "ko-fi.com/streborn", "paypal.me/JR31337"} {
		if n := strings.Count(indexHTML, link); n != 1 {
			t.Errorf("%s appears %d times, want once", link, n)
		}
		if at := strings.Index(indexHTML, link); at < more || at > end {
			t.Errorf("%s is outside the More panel", link)
		}
	}
}
