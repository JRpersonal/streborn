package webui

import (
	"regexp"
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
	if !strings.Contains(indexHTML, "window.suUpdateInFlight = function() { return runActive() || fleetRunning() || (polling && suBusyState()); };") {
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

// A router outage in the middle of a single-speaker update from the phone
// (discussion #1339, ST10, v1.0.21). Three things went wrong on the card and
// are pinned here:
//
//   - Ten minutes in, "Not answering right now" turned back into
//     "Downloading, 22%": the phone came back to the page, the card redrew
//     the last status it had, and nothing said that status was old. The card
//     now keeps when the speaker last stopped answering and paints that
//     instead of an old percentage.
//   - The phone gave up fifteen minutes after the START, while the speaker
//     itself was still waiting out the outage, and never read how it ended.
//     It now waits fifteen minutes from the last answer.
//   - After the failed update the card offered "Install the Spotify engine"
//     next to "Update to v1.0.22", which read as if only the engine were
//     missing. A failed download leaves the agent untouched; the update
//     brings the engine back along with it, so the update is the one button.
func TestPhoneRemoteUpdateCardAfterAnOutage(t *testing.T) {
	for _, want := range []string{
		// Every status read keeps lostSince.
		"async function load() { var r = await api(SU); if (r && typeof r === 'object') { st = r; lostSince = 0; } else heardNothing(); return r; }",
		"async function ownStatus() { var r = await api(SU); if (r && typeof r === 'object') { st = r; lostSince = 0; return r; } heardNothing(); return null; }",
		// render paints an old busy status as silence, not as progress.
		"if (stBusy && lostSince) {",
		"else if (polling || fleetPolling) show(T.sfNoAnswer, false, true);",
		// The poll's window runs from the last answer.
		"while (Date.now() - heard < 15 * 60 * 1000) {",
		// The engine-only button stands down while an update is waiting.
		"if (be) be.style.display = (!busy && st.engine === 'missing' && !upd) ? '' : 'none';",
		"var engNote = st.engine === 'missing' ? (upd ? fill(T.suEngineByUpd, st.latest) : T.suEngineMissing) : '';",
		`suEngineByUpd:"The Spotify engine is missing on this speaker. The update to {v} brings it back."`,
		`suEngineByUpd:"Auf diesem Lautsprecher fehlt die Spotify-Engine. Das Update auf {v} bringt sie zurück."`,
		// A row nobody reached is asked about again.
		"replanIfUnreachable();",
		// Back on the page after the poll gave up on a silent speaker: ask
		// again instead of keeping "not reachable" for good.
		"function suResume() { if (!polling && lostSince && suBusyState()) poll(); }",
		"    suResume();\n  });",
		// A plan or check that got an answer clears the silence too.
		"if (r && typeof r === 'object') { st = r; lostSince = 0; }\n    render();\n    await probePeers();",
		"if (r && typeof r === 'object') { st = r; lostSince = 0; } else show(T.suCheckFail, true);",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("update card lost: %s", want)
		}
	}
	// The old start-based window and the old pairing of the engine button.
	for _, gone := range []string{
		"while (Date.now() - start < 15 * 60 * 1000) {\n      await sleep(2000);\n      var before = st;",
		"if (be) be.style.display = (!busy && st.engine === 'missing') ? '' : 'none';",
	} {
		if strings.Contains(indexHTML, gone) {
			t.Errorf("update card still has: %s", gone)
		}
	}
	// The unreachable row no longer sends the owner to the desktop app or to a
	// page they cannot open either.
	for _, m := range regexp.MustCompile(`sfUnreach:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1) {
		if strings.Contains(strings.ToLower(m[1]), "desktop") {
			t.Errorf("sfUnreach still points at the desktop app: %q", m[1])
		}
	}
	// Alone, the speaker retries a lost download for about eight minutes and
	// then fails; nothing starts it over. So the single card must not promise
	// "it carries on by itself", which only a fleet run keeps, in any language.
	if !strings.Contains(indexHTML, "show((runActive() || fleetRunning()) ? T.sfNetWait : T.sfNetWaitOne, false, true);") {
		t.Error("the single-speaker card promises a resume after an outage that only a fleet run makes")
	}
	if a, b := len(regexp.MustCompile(`sfNetWait:"`).FindAllString(indexHTML, -1)), len(regexp.MustCompile(`sfNetWaitOne:"`).FindAllString(indexHTML, -1)); a != b {
		t.Errorf("sfNetWaitOne is in %d languages, sfNetWait in %d", b, a)
	}
	for _, m := range regexp.MustCompile(`suEngineByUpd:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1) {
		if !strings.Contains(m[1], "{v}") {
			t.Errorf("suEngineByUpd must name the version: %q", m[1])
		}
	}
}
