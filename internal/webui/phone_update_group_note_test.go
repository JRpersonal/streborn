package webui

import (
	"regexp"
	"strings"
	"testing"
)

// TestPhoneUpdateCardWarnsAGroupedSpeaker pins the note on the phone's
// single-speaker update (#1339): a speaker in a live group leaves it while it
// restarts for the update, and the card says so before the tap, in every
// locale bundle.
func TestPhoneUpdateCardWarnsAGroupedSpeaker(t *testing.T) {
	bundles := strings.Count(indexHTML, "sfOneNote:\"")
	if bundles != 12 {
		t.Fatalf("found %d locale bundles with sfOneNote, want 12", bundles)
	}
	vals := regexp.MustCompile(`suGrpNote:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1)
	if len(vals) != bundles {
		t.Fatalf("suGrpNote: %d locale bundles but %d keys", bundles, len(vals))
	}
	seen := map[string]bool{}
	for _, v := range vals {
		if strings.TrimSpace(v[1]) == "" {
			t.Error("an empty suGrpNote")
		}
		if seen[v[1]] {
			t.Errorf("suGrpNote %q appears twice: an untranslated copy", v[1])
		}
		seen[v[1]] = true
		if regexp.MustCompile(` [-\x{2013}\x{2014}] `).MatchString(v[1]) {
			t.Errorf("suGrpNote %q uses a dash as punctuation", v[1])
		}
	}
	de := regexp.MustCompile(`de:\{[^\n]*?suGrpNote:"([^"]*)"`).FindStringSubmatch(indexHTML)
	if de == nil || !regexp.MustCompile(`\bdu\b`).MatchString(de[1]) || !strings.ContainsAny(de[1], "äöüÄÖÜß") {
		t.Errorf("German suGrpNote must use du and real umlauts: %v", de)
	}
	for _, want := range []string{
		// Only a live group, never a stereo pair (that survives a restart).
		"zone.grouped && !zone.stereo",
		// Single-speaker update only: the plain update with no fleet, or the
		// owner's "Only this speaker" choice.
		"else if (upd && !fleet() && suGroupNoteDue()) show(T.suGrpNote);",
		"T.sfOneNote + (suGroupNoteDue() ? ' ' + T.suGrpNote : '')",
		// The group state usually arrives after the card painted.
		"suGroupChanged(wasGrp);",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("index.html lacks %q", want)
		}
	}
}

// TestPhoneFleetUpdateWarnsGroupedSpeakers pins the same note for "Update all
// speakers": a run that restarts this speaker or a member of its live group
// breaks that group just as the single update does, so the fleet plan says it
// before the tap, in every locale of the short-line dictionary.
func TestPhoneFleetUpdateWarnsGroupedSpeakers(t *testing.T) {
	start := strings.Index(indexHTML, "var I18N3 = {")
	if start < 0 {
		t.Fatal("I18N3 dictionary not found")
	}
	end := strings.Index(indexHTML[start:], "\n};")
	if end < 0 {
		t.Fatal("I18N3 dictionary not closed")
	}
	dict := indexHTML[start : start+end]
	vals := regexp.MustCompile(`(?m)^  (\w+):\{suGrpAllNote:"([^"]*)"\},$`).FindAllStringSubmatch(dict, -1)
	if len(vals) != 12 {
		t.Fatalf("suGrpAllNote in %d locales, want 12", len(vals))
	}
	seen := map[string]bool{}
	for _, v := range vals {
		if strings.TrimSpace(v[2]) == "" {
			t.Errorf("%s: an empty suGrpAllNote", v[1])
		}
		if seen[v[2]] {
			t.Errorf("%s: suGrpAllNote %q appears twice: an untranslated copy", v[1], v[2])
		}
		seen[v[2]] = true
		if regexp.MustCompile(` [-\x{2013}\x{2014}] `).MatchString(v[2]) {
			t.Errorf("%s: suGrpAllNote uses a dash as punctuation", v[1])
		}
		if v[1] == "de" && (!regexp.MustCompile(`\bdu\b`).MatchString(v[2]) || !strings.ContainsAny(v[2], "äöüÄÖÜß")) {
			t.Errorf("German suGrpAllNote must use du and real umlauts: %q", v[2])
		}
	}
	for _, want := range []string{
		// The fleet plan, not a run and not "Only this speaker".
		"else if (!onlyThis && planFresh && fleet() && st.fleet.mode === 'plan' && suGroupNoteDue(planRows())) show(T.suGrpAllNote);",
		// One gate for both: the fleet variant asks the same live-group rule.
		"function suGroupNoteDue(rows) {",
		// The dictionary is merged into T like the other two.
		"var base3 = I18N3.en, sel3 = I18N3[code] || base3;",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("index.html lacks %q", want)
		}
	}
}
