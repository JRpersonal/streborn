package main

// A speaker that accepts the restart command and then never restarts.
//
// A Wave SoundTouch, 2026-11-03, freshly cleaned of AfterTouch: STR's install
// ran over SSH twice, both times without an error, and both times the agent
// "did not come up". The bundle taken five minutes after the second attempt
// told the real story. The speaker's uptime still counted from the owner's own
// power cycle twenty minutes earlier, so neither install's restart had taken
// place, and PID 1 read "init [6]": init had switched to the reboot runlevel and
// then stalled in its shutdown scripts. A healthy speaker reads "init [5]".
// Bose's control port and SSH were both still answering. The owner had already
// met the same stall with AfterTouch's own reboot command and had to pull the
// plug.
//
// The files STR had just installed were all on the NAND, waiting for a boot
// that was not going to come by itself. So the wait now checks, once, whether
// the restart it asked for actually happened: if the speaker still answers SSH
// a minute after the restart command and its uptime is older than that command,
// the restart stalled, and the app forces it (`reboot -f` skips the init scripts
// that hang; the sysrq write behind it is the kernel's own last resort). Both
// come after a `sync`, so the forced restart is no rougher on the NAND than the
// power cycle the owner would otherwise have to do by hand.

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// rebootLedger remembers when the app last sent the restart command to each
// speaker, so the agent wait can tell "still restarting" from "never restarted".
var rebootLedger sync.Map // host -> time.Time

// noteRebootSent records that the restart command just went to host.
func noteRebootSent(host string) {
	rebootLedger.Store(host, time.Now())
}

// rebootSentAt returns when the restart command last went to host.
func rebootSentAt(host string) (time.Time, bool) {
	v, ok := rebootLedger.Load(host)
	if !ok {
		return time.Time{}, false
	}
	t, ok := v.(time.Time)
	return t, ok
}

// hungRebootCheckAfter is how long after the restart command the check may run.
// A speaker that is restarting normally has its SSH gone well before this; one
// that answers SSH this late AND reports an uptime older than the command has
// not restarted at all.
var hungRebootCheckAfter = 60 * time.Second

// hungRebootCheckWindow bounds how old a ledger entry may be and still count.
// An entry from an install half an hour ago says nothing about the speaker now.
var hungRebootCheckWindow = 10 * time.Minute

// hungRebootSlack absorbs the second or two between the app sending the command
// and the speaker's kernel starting its clock on a real restart.
const hungRebootSlack = 20 * time.Second

// forcedRebootCmd restarts without init's shutdown scripts. sync first, as in
// rebootCmd; then reboot -f, which calls the kernel directly. If even that
// returns, the sysrq trigger is the kernel's own reset, and writing to it works
// whatever the sysrq sysctl says.
const forcedRebootCmd = "(sleep 1; sync; /sbin/reboot -f; sleep 15; sync; echo b > /proc/sysrq-trigger) </dev/null >/dev/null 2>&1 &"

// rebootNeverHappened reports whether a speaker whose kernel has been up for
// uptime cannot have restarted since the command sent sinceSent ago.
func rebootNeverHappened(uptime, sinceSent time.Duration) bool {
	return uptime > sinceSent+hungRebootSlack
}

// parseUptimeProbe reads the output of hungRebootProbeCmd: the uptime in whole
// seconds on the first line, then whatever ps says about PID 1.
func parseUptimeProbe(out string) (uptime time.Duration, pid1 string, ok bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 {
		return 0, "", false
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(lines[0]), 10, 64)
	if err != nil || sec < 0 {
		return 0, "", false
	}
	if len(lines) > 1 {
		pid1 = strings.Join(strings.Fields(lines[1]), " ")
	}
	return time.Duration(sec) * time.Second, pid1, true
}

const hungRebootProbeCmd = "awk '{print int($1)}' /proc/uptime 2>/dev/null; ps 2>/dev/null | awk '$1==1'"

// Seams, so the check can be exercised without a speaker.
var (
	hungRebootProbeFn = func(host string) (string, error) {
		return boxSSHOutput(host, hungRebootProbeCmd, 8*time.Second)
	}
	forceRebootFn = func(host string) error {
		err := boxSSHFireAndForget(host, forcedRebootCmd, 6*time.Second)
		boxSSHClients.invalidateHost(host)
		return err
	}
)

// forceRebootIfHung runs the check described at the top of this file and, when
// the restart stalled, forces it. It returns true only when it sent the forced
// restart, which is the caller's cue to give the speaker a fresh wait budget:
// its boot starts now, not when the first command went out.
//
// Every "cannot tell" answer (no ledger entry, too early, too late, SSH down,
// unreadable uptime) returns false and does nothing. The forced path is only
// ever taken on positive evidence that the speaker is still on its old boot.
func (a *App) forceRebootIfHung(host string) bool {
	sent, ok := rebootSentAt(host)
	if !ok {
		return false
	}
	since := time.Since(sent)
	if since < hungRebootCheckAfter || since > hungRebootCheckWindow {
		return false
	}
	out, err := hungRebootProbeFn(host)
	if err != nil {
		return false
	}
	uptime, pid1, ok := parseUptimeProbe(out)
	if !ok || !rebootNeverHappened(uptime, since) {
		return false
	}
	a.logger.Warn("install_str: the speaker accepted the restart command but never restarted; forcing the restart",
		"host", host, "sinceRestartCommand", since.Round(time.Second), "uptime", uptime, "pid1", pid1)
	a.recordOTA(host, fmt.Sprintf("restart: the speaker was still on its old boot %s after the restart command (uptime %s, %q); forced the restart",
		since.Round(time.Second), uptime, pid1))
	_ = forceRebootFn(host)
	noteRebootSent(host)
	return true
}
