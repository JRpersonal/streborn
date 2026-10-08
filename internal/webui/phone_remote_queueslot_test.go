package webui

import (
	"strings"
	"testing"
)

// A key holding a music-library folder plays one media-server URL after
// another, so the location the phone page matches keys by never names it, and
// the phone grid lit such a key only from the user's own tap (#1029). The page
// now reads the agent's queue card on its status poll and lights the key the
// card names, like the desktop app does since #1190.
func TestPhoneRemoteLightsTheQueueKey(t *testing.T) {
	start := strings.Index(indexHTML, "function queueSlot(")
	if start < 0 {
		t.Fatal("queueSlot is gone; if it moved, move this test with it")
	}
	fn := indexHTML[start:]
	fn = fn[:strings.Index(fn, "\n}\n")]
	for _, want := range []string{"q.active", `/^queue:slot:(\d+)$/`, "type === 'queue'"} {
		if !strings.Contains(fn, want) {
			t.Errorf("queueSlot lost %q: it must only name a folder key of a running queue", want)
		}
	}
	if !strings.Contains(indexHTML, "playingSlot(loc, lastPresetList) || queueSlot(queue, lastPresetList)") {
		t.Error("markPlaying does not fall back to the queue card")
	}

	rs := indexHTML[strings.Index(indexHTML, "async function refreshStatus("):]
	rs = rs[:strings.Index(rs, "\n}\n")]
	if strings.Count(rs, "markPlaying(") != 1 || !strings.Contains(rs, "markPlaying(nowLoc, queue)") {
		t.Error("refreshStatus must mark the key once, with the queue it read")
	}
	if strings.Count(rs, "currentQueue()") != 1 {
		t.Error("the queue must be read once per status poll, not in a loop of its own")
	}
	if strings.Index(rs, "currentQueue()") > strings.Index(rs, "markPlaying(nowLoc, queue)") {
		t.Error("the key is marked before the queue was read")
	}
}
