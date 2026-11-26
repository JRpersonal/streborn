package main

import (
	"net"
	"strings"
	"testing"
	"time"
)

// The hours-without-power advice (Jens, 2026-11-26): some speakers hang in
// their start after any warm restart and only hours without power free them,
// so the sentence is only for an owner whose speaker STR knows it restarted
// and that has not come back. Everybody else gets the ten-second power cycle.

const hoursSentence = "several hours"

// slogLine is one app-log line as the text handler writes it.
func slogLine(msg, host string) string {
	return `time=2026-11-26T10:00:00.000+01:00 level=INFO msg="` + msg + `" host=` + host
}

func hungSpeakerPorts() []portAnswer {
	return []portAnswer{
		{Port: 22, Result: "timeout"},
		{Port: 8090, Result: "timeout"},
		{Port: 8888, Result: "timeout"},
		{Port: 17008, Result: "timeout"},
	}
}

func reportWithLog(host string, lines ...string) failureReport {
	return failureReport{
		Host:  host,
		Phase: "install:agent-not-up",
		Facts: installFacts{
			SubnetKnown: true, SameSubnet: true, PingRan: true,
			Ports:   hungSpeakerPorts(),
			LogTail: strings.Join(lines, "\n"),
		},
	}
}

func adviceTail(t *testing.T, r failureReport) string {
	t.Helper()
	out := formatFailureReport(r)
	i := strings.Index(out, "what to try")
	if i < 0 {
		t.Fatalf("no advice section:\n%s", out)
	}
	return out[i:]
}

func TestHoursAdviceAfterTheInstallsOwnRestartWhenTheSpeakerStaysAway(t *testing.T) {
	const host = "192.0.2.1"
	for name, restart := range map[string]string{
		"the install's own restart":      "repair_ssh: restart sent after the install",
		"an older log, install ran":      "repair_ssh: install ran",
		"the stopped agent's restart":    "repair_ssh: restarting the speaker so the STR agent stopped for the install comes back",
		"the forced restart":             "install_str: the speaker accepted the restart command but never restarted; forcing the restart",
		"a restart the user asked for":   "ota journal event=\"reboot: STR asked the speaker to restart (sign-in warning)\"",
		"an accepted agent update":       "ota journal event=\"outcome: upload accepted; success is decided by the version poll\"",
		"the :17000 unlock's sys reboot": "telnet-enable: injection written and sys reboot sent; polling for SSH",
	} {
		r := reportWithLog(host,
			slogLine("install_str: ssh ok", host),
			slogLine("repair_ssh: staged install files", host),
			slogLine(restart, host),
		)
		tail := adviceTail(t, r)
		if !strings.Contains(tail, agentNotUpAdvice) || !strings.Contains(tail, hoursSentence) {
			t.Errorf("%s: a restart STR knows of and a silent speaker must get the hours advice:\n%s", name, tail)
		}
		if strings.Contains(tail, agentNotUpNoRestartAdvice) {
			t.Errorf("%s: only one of the two agent-not-up paragraphs may appear", name)
		}
	}
}

