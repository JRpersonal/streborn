package webui

import (
	"regexp"
	"strings"
	"testing"
)

// #1339 4c: "Update all speakers" from the phone, router unplugged during the
// download. The single-speaker card ended in about ten minutes; the run waited
// two and a half hours, because its only bound moved with every minute the
// phone was locked. The run now ends on wall-clock silence, stops at the first
// network failure for every speaker still to come, and gives up on serving
// speaker calls that hang.
func TestPhoneFleetRunEndsOnANetworkOutage(t *testing.T) {
	page := readIndexHTML(t)
	js := page[strings.Index(page, "(function wireSelfUpdate(){"):strings.Index(page, "(function wireDiag(){")]
	for _, want := range []string{
		// Wall-clock silence, never shifted by the visibility handler.
		"var SILENT_MS = 12 * 60 * 1000, SILENT_SEEN_MS = 3 * 60 * 1000;",
		"if (!row.heardAt) row.heardAt = Date.now();",
		"t - row.heardAt >= SILENT_MS && t - row.silentSeenAt >= SILENT_SEEN_MS",
		"row.heardAt = Date.now(); row.silentSeenAt = 0;",
		// The page has to see the silence itself again after being away.
		"if (row.backAt) row.backAt += away; row.silentSeenAt = 0; }",
		// A restart after the network wait measures afresh.
		"heardAt: 0, silentSeenAt: 0 });",
		// One end state instead of walking the other speakers into it.
		"if (row.state === 'failed' && netErr(row.reason)) { netStop = row.reason; break; }",
		"if (!netStop && self && (self.state === 'waiting' || self.state === 'updating')) {",
		// Serving speaker calls with a time limit.
		"{ action: 'run-claim', token: run.token }, 10000);",
		"{ action: 'run-end', token: run.token }, 10000);",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the phone fleet run lost %q", want)
		}
	}
	if strings.Contains(js, "row.heardAt +=") || strings.Contains(js, "row.silentSeenAt +=") {
		t.Error("the silence clock must not move with the time the page was hidden")
	}
	if !strings.Contains(page, "async function apiDetail(path, method, body, ms) {") {
		t.Error("apiDetail lost its optional time limit")
	}

	// The silence failure reads as a network failure, so the card says "try
	// again when your network is back" and the run stops on it.
	m := regexp.MustCompile(`var NET_RE = /(.*)/i;`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("NET_RE not found")
	}
	netRE := regexp.MustCompile(`(?i)` + strings.ReplaceAll(m[1], `\/`, `/`))
	for _, msg := range []string{"no network answer for 124 minutes in the middle of the download", "no network answer: no answer"} {
		if !netRE.MatchString(msg) {
			t.Errorf("%q is not taken for a network failure", msg)
		}
	}
	for _, want := range []string{
		"function netReason(msg)",
		"failRow(row, netReason(res.fail));",
		"if ((row.tries || 0) >= NET_TRIES) { failRow(row, netReason(res.fail)); return false; }",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("a row that waited out the network in vain must fail as a network failure: lost %q", want)
		}
	}
}

// #1339 issue 4c, the rest of it: through one Wi-Fi outage "Update all" on the
// phone could still wait about an hour (two tries of a fifteen-minute wait,
// a deadline pushed out three minutes on every "no internet" answer without
// a cap, and the hidden time on top), while a single-speaker update says it
// has stopped after SU_STOP_MS. The run now has one network budget on the wall
// clock from the first network failure, across retries and hidden time.
func TestPhoneFleetRunHasOneNetworkBudget(t *testing.T) {
	page := readIndexHTML(t)
	js := page[strings.Index(page, "(function wireSelfUpdate(){"):strings.Index(page, "(function wireDiag(){")]
	for _, want := range []string{
		"var RUN_NET_MS = 15 * 60 * 1000;",
		// On the wall clock, kept in the saved run, cleared by a working network.
		"function netBegin(at) { if (run && !run.netSince) { run.netSince = at || Date.now(); saveRun(); } }",
		"function netOk() { if (run && run.netSince) { run.netSince = 0; saveRun(); } }",
		"function netLeft() { return run && run.netSince ? run.netSince + RUN_NET_MS - Date.now() : RUN_NET_MS; }",
		// A silence counts from the last answer.
		"net: true, since: row.heardAt };",
		"netBegin(res.since);",
		// The retry wait never runs past the budget, and an exhausted budget
		// is not waited out again.
		"var until = Math.min(Date.now() + NET_MS, run.netSince + RUN_NET_MS);",
		"if (netLeft() <= 0) { failRow(row, netReason(netGone())); return false; }",
		// The speaker's "no internet" answers extend the row only up to the
		// budget, then end it as a network failure.
		"if (netLeft() <= 0) return { fail: netReason(netGone()), net: true };",
		"row.deadline = Math.max(row.deadline, Math.min(Date.now() + 3 * 60 * 1000, run.netSince + RUN_NET_MS));",
		// Healthy answers and a finished speaker end the outage.
		"if (now.phase !== 'network' && now.state !== 'failed') netOk();",
		"if (row.state === 'done') netOk();",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the phone fleet run lost %q", want)
		}
	}
	if strings.Contains(js, "row.deadline = Math.max(row.deadline, Date.now() + 3 * 60 * 1000);") {
		t.Error("the uncapped three-minute extension is back")
	}
	if strings.Contains(js, "run.netSince +=") {
		t.Error("the network budget must not move with the time the page was hidden")
	}
	// The budget failure must read as a network failure, so drive() stops the
	// run with netStop.
	m := regexp.MustCompile(`var NET_RE = /(.*)/i;`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("NET_RE not found")
	}
	netRE := regexp.MustCompile(`(?i)` + strings.ReplaceAll(m[1], `\/`, `/`))
	if !netRE.MatchString("no network answer: no network for 15 minutes") {
		t.Error("the budget failure is not taken for a network failure")
	}
	if !strings.Contains(js, "function netReason(msg) { msg = String(msg || ''); return netErr(msg) ? msg : 'no network answer: ' + (msg || 'the speaker did not answer'); }") {
		t.Error("netReason changed; re-check that the budget failure still reads as a network failure")
	}
}
