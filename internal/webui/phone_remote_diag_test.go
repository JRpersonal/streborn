package webui

import (
	"regexp"
	"strings"
	"testing"
)

// "Report a problem" on the phone's More screen said "Send it with your
// report" and never said where (discussion #1339). The "Report on GitHub"
// link next to it existed only in UI 2.0. It is now in both layouts, gets its
// label in both, and the card's texts send the file to a bug report on
// GitHub in every language.
func TestPhoneRemoteReportAProblemSaysWhere(t *testing.T) {
	tag := regexp.MustCompile(`<a class="([^"]*)" id="u2Issues" href="https://github.com/JRpersonal/streborn/issues"`).FindStringSubmatch(indexHTML)
	if tag == nil {
		t.Fatal("the Report on GitHub link is gone from the Report a problem card")
	}
	if strings.Contains(tag[1], "ui2-only") {
		t.Error("the Report on GitHub link shows only in UI 2.0")
	}
	static := indexHTML[strings.Index(indexHTML, "set('lblDiag', T.diag);"):]
	static = static[:strings.Index(static, "buildShareButtons();")]
	if !strings.Contains(static, "set('u2lIssues', T.issues);") {
		t.Error("the Report on GitHub link has no label in the current layout")
	}
	bundles := strings.Count(indexHTML, "suHead:\"")
	issues := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^  (\w+):\{[^\n]*?[{,]issues:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1) {
		issues[m[1]] = m[2]
	}
	for _, key := range []string{"diagSum", "diagOk"} {
		vals := regexp.MustCompile(`(?m)^  (\w+):\{[^\n]*?[{,]`+key+`:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1)
		if len(vals) != bundles {
			t.Errorf("%s: in %d of %d locales", key, len(vals), bundles)
		}
		for _, v := range vals {
			if !strings.Contains(v[2], "GitHub") {
				t.Errorf("%s (%s) does not say the file goes to GitHub: %q", key, v[1], v[2])
			}
			if strings.ContainsAny(v[2], "–—") || strings.Contains(v[2], " - ") {
				t.Errorf("%s (%s) uses a dash: %q", key, v[1], v[2])
			}
			// The note after saving names the button below by its label.
			if key == "diagOk" && issues[v[1]] != "" && !strings.Contains(v[2], issues[v[1]]) {
				t.Errorf("diagOk (%s) does not name the button %q: %q", v[1], issues[v[1]], v[2])
			}
		}
	}
	de := regexp.MustCompile(`(?m)^  de:\{[^\n]*?[{,]diagOk:"([^"]*)"`).FindStringSubmatch(indexHTML)
	if de == nil || !strings.Contains(de[1], "häng") {
		t.Error("the German diagOk lost its informal du form")
	}
}
