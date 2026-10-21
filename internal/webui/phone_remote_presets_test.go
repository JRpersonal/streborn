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

// The phone remote's keys show their cover (#1312): lazily loaded, healed of
// box-form art the phone cannot reach, never DuckDuckGo's grey "no icon"
// chevron, and a key whose image fails is the plain text tile again.
func TestPhoneRemotePresetKeysShowCover(t *testing.T) {
	for _, want := range []string{
		// The tile builds the image from the key's art and marks itself.
		"var art = presetArtImg(presetArtCands(p));",
		"div.classList.add('has-art');",
		// Library album or folder keys fall back to the first track's cover.
		"p.type === 'queue' && Array.isArray(p.items)",
		// The agent's /art?u= wrapper is unwrapped; loopback and the
		// /icon.png stand-in are dropped; DuckDuckGo entries are dropped.
		"u.pathname === '/art' && u.searchParams.get('u')",
		"u.hostname === '127.0.0.1' || u.hostname === 'localhost' || u.pathname === '/icon.png'",
		"u.hostname === 'icons.duckduckgo.com'",
		// Lazy, and the failure path restores the text-only tile.
		"img.loading = 'lazy';",
		"tile.classList.remove('has-art'); tile.removeChild(img);",
		// The number and name stay visible beside the image.
		".preset.has-art {",
		".preset .art {",
		// A cover filled in after the save repaints the grid.
		"(p.art || '')",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("phone remote cover art: index.html lacks %q", want)
		}
	}
	// No inline onerror attribute: the handler is assigned in script.
	if strings.Contains(indexHTML, `onerror="`) {
		t.Error("phone remote uses an inline onerror attribute")
	}
}

// All six keys on the phone remote are one size. The grid's rows used to be
// sized by their content, so a key whose name wrapped (or a Pandora key with
// its service line) made its row taller than the rest: "keys 1 and 2 are
// bigger than the four below". Rows are 1fr and columns minmax(0,1fr) now,
// the note row above the keys keeps its own height, and the rename editor
// grows only its own row.
func TestPhoneRemotePresetKeysAreEqualSize(t *testing.T) {
	for _, want := range []string{
		"#presets { grid-template-columns:repeat(2,minmax(0,1fr)); grid-auto-rows:1fr; }",
		"#presets.has-note { grid-template-rows:auto; }",
		"#presets:has(.renaming) { grid-auto-rows:auto; }",
		"grid.classList.toggle('has-note', !list.length);",
		"grid.classList.add('has-note');",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("phone remote preset grid is missing %q", want)
		}
	}
}
