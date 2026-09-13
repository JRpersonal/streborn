package webui

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The speaker's own page had an "Undo stereo pair" button that asked the agent
// to dissolve a GROUP. Since #843 stopped Ungroup from tearing pairs apart, the
// agent deliberately leaves a pair alone unless the caller says it means the
// pair, so the button asked for the one thing the agent refuses. A reporter
// pressed it thirty times against a live pair, saw nothing happen and nothing
// said, and undid the pair from the desktop app in the end (#936).
//
// The agent side is covered elsewhere; nothing there can catch a caller that
// omits the flag, which is exactly what happened. So the caller is pinned here.
func TestRemoteUndoSendsThePairFlag(t *testing.T) {
	page := readIndexHTML(t)

	call := regexp.MustCompile(`api\('/api/box/zone'[^;]*'DELETE'\)`).FindString(undoHandler(t, page))
	if call == "" {
		t.Fatal("the undo button's zone DELETE call is gone; if it moved, move this test with it")
	}
	if !strings.Contains(call, "stereo=1") {
		t.Errorf("the undo button does not send the pair flag, so a stereo pair survives it: %s", call)
	}
	if !strings.Contains(call, "stereo ?") && !strings.Contains(call, "stereo?") {
		t.Errorf("the pair flag is not conditional, so undoing a plain group would claim a pair: %s", call)
	}
}

// A no-op that reports success is worse than an error, because the user repeats
// it. The agent answers 200 {"ok":true,"nothing":true} when it declines to act,
// and the page used to check only ok === false.
func TestRemoteUndoReportsARefusal(t *testing.T) {
	page := readIndexHTML(t)

	after := undoHandler(t, page)

	if !strings.Contains(after, "res.nothing") {
		t.Error("a declined undo still reads as success: the handler ignores the answer's nothing flag")
	}
	if !strings.Contains(after, "unpairFail") {
		t.Error("a failed pair undo shows the GROUP wording on a card that says PAIR")
	}
}

// Every language the page speaks needs the new note, or a failure is silent
// again for everyone except English.
func TestUnpairFailIsTranslatedEverywhere(t *testing.T) {
	page := readIndexHTML(t)
	blocks := regexp.MustCompile(`(?m)^  ([a-zA-Z-]+):\{`).FindAllStringSubmatch(page, -1)
	if len(blocks) < 10 {
		t.Fatalf("found %d language blocks, expected the full set", len(blocks))
	}
	seen := map[string]bool{}
	for _, b := range blocks {
		seen[b[1]] = true
	}
	lines := strings.Split(page, "\n")
	for _, line := range lines {
		m := regexp.MustCompile(`^  ([a-zA-Z-]+):\{`).FindStringSubmatch(line)
		if m == nil || !seen[m[1]] {
			continue
		}
		if !strings.Contains(line, "ungroupFail:") {
			continue // a block that never had the group note needs no pair note
		}
		if !strings.Contains(line, "unpairFail:") {
			t.Errorf("language %q has ungroupFail but no unpairFail", m[1])
		}
	}
}

func readIndexHTML(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("assets/index.html")
	if err != nil {
		t.Fatalf("read the served page: %v", err)
	}
	return string(b)
}

// undoHandler returns the click handler of the undo button and nothing else.
// The page has a second zone DELETE in leavePeer, and that one is right as it
// is: removing the last member of a group IS a group operation, and the pair
// card offers no per-member removal (the leave buttons are gated on
// !zone.stereo). Matching the first DELETE in the file tests the wrong one.
func undoHandler(t *testing.T, page string) string {
	t.Helper()
	// The id is read twice: once to hide the button on a group follower, and
	// once by the click handler. The handler is the later one.
	at := strings.LastIndex(page, "getElementById('btnUngroup')")
	if at < 0 {
		t.Fatal("the undo button is gone from the page")
	}
	end := at + 2600
	if end > len(page) {
		end = len(page)
	}
	return page[at:end]
}
