package webui

import (
	"strings"
	"testing"
)

// Discussion #1874: the phone page marks the row of the track that plays
// right now in an open music library folder, and in the library search
// results. The row is found by the track's address, the one the agent's queue
// reports as running, and the mark is repainted by every status poll so it
// follows the music while the list stays open. That the queue reply carries
// the address is covered in queue_meta_test.go.
func TestPhoneRemoteMarksThePlayingLibraryTrack(t *testing.T) {
	for _, want := range []string{
		// Both lists key their rows by the track address.
		"ord.tracks.forEach(function(it){\n    var row = document.createElement('div'); row.className = 'station';\n    if (it.url) { row.setAttribute('data-url', it.url); browseMarkRow(row); }",
		"lastLibResults.forEach(function(it, idx){\n    var row = document.createElement('div'); row.className = 'station';\n    if (it.url) { row.setAttribute('data-url', it.url); browseMarkRow(row); }",
		// The queue's running track first, the speaker's location otherwise.
		"if (queue && typeof queue.url === 'string' && queue.url) out.push(queue.url);",
		// Repainted on the status cadence that already runs, no new polling.
		"browseMarkNow(browseNowURLs(playing && upnpPlay, queue, nowLoc));",
		// Not by colour alone, and announced as the current item.
		".station.now .sname::before {",
		"row.setAttribute('aria-current', 'true')",
	} {
		if !strings.Contains(strings.ReplaceAll(indexHTML, "\r\n", "\n"), want) {
			t.Errorf("the phone page lost a part of the playing-track mark: %q", want)
		}
	}
}