func TestNoHoursAdviceWithoutAKnownRestart(t *testing.T) {
	const host = "192.0.2.1"
	cases := map[string]failureReport{
		// The app got in, then the upload failed: nothing restarted the speaker.
		"ssh ok only": reportWithLog(host,
			slogLine("install_str: ssh ok", host)),
		"staged, then failed": reportWithLog(host,
			slogLine("install_str: ssh ok", host),
			slogLine("repair_ssh: staged install files", host),
			slogLine("repair_ssh: staging failed on all targets", host)),
		// A restart earlier in the log, then the speaker answered again.
		"restarted, then back": reportWithLog(host,
			slogLine("telnet-enable: injection written and sys reboot sent; polling for SSH", host),
			slogLine("install_str: SSH opened stick-free via :17000; installing STR over SSH", host),
			slogLine("repair_ssh: staged install files", host)),
		"installed, then confirmed late": reportWithLog(host,
			slogLine("install_str: ssh ok", host),
			slogLine("repair_ssh: restart sent after the install", host),
			slogLine(`ota journal event="install: confirmed late - the agent answered"`, host)),
		// The restart request went out and failed.
		"restart request failed": reportWithLog(host,
			slogLine("install_str: ssh ok", host),
			slogLine(`ota journal event="reboot: STR asked the speaker to restart (x) and the request failed: refused"`, host)),
		// Another speaker's restart, including one whose address starts with
		// this speaker's.
		"another speaker restarted": reportWithLog(host,
			slogLine("install_str: ssh ok", host),
			slogLine("repair_ssh: restart sent after the install", "192.0.2.10"),
			slogLine("repair_ssh: restart sent after the install", "192.0.2.7")),
	}
	for name, r := range cases {
		tail := adviceTail(t, r)
		if strings.Contains(tail, hoursSentence) {
			t.Errorf("%s: no restart STR knows of, so no hours without power:\n%s", name, tail)
		}
		if !strings.Contains(tail, agentNotUpNoRestartAdvice) {
			t.Errorf("%s: the reached-this-session advice without the hours must lead:\n%s", name, tail)
		}
	}
}

func TestNoHoursAdviceWhenTheSpeakerIsBack(t *testing.T) {
	const host = "192.0.2.1"
	base := func() failureReport {
		return reportWithLog(host,
			slogLine("install_str: ssh ok", host),
			slogLine("repair_ssh: restart sent after the install", host))
	}
	portOpen := base()
	portOpen.Facts.Ports[1].Result = "open"
	agentAnswered := base()
	agentAnswered.BoxNow = []string{"version:                    1.9.2"}
	for name, r := range map[string]failureReport{"a port answers": portOpen, "the agent answered": agentAnswered} {
		if restartKnownAndNotBack(r) {
			t.Errorf("%s: a speaker that answers again is back", name)
		}
		got := applyReachedThisSession(nil, r)
		if len(got) == 0 || got[0] != agentNotUpNoRestartAdvice {
			t.Errorf("%s: want the advice without the hours, got %v", name, got)
		}
	}
}

func TestHoursAdviceFromTheJournalWhenTheLogTailIsEmpty(t *testing.T) {
	r := failureReport{
		Host:    "192.0.2.1",
		History: "time=2026-11-26T10:00:00+01:00 host=192.0.2.1 install: started\nrepair_ssh: install ran (outBytes=671)\ntime=2026-11-26T10:01:00+01:00 host=192.0.2.1 install: UNCONFIRMED step=reboot code=agent-not-up",
		Facts:   installFacts{Ports: hungSpeakerPorts()},
	}
	// The second line names no host, so it is about no speaker in particular
	// and does not count; the journal's own restart line does.
	if restartKnownAndNotBack(r) {
		t.Fatal("a line without this speaker's host must not prove its restart")
	}
	r.History += "\ntime=2026-11-26T10:00:30+01:00 host=192.0.2.1 restart: the speaker was still on its old boot 1m0s after the restart command"
	if !restartKnownAndNotBack(r) {
		t.Fatal("the journal's forced-restart line is a restart STR knows of")
	}
}

func TestLineIsAboutHost(t *testing.T) {
	cases := []struct {
		line, host string
		want       bool
	}{
		{"msg=x host=192.0.2.1", "192.0.2.1", true},
		{"msg=x host=192.0.2.1 err=y", "192.0.2.1", true},
		{`msg=x host="192.0.2.1"`, "192.0.2.1", true},
		{"msg=x host=192.0.2.10", "192.0.2.1", false},
		{"msg=x peer=192.0.2.1", "192.0.2.1", false},
		{"anything", "", true},
	}
	for _, c := range cases {
		if got := lineIsAboutHost(c.line, c.host); got != c.want {
			t.Errorf("lineIsAboutHost(%q, %q) = %v, want %v", c.line, c.host, got, c.want)
		}
	}
}

