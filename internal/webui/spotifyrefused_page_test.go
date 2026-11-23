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

// When the page is certain Spotify blocks a Premium account, the notice also
// states Spotify's rule: it no longer enables accounts that have not played on
// such players within the last year, and nothing in STR or the speaker can
// change that. There is no support step any more (decided on discussion
// #1077): Spotify support cannot lift it. Certain means the speaker's
// persistent per-account record (accountRefused, not an older agent's
// audioKeyRefused) and a plan reported as exactly Premium; the desktop app's
// refusalSupportFor (spotifyrefused.js) holds the same rule.
var spRuleText = map[string]string{
	"en": "Spotify has said it no longer enables accounts that have not played on players like this within the last year. Nothing in STR or your speaker can change that.",
	"de": "Spotify hat erklärt, dass es Konten, die im letzten Jahr nicht auf solchen Playern gespielt haben, nicht mehr freischaltet. Daran können weder STR noch dein Lautsprecher etwas ändern.",
	"nl": "Spotify heeft laten weten dat het accounts die het afgelopen jaar niets op zulke spelers hebben afgespeeld, niet meer vrijgeeft. Daar kunnen STR en je speaker niets aan veranderen.",
	"fr": "Spotify a indiqué ne plus activer les comptes qui n'ont rien lu sur ce type de lecteur au cours de la dernière année. Ni STR ni ton enceinte ne peuvent y changer quoi que ce soit.",
	"es": "Spotify ha dicho que ya no habilita cuentas que no hayan reproducido nada en reproductores como este en el último año. Ni STR ni tu altavoz pueden cambiarlo.",
	"pl": "Spotify poinformowało, że nie włącza już kont, które w ciągu ostatniego roku nic nie odtwarzały na takich odtwarzaczach. Ani STR, ani twój głośnik nie mogą tego zmienić.",
	"tr": "Spotify, son bir yıl içinde bu tür oynatıcılarda hiçbir şey çalmamış hesapları artık etkinleştirmediğini açıkladı. Bunu ne STR ne de hoparlörün değiştirebilir.",
	"ar": "أعلنت Spotify أنها لم تعد تفعّل الحسابات التي لم تُشغّل شيئًا على مثل هذه المشغلات خلال العام الماضي. لا يمكن لـ STR ولا لمكبر الصوت لديك تغيير ذلك.",
	"ja": "Spotifyは、過去1年以内にこの種のプレーヤーで再生していないアカウントを今後は有効にしないと表明しています。STRやスピーカー側では変えられません。",
	"lt": "Spotify pranešė, kad nebeįjungia paskyrų, kurios per pastaruosius metus nieko negrojo tokiuose grotuvuose. Nei STR, nei tavo garsiakalbis to pakeisti negali.",
	"lv": "Spotify ir paziņojis, ka vairs neaktivizē kontus, kuros pēdējā gada laikā nekas nav atskaņots šādos atskaņotājos. Ne STR, ne tavs skaļrunis to nevar mainīt.",
	"uk": "Spotify повідомив, що більше не вмикає облікові записи, які протягом останнього року нічого не відтворювали на таких плеєрах. Ні STR, ні твій динамік не можуть цього змінити.",
}

func TestPhoneSpotifyRuleIsTranslatedEverywhere(t *testing.T) {
	page := readIndexHTML(t)
	start := strings.Index(page, "var I18N2 = {")
	if start < 0 {
		t.Fatal("I18N2 dictionary not found")
	}
	end := strings.Index(page[start:], "\n};")
	lang := regexp.MustCompile(`^  ([a-z]{2}):\{`)
	value := regexp.MustCompile(`spRefusedSupport:"((?:[^"\\]|\\.)*)"`)
	gone := regexp.MustCompile(`spRefusedSupport(?:Open|Copy|Copied|Message):`)
	dash := regexp.MustCompile(`[–—]| - `)
	seen := 0
	for _, line := range strings.Split(page[start:start+end], "\n") {
		m := lang.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		seen++
		want, ok := spRuleText[m[1]]
		if !ok {
			t.Errorf("language %q has no agreed text for Spotify's rule", m[1])
		}
		v := value.FindStringSubmatch(line)
		switch {
		case v == nil:
			t.Errorf("language %q has no spRefusedSupport", m[1])
		case ok && v[1] != want:
			t.Errorf("language %q: spRefusedSupport = %q, want %q", m[1], v[1], want)
		case dash.MatchString(v[1]):
			t.Errorf("language %q: spRefusedSupport has a dash: %q", m[1], v[1])
		}
		if k := gone.FindString(line); k != "" {
			t.Errorf("language %q still carries %s", m[1], strings.TrimSuffix(k, ":"))
		}
	}
	if seen < 12 {
		t.Errorf("only %d languages in I18N2", seen)
	}
}

func TestPhoneSpotifyRuleOnlyWhenCertain(t *testing.T) {
	page := readIndexHTML(t)
	for _, want := range []string{
		"premium: String(info.product || '').toLowerCase() === 'premium',",
		"sticky: !!info.accountRefused",
		"return !!(ref && ref.sticky && ref.premium && !ref.free);",
		// The render signature carries it, so the notice is rebuilt when it
		// comes or goes, and left alone (toggle open) otherwise.
		"var support = spSupportFor(ref);",
		"[lead].concat(help, [support ? 'support' : '']).join('|')",
		"if (support) more.appendChild(spSupportBlock());",
		// Just the line.
		"line.className = 'sp-refused-support';",
		"line.textContent = T.spRefusedSupport;",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// Nobody is sent to Spotify support any more: no contact page, no ready
	// message, no buttons.
	for _, gone := range []string{
		"SP_SUPPORT_URL", "support.spotify.com", "sp-refused-actions",
		"spRefusedSupportOpen", "spRefusedSupportCopy", "spRefusedSupportCopied", "spRefusedSupportMessage",
	} {
		if strings.Contains(page, gone) {
			t.Errorf("page still contains %q", gone)
		}
	}
}
