package sticksetup

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The country-code -> Bose sysLanguage mapping exists twice: in Go
// (SysLanguageForCountry, used by the desktop app) and in shell
// (lang_int_for_cc in usb-stick/run.sh, used on the box for region-only
// sticks). They must agree or a region-only install picks a different display
// language than the app. This test parses BOTH tables out of their source files
// and fails on any drift, so neither copy can be edited in isolation.

// parseGoTable extracts the cc->int map from SysLanguageForCountry by calling
// the live function over the union of country codes both tables mention. We
// discover the code set from the shell table (which lists every code) plus the
// Go source's case labels, so a code added to only one side is caught.
func goCases(t *testing.T) map[string]int {
	src, err := os.ReadFile("sticksetup.go")
	if err != nil {
		t.Fatalf("read sticksetup.go: %v", err)
	}
	body := between(string(src), "func SysLanguageForCountry(cc string) int {", "\n}")
	out := map[string]int{}
	caseRe := regexp.MustCompile(`case ((?:"[A-Z]{2}",?\s*)+):\s*\n\s*return (\d+)`)
	for _, m := range caseRe.FindAllStringSubmatch(body, -1) {
		val, _ := strconv.Atoi(m[2])
		for _, cc := range regexp.MustCompile(`"([A-Z]{2})"`).FindAllStringSubmatch(m[1], -1) {
			out[cc[1]] = val
		}
	}
	return out
}

func shellCases(t *testing.T) map[string]int {
	src, err := os.ReadFile("../usb-stick/run.sh")
	if err != nil {
		t.Fatalf("read run.sh: %v", err)
	}
	body := between(string(src), "lang_int_for_cc() {", "\n}")
	out := map[string]int{}
	// Lines like:  DK|GL|FO) echo 1 ;;   (skip the *) default)
	lineRe := regexp.MustCompile(`(?m)^\s*([A-Z|]+)\)\s*echo (\d+)`)
	for _, m := range lineRe.FindAllStringSubmatch(body, -1) {
		val, _ := strconv.Atoi(m[2])
		for _, cc := range strings.Split(m[1], "|") {
			out[cc] = val
		}
	}
	return out
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	s = s[i+len(start):]
	j := strings.Index(s, end)
	if j < 0 {
		return s
	}
	return s[:j]
}

func TestLangTableParity(t *testing.T) {
	goMap := goCases(t)
	shMap := shellCases(t)
	if len(goMap) < 20 || len(shMap) < 20 {
		t.Fatalf("parser regression: goMap=%d shMap=%d (expected >=20 each)", len(goMap), len(shMap))
	}

	// Every Go code must exist in shell with the same value, exercised through
	// the live Go function (not just the parsed source) so a refactor of the Go
	// switch is also covered.
	for cc, want := range goMap {
		if got := SysLanguageForCountry(cc); got != want {
			t.Errorf("Go SysLanguageForCountry(%q)=%d but its own case says %d", cc, got, want)
		}
		if shMap[cc] != want {
			t.Errorf("%q: Go=%d shell=%d (drift)", cc, want, shMap[cc])
		}
	}
	// And no shell code is missing from Go.
	for cc, want := range shMap {
		if goMap[cc] != want {
			t.Errorf("%q: shell=%d Go=%d (shell has a code Go lacks or disagrees)", cc, want, goMap[cc])
		}
	}
}
