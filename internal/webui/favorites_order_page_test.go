package webui

import (
	"regexp"
	"strings"
	"testing"
)

func pageSection(t *testing.T, from, to string) string {
	t.Helper()
	at := strings.Index(indexHTML, from)
	if at < 0 {
		t.Fatalf("could not find %q in the phone page", from)
	}
	rest := indexHTML[at:]
	if end := strings.Index(rest[len(from):], to); end >= 0 {
		return rest[:len(from)+end]
	}
	return rest
}

// #1519 on the phone: a favourite row has up and down buttons once there is
// more than one favourite, and a move goes to the order endpoint with every
// favourite's key, the way the desktop app and the agent name them.
func TestPhoneRemoteFavouritesMove(t *testing.T) {
	row := pageSection(t, "function renderFavRow(", "function renderUnstarConfirm(")
	for _, want := range []string{
		"if (favStations.length > 1) {",
		"favMoveButton(st, i, -1)",
		"favMoveButton(st, i, 1)",
		"function favMoveButton(",
		"var ids = favStations.map(favKey);",
		"api('/api/favorites/order', 'POST', { ids: ids })",
		"T.favUp",
		"T.favDown",
		"b.disabled = true; return b;",
	} {
		if !strings.Contains(row, want) {
			t.Errorf("the favourite row is missing %q", want)
		}
	}
}

// Every language table of the phone page names the two move buttons.
func TestPhoneRemoteFavouriteMoveStringsInEveryLanguage(t *testing.T) {
	block := pageSection(t, "var I18N2 = {", "\n};")
	langs := regexp.MustCompile(`(?m)^\s*([a-zA-Z-]+):\{`).FindAllStringSubmatchIndex(block, -1)
	if len(langs) < 12 {
		t.Fatalf("found %d language tables", len(langs))
	}
	for i, m := range langs {
		end := len(block)
		if i+1 < len(langs) {
			end = langs[i+1][0]
		}
		table := block[m[0]:end]
		lang := block[m[2]:m[3]]
		for _, key := range []string{"favUp:\"", "favDown:\""} {
			if !strings.Contains(table, key) {
				t.Errorf("%s has no %s", lang, strings.TrimSuffix(key, ":\""))
			}
		}
	}
}