// The paragraph for the unproven case must not carry the sentence, and the
// four that do carry it stay the ones the restart paths choose.
func TestOnlyTheRestartParagraphsNameHoursWithoutPower(t *testing.T) {
	if strings.Contains(agentNotUpNoRestartAdvice, "hours") {
		t.Error("the no-restart paragraph must not name hours without power")
	}
	if !strings.Contains(agentNotUpNoRestartAdvice, "ten seconds") {
		t.Error("the no-restart paragraph keeps the short power cycle")
	}
	if strings.HasPrefix(agentNotUpAdvice, agentNotUpNoRestartAdvice[:40]) {
		t.Error("the two agent-not-up paragraphs must not share a prefix the report could confuse")
	}
	for name, p := range map[string]string{
		"agentNotUpAdvice":                   agentNotUpAdvice,
		"repairBoxRestartedAdvice":           repairBoxRestartedAdvice,
		"restartingAfterUnlockAdvice":        restartingAfterUnlockAdvice,
		"restartingAfterUnlockNoStickAdvice": restartingAfterUnlockNoStickAdvice,
	} {
		if !strings.Contains(p, hoursSentence) {
			t.Errorf("%s lost the hours sentence it carries after a known restart", name)
		}
	}
}

// The unlock's restarts go out over :17000, so they have their own ledger, and
// only a "sys reboot" that was actually written counts.
func TestTAPRebootLedger(t *testing.T) {
	const host = "192.0.2.99"
	tapReboots.Delete(host)
	defer tapReboots.Delete(host)
	start := time.Now()
	if tapRebootSentSince(host, start) {
		t.Fatal("nothing was sent yet")
	}

	// A connection that cannot be written to: no restart went out.
	c1, c2 := net.Pipe()
	_ = c2.Close()
	sendTAPReboot(&tapConn{conn: c1}, host)
	_ = c1.Close()
	if tapRebootSentSince(host, start) {
		t.Fatal("a sys reboot that could not be written must not count as a restart")
	}

	// A live connection: the restart went out.
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		buf := make([]byte, 64)
		_, _ = b.Read(buf)
		_, _ = b.Write([]byte("->"))
	}()
	sendTAPReboot(&tapConn{conn: a}, host)
	if !tapRebootSentSince(host, start) {
		t.Fatal("a written sys reboot is a restart STR knows of")
	}
	if tapRebootSentSince(host, time.Now().Add(time.Minute)) {
		t.Fatal("a restart before the given time must not count")
	}
}

// Source checks for the two places a restart is decided without a speaker to
// test against: the install's "restarting after the unlock" advice and the
// unlock's own memory of the restart.
func TestUnlockRestartAdviceNeedsASentRestart(t *testing.T) {
	adv := funcBody(t, readInstallSrc(t), "func (a *App) installUnlockOrAdvise(")
	if strings.Contains(adv, "rebootedByUs = true") {
		t.Error("rebootedByUs must not be assumed; it needs a sys reboot that went out")
	}
	if !strings.Contains(adv, "rebootedByUs = tapRebootSentSince(host, unlockStart)") {
		t.Error("rebootedByUs must come from the :17000 restart ledger")
	}
	ens := funcBody(t, readSource(t, "sshunlock.go"), "func (a *App) ensureSSHOpen(")
	sent, mark := strings.Index(ens, "tapRebootSentSince(host, start)"), strings.Index(ens, "markUnlockReboot(host)")
	if sent < 0 || mark < 0 || sent > mark {
		t.Error("ensureSSHOpen must remember the unlock's restart only when one went out")
	}
	for _, f := range []string{"telnet_enable_ssh.go", "telnet_bootstrap_marge.go"} {
		if strings.Contains(readSource(t, f), `t.send("sys reboot")`) {
			t.Errorf("%s sends sys reboot past the ledger; use sendTAPReboot", f)
		}
	}
}
