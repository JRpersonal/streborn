package webui

import (
	"strings"
	"testing"
)

// On the phone page of a grouped speaker the volume slider sat at 0 for about
// 10 to 15 s after the page opened (Jens on an ST30, 2026-11-23). loadZone
// adopted the group level only when the scope was already the group, but on
// the first read the scope was still 'one' and switched to the group only
// afterwards; from then on the speaker's own reading was not allowed to fill
// the slider either, so it waited for the slow zone poll. The scope has to be
// settled before the slider adopts the group level.
func TestPhoneGroupVolumeShownOnFirstZoneRead(t *testing.T) {
	a := strings.Index(indexHTML, "async function loadZone() {")
	if a < 0 {
		t.Fatal("loadZone not found")
	}
	end := strings.Index(indexHTML[a:], "\n}\n")
	if end < 0 {
		t.Fatal("end of loadZone not found")
	}
	body := indexHTML[a : a+end]

	adopt := strings.Index(body, "if (volScope !== 'one' && typeof shown === 'number'")
	if adopt < 0 {
		t.Fatal("the group-level adopt is gone from loadZone")
	}
	// The grouped path: the last applyScopeUI before the adopt must exist, and
	// none may come after it (that was the bug's order).
	scope := strings.LastIndex(body[:adopt], "applyScopeUI();")
	if scope < 0 || scope < strings.Index(body, "zone.grouped = true;") {
		t.Fatal("loadZone must settle the volume scope (applyScopeUI) after marking the group and before adopting the group level")
	}
	if strings.Contains(body[adopt:], "applyScopeUI();") {
		t.Fatal("applyScopeUI runs after the adopt again: on the first read the slider would stay at 0 until the next zone poll")
	}
}
