package webui

import (
	"strings"
	"testing"
)

// stationCSSRule returns the declaration block of the rule whose line starts
// with sel followed by " {".
func stationCSSRule(t *testing.T, sel string) string {
	t.Helper()
	i := strings.Index(indexHTML, "\n"+sel+" {")
	if i < 0 {
		t.Fatalf("rule %q not found in indexHTML", sel)
	}
	rest := indexHTML[i+1:]
	end := strings.Index(rest, "}")
	if end < 0 {
		t.Fatalf("rule %q is not closed", sel)
	}
	return rest[:end]
}

// A search result row on the Find tab: with the name beside three fixed-width
// buttons, French ("Enregistrer sur touche"), a 360px phone in German, or a
// larger text size squeezed the name to one letter per line. The row now wraps
// the buttons beneath the name, and the name breaks between words (#1336).
func TestPhoneRemoteStationRowWrapsButtonsInsteadOfSqueezingName(t *testing.T) {
	row := stationCSSRule(t, ".station")
	if !strings.Contains(row, "flex-wrap:wrap") {
		t.Error(".station must wrap its buttons onto a second line")
	}
	name := stationCSSRule(t, ".station .sname")
	if !strings.Contains(name, "flex:1 1 8em") {
		t.Error(".station .sname needs an 8em basis so the buttons wrap before the name gets too narrow")
	}
	if !strings.Contains(name, "overflow-wrap:break-word") {
		t.Error(".station .sname must break between words")
	}
	if strings.Contains(name, "overflow-wrap:anywhere") {
		t.Error(".station .sname must not use overflow-wrap:anywhere: it renders one letter per line")
	}
}
