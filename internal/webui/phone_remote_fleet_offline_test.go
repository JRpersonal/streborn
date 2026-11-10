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
	if !strings.Contains(indexHTML, "window.suUpdateInFlight = function() { return runActive() || fleetRunning() || (suBusyState() && (polling || Date.now() - lastHeard < SU_QUIET_CAP_MS)); };") {
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
		"async function load() { await releaseLeft(); var r = await suGet(); if (r && typeof r === 'object') heardFrom(r); else heardNothing(); return r; }",
		"async function ownStatus() { var r = await suGet(); if (r && typeof r === 'object') { heardFrom(r); return r; } heardNothing(); return null; }",
		"function heardFrom(r) { st = r; lostSince = 0; lastHeard = Date.now(); }",
		// render paints an old busy status as silence, not as progress.
		"if (stBusy && lostSince) {",
		"else if (midway || polling || fleetPolling) show(T.sfNoAnswer, false, true);",
		// The poll's window runs from the last answer, and outlasts an
		// outage of hours at the slower pace.
		"while (Date.now() - heard < SU_QUIET_CAP_MS) {",
		"await sleep(Date.now() - heard < 15 * 60 * 1000 ? 2000 : NET_POLL_MS);",
		"var SU_STOP_MS = PEER_MS, SU_QUIET_CAP_MS = 3 * 60 * 60 * 1000;",
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
		"planning = false; planFresh = true;\n    if (r && typeof r === 'object') heardFrom(r);\n    render();",
		"if (r && typeof r === 'object') heardFrom(r); else show(T.suCheckFail, true);",
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
	// then fails; nothing starts it over, and a speaker-driven run updates the
	// serving speaker last without a restart either. So the single card must
	// not promise "it carries on by itself", which only a run this phone
	// drives keeps, in any language.
	if !strings.Contains(indexHTML, "show(runActive() ? T.sfNetWait : T.sfNetWaitOne, false, true);") {
		t.Error("the single-speaker card promises a resume after an outage that only a phone-driven run makes")
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

// The router goes off during a single-speaker update and the phone loses the
// network together with the speaker (discussion #1339, issue 4). The speaker
// gives up after about eleven minutes and keeps its version; the card used to
// stop asking fifteen minutes after the last answer, painted the red "not
// reachable", flipped back to "waiting" every time the phone came back to the
// page, and the Play screen said "not reachable" on top. Pinned here: silence
// mid-download is never red, the card measures the silence from the last
// answer and says the speaker has stopped once it is past the speaker's own
// retries, it keeps asking for hours, and the Play screen leaves the word to
// the card while the update's last word was busy.
func TestPhoneRemoteUpdateCardLongSilence(t *testing.T) {
	js := indexHTML[strings.Index(indexHTML, "(function wireSelfUpdate(){"):strings.Index(indexHTML, "(function wireDiag(){")]
	rd := js[strings.Index(js, "if (stBusy && lostSince) {"):]
	rd = rd[:strings.Index(rd, "else if (st.state === 'checking')")]
	stop := strings.Index(rd, "show(fill(T.suNetStopped, st.current))")
	if stop < 0 {
		t.Fatal("a long silence mid-download does not say the speaker has stopped the update")
	}
	if !strings.Contains(rd, "if (midway && lastHeard && Date.now() - lastHeard >= SU_STOP_MS && !runActive() && !fleetRunning())") {
		t.Error("the stopped line is not measured from the last answer, or speaks over a run")
	}
	// The red line is only reached by a busy state that is not a download.
	quiet := strings.Index(rd, "else if (midway || polling || fleetPolling) show(T.sfNoAnswer, false, true);")
	red := strings.Index(rd, "else show(T.offline, true);")
	if quiet < 0 || red < 0 || quiet > red || stop > quiet {
		t.Error("a silent download can reach the red offline line")
	}
	if !strings.Contains(js, "var SU_STOP_MS = PEER_MS,") {
		t.Error("the stopped line no longer mirrors the twelve minutes of a run")
	}
	bundles := strings.Count(indexHTML, "suHead:\"")
	got := regexp.MustCompile(`suNetStopped:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1)
	if len(got) != bundles {
		t.Errorf("suNetStopped: in %d of %d locales", len(got), bundles)
	}
	for _, m := range got {
		if !strings.Contains(m[1], "{v}") {
			t.Errorf("suNetStopped must name the version the speaker keeps: %q", m[1])
		}
		if strings.ContainsAny(m[1], "–—") || strings.Contains(m[1], " - ") {
			t.Errorf("suNetStopped uses a dash: %q", m[1])
		}
	}
}
