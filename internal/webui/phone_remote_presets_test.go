package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The phone remote's preset grid (#882): a failed read must not paint six
// "empty" keys, which is exactly what an empty store paints, so an
// unreachable speaker and a speaker with no presets could not be told apart.
func TestPhoneRemoteFailedPresetReadShowsOfflineNotEmptyKeys(t *testing.T) {
	if strings.Contains(indexHTML, "const list = raw || [];") {
		t.Fatal("loadPresets still turns a failed read into an empty list (#882 regression)")
	}
	if !strings.Contains(indexHTML, `grid.innerHTML = '<div class="grid-note">' + escapeHtml(T.offline) + '</div>';`) {
		t.Fatal("loadPresets must show the offline note when the first read fails and nothing is painted")
	}
	if !strings.Contains(indexHTML, `grid.innerHTML = '<div class="grid-note">' + escapeHtml(T.noPresets) + '</div>';`) {
		t.Fatal("an empty store must be named as such above the six empty keys")
	}
	if !strings.Contains(indexHTML, ".grid-note {") {
		t.Fatal("the grid note needs its full-width rule")
	}
}

// The new string exists in every locale bundle.
func TestPhoneRemoteLocalesCarryNoPresets(t *testing.T) {
	bundles := strings.Count(indexHTML, "now:\"")
	if bundles == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	got := len(regexp.MustCompile(`noPresets:"`).FindAllString(indexHTML, -1))
	if got != bundles {
		t.Errorf("noPresets: %d locale bundles but %d keys", bundles, got)
	}
}
