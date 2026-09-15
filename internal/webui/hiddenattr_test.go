package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The hidden attribute has to actually hide.
//
// The browser's own rule for it is a plain display:none, so any declared
// display outranks it. This page declares display on .row (grid) and on
// button.btn (flex), so three elements written as hidden in the markup were on
// screen the whole time: browseNav, btnBrowseMore and btnSleepOff. The
// sleep-timer row at the bottom of the stylesheet already carried a
// one-element patch for the same thing, written before anyone saw it was
// general (#709).
func TestTheHiddenAttributeWins(t *testing.T) {
	page := readIndexHTML(t)
	css := page[:strings.Index(page, "</style>")]

	rule := regexp.MustCompile(`(?m)^\[hidden\]\s*\{[^}]*display\s*:\s*none\s*!important`)
	if !rule.MatchString(css) {
		t.Fatal("the general [hidden] rule is gone; an element marked hidden inside a .row or a .btn is visible again")
	}

	// It has to come before the rules it overrides in source order too, since
	// !important ties are broken by order.
	at := rule.FindStringIndex(css)[0]
	for _, later := range []string{".row { display:grid", "button.btn, a.btn { display:flex"} {
		if i := strings.Index(css, later); i >= 0 && i < at {
			t.Errorf("%q is declared before the [hidden] rule", later)
		}
	}
}

// The elements that were actually broken, so a future markup change that drops
// the attribute is caught rather than silently un-hiding them again.
func TestTheElementsThatWereVisibleStayMarkedHidden(t *testing.T) {
	page := readIndexHTML(t)
	for _, id := range []string{"browseNav", "btnBrowseMore", "btnSleepOff"} {
		tag := elementTag(t, page, id)
		if !strings.Contains(tag, " hidden") {
			t.Errorf("%s is no longer marked hidden: %s", id, tag)
		}
	}
}

// elementTag returns the opening tag carrying id="<id>".
func elementTag(t *testing.T, page, id string) string {
	t.Helper()
	re := regexp.MustCompile(`<[^>]*\bid="` + regexp.QuoteMeta(id) + `"[^>]*>`)
	m := re.FindString(page)
	if m == "" {
		t.Fatalf("element %s is gone from the page; if it moved, move this test with it", id)
	}
	return m
}
