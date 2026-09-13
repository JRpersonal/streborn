package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The phone remote met the same wall as the desktop app: a save whose station
// already sits on another key is refused, and the page only said "could not
// save". One reporter read that as a bug (#709), another as "I can keep one
// Spotify playlist" (#925). Both save paths on the page now offer the move.

func TestPhoneRemoteOffersToMoveTheStation(t *testing.T) {
	// The refusal body is the interesting part of the answer, and api() drops it
	// on any non-2xx, so both save paths have to read the answer themselves.
	if !strings.Contains(indexHTML, "async function putPresetSlot(slot, body)") {
		t.Fatal("the page needs a preset write that hands back the refusal body")
	}
	if !strings.Contains(indexHTML, "function presetConflict(r)") ||
		!strings.Contains(indexHTML, "b.code !== 'already-on-slot'") {
		t.Fatal("the page must recognise the already-on-another-key refusal")
	}
	if !strings.Contains(indexHTML, "'/api/presets/move', 'POST'") {
		t.Fatal("the move must go to the agent's move endpoint")
	}
	// The hold-to-save and the station picked in the Find tab both offer it.
	for _, path := range []string{
		"var r = await putPresetSlot(slot, body);", // hold-to-save
		"var r = await putPresetSlot(n, body);",    // save from a search result
	} {
		if !strings.Contains(indexHTML, path) {
			t.Errorf("a save path still swallows the refusal: %q not found", path)
		}
	}
	if strings.Count(indexHTML, "renderMoveOffer(") < 3 {
		t.Error("both save paths must render the offer (one definition, two uses)")
	}
	// Declining must change nothing on the speaker: the keep button only clears
	// the offer, it never calls the move.
	keep := indexHTML[strings.Index(indexHTML, "keep.addEventListener"):]
	if i := strings.Index(keep, "\n"); i > 0 {
		keep = keep[:i]
	}
	if strings.Contains(keep, "doPresetMove") {
		t.Errorf("declining the offer must not move anything: %s", keep)
	}
}

// Every new string exists in all twelve locale blocks of the page.
func TestPhoneRemoteLocalesCarryTheMoveOffer(t *testing.T) {
	bundles := strings.Count(indexHTML, "now:\"")
	if bundles == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	for _, key := range []string{"mvTitle", "mvNote", "mvBtn", "mvKeep", "mvDone", "mvFail"} {
		got := len(regexp.MustCompile(key+`:"`).FindAllString(indexHTML, -1))
		if got != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, got)
		}
	}
	// The button has to name the key it moves to, and the note the key the
	// station is on, or neither says what will happen.
	if !regexp.MustCompile(`mvBtn:"[^"]*\{n\}`).MatchString(indexHTML) {
		t.Error("mvBtn must carry the target key number")
	}
	if !regexp.MustCompile(`mvNote:"[^"]*\{from\}`).MatchString(indexHTML) {
		t.Error("mvNote must carry the key the station is already on")
	}
}
