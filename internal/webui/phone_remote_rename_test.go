package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The phone remote can rename a key (#812): the pencil is on the key, it writes
// with PATCH so the station is untouched, and the 15 s re-read cannot repaint a
// key that is being typed into.
func TestPhoneRemoteCanRenameAPresetKey(t *testing.T) {
	if !strings.Contains(indexHTML, "div.appendChild(renameButton(div, i, nm));") {
		t.Error("a filled preset key must carry the rename button")
	}
	if !strings.Contains(indexHTML, `api('/api/presets/' + slot, 'PATCH', { name: name })`) {
		t.Error("the rename must go out as a PATCH carrying only the name")
	}
	if !strings.Contains(indexHTML, "if (renamingSlot) return;") {
		t.Error("loadPresets must not repaint the grid while a key is being renamed")
	}
	if !strings.Contains(indexHTML, ".preset .ren {") || !strings.Contains(indexHTML, ".preset .renin {") {
		t.Error("the rename pencil and its input need their rules")
	}
	// The pencil sits inside the play tile, which also carries the hold-to-save
	// gesture. Every gesture that reaches the tile starts the station or saves
	// over the key, so the button has to swallow them.
	for _, gesture := range []string{"'click'", "'mousedown'", "'touchstart'", "'keydown'"} {
		re := regexp.MustCompile(`b\.addEventListener\(` + regexp.QuoteMeta(gesture) + `[^;]*stopPropagation`)
		if !re.MatchString(indexHTML) {
			t.Errorf("the rename button must stop %s from reaching the key", gesture)
		}
	}
}

// The rename strings exist in every locale bundle of the page, so the pencil is
// never labelled in English on a German or Japanese phone.
func TestPhoneRemoteLocalesCarryRenameStrings(t *testing.T) {
	bundles := strings.Count(indexHTML, "tPlay:\"")
	if bundles == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	for _, key := range []string{"ren", "renSave", "renCancel", "renFail"} {
		got := len(regexp.MustCompile(`\b`+key+`:"`).FindAllString(indexHTML, -1))
		if got != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, got)
		}
	}
}
