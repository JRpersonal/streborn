package webui

import (
	"strings"
	"testing"
)

// The phone page's general offline state must stand down while a fleet update
// is in flight. A tester with three ST10s switched the router off for eleven
// minutes during "Update all speakers" (discussion #1339): about eight minutes
// in, the page showed the Play screen with "The speaker is not reachable right
// now" although the run was still waiting, and the summary later was right.
// Two things make that happen, and both are pinned here: the status poll's
// failure counter painted the offline state without asking whether a run was
// waiting, and a reload of the page (a phone drops a background tab) opened
// on Play with the run restored but out of sight.
func TestPhoneRemoteOfflineStateStandsDownDuringFleetUpdate(t *testing.T) {
	// The self-update card tells the status poll whether a run is in flight,
	// this phone's own run or one the serving speaker drives.
	if !strings.Contains(indexHTML, "window.suUpdateInFlight = function() { return runActive() || fleetRunning(); };") {
		t.Error("the self-update card no longer tells the status poll that a fleet update is in flight")
	}
	// The poll's failure path asks before it paints "not reachable".
	const guard = "if (netFails >= NET_FAIL_QUIET && !(window.suUpdateInFlight && window.suUpdateInFlight())) {"
	at := strings.Index(indexHTML, guard)
	if at < 0 {
		t.Fatal("the status poll paints the offline state without checking for a fleet update in flight")
	}
	// The offline painting itself is still there, right behind the guard: the
	// general case (a speaker that is simply unplugged) must keep announcing
	// itself once the run is over.
	rest := indexHTML[at : at+400]
	if !strings.Contains(rest, "setNow(T.offline || 'The speaker is not reachable right now.', '');") || !strings.Contains(rest, "sc.classList.add('offline');") {
		t.Error("the general offline state behind the guard changed; a speaker that is really gone must still be announced")
	}
	// A run restored after a reload opens on the update card, not on Play.
	if !strings.Contains(indexHTML, "if (runActive()) { showTab('More'); load().then(function(){ render(); drive(); }); }") {
		t.Error("a reload in the middle of a run no longer opens on the update card")
	}
	// The card's own waiting word, the one that stays visible instead.
	if !strings.Contains(indexHTML, `sfNoAnswer:"Not answering right now. Waiting for it."`) {
		t.Error("the update card lost its waiting word for a speaker that does not answer")
	}
}
