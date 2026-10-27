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
