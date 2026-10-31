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
// its button group beneath the name, and the name breaks between words (#1336).
// The layout itself is measured in e2e/specs/phone-station-wrap.spec.mjs; this
// guards the pieces it is built from.
func TestPhoneRemoteStationRowWrapsButtonsInsteadOfSqueezingName(t *testing.T) {
	row := stationCSSRule(t, ".station")
	if !strings.Contains(row, "flex-wrap:wrap") {
		t.Error(".station must wrap its button group onto a second line")
	}
	name := stationCSSRule(t, ".station .sname")
	if !strings.Contains(name, "flex:1 1 5em") {
		t.Error(".station .sname needs a 5em basis: wider wraps English rows on a 360 to 390px phone, narrower squeezes the name")
	}
	if !strings.Contains(name, "overflow-wrap:break-word") {
		t.Error(".station .sname must break between words")
	}
	if strings.Contains(name, "overflow-wrap:anywhere") {
		t.Error(".station .sname must not use overflow-wrap:anywhere: it renders one letter per line")
	}
	group := stationCSSRule(t, ".station .sbtns")
	for _, want := range []string{"display:flex", "margin-left:auto"} {
		if !strings.Contains(group, want) {
			t.Errorf(".station .sbtns lacks %q", want)
		}
	}
}

// Every .station row builder puts its buttons into the one group stationBtns
// returns. A button appended straight to the row is a flex item of its own, so
// the row breaks at whichever button no longer fits and splits the group, which
// left the favourites star alone on a line.
func TestPhoneRemoteStationRowsKeepButtonsInOneGroup(t *testing.T) {
	rows := strings.Count(indexHTML, "row.className = 'station'")
	groups := strings.Count(indexHTML, "stationBtns(row)") - strings.Count(indexHTML, "function stationBtns(row)")
	if rows == 0 {
		t.Fatal("no .station row builder found in indexHTML")
	}
	if groups != rows {
		t.Errorf("%d .station row builders but %d stationBtns(row) calls: every builder needs exactly one button group", rows, groups)
	}
	if !strings.Contains(indexHTML, "bSave.className = 'btn fill'") {
		t.Error("the Save to key button must be the one that fills and wraps its label, so the group fits in one line")
	}
}
