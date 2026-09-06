package main

// What the install says when its agent-wait budget runs out.
//
// A SoundTouch 10 (bundle 2026-09-06) had its agent up about 120 s after the
// network install, well inside the 180 s budget, but the PC could not reach
// the box at all for several more minutes: its Wi-Fi came back late (the box
// carries two saved profiles and tried the dead one first). The install was
// reported "FAILED step=reboot code=agent-not-up", the user was told the
// agent did not come up, and the speaker went on to work.
//
// The budget stays. What changes is the verdict when it runs out: if the
// speaker answers nothing at any layer, not a ping, not an ARP entry, not its
// own Bose port, not SSH, then "the agent did not come up" is a claim the app
// cannot make. It is a speaker that is not back on the network yet, and that
// is what the user is told, with one more look two minutes later that
// corrects the journal (and the screen) when the agent then answers.

import (
	"context"
	"fmt"
	"sync"
	"time"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"
)

// installLateRecheckDelay is how long after the budget ran out the app looks
// once more, in the background. One look, not a poll: the speaker list's own
// discovery takes over from there. A seam so tests do not wait two minutes.
var installLateRecheckDelay = 120 * time.Second

// agentWaitFacts is what the PC can establish about a speaker that did not
// bring up its agent within the budget. Zero values mean "not established",
// never "proven absent"; the verdict below is deliberately conservative.
type agentWaitFacts struct {
	PingRan   bool // a ping was actually attempted (the binary exists and ran)
	PingAlive bool // ... and the speaker replied to it
	ARP       bool // this PC's address table has an entry for the speaker
	BosePort  bool // the speaker's own :8090 accepted a TCP connection
	SSH       bool // :22 accepted a TCP connection
}

// speakerNotBackOnNetwork is the fingerprint of a speaker that is still
// restarting or reconnecting to Wi-Fi: silence at every layer. Any sign of
// life (an ARP entry, a ping reply, an open Bose or SSH port) means the
// speaker IS on the network and the agent really did not come up, so the
// older, stricter verdict stands. A ping that could not run proves nothing
// either way and is simply left out.
func (f agentWaitFacts) speakerNotBackOnNetwork() bool {
	if f.ARP || f.BosePort || f.SSH {
		return false
	}
	if f.PingRan && f.PingAlive {
		return false
	}
	return true
}

func (f agentWaitFacts) String() string {
	return fmt.Sprintf("pingRan=%v pingAlive=%v arp=%v bosePort=%v ssh=%v",
		f.PingRan, f.PingAlive, f.ARP, f.BosePort, f.SSH)
}

// gatherAgentWaitFacts collects the facts in parallel (ports and ping at once;
// ARP afterwards, so the dials have populated the cache it reads). A few
// seconds at most, and only on the failure path.
func gatherAgentWaitFacts(ctx context.Context, host string) agentWaitFacts {
	var f agentWaitFacts
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		pctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		f.PingRan, f.PingAlive = pingHost(pctx, host)
	}()
	go func() {
		defer wg.Done()
		f.BosePort = tcpReachable(host, bosePort, 3*time.Second)
	}()
	go func() {
		defer wg.Done()
		f.SSH = tcpReachable(host, 22, 3*time.Second)
	}()
	wg.Wait()
	actx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	f.ARP = arpLookup(actx, host) != ""
	return f
}

// agentWaitFactsFn and agentRunningLateFn are seams: tests replace them so
// the verdict and the late re-check can be exercised without a speaker, a
// ping binary or an SSH server on this machine. Nothing in the app assigns
// to them.
var (
	agentWaitFactsFn   = gatherAgentWaitFacts
	agentRunningLateFn = (*App).agentRunningViaSSH
)

// speakerNotBackCode is the InstallResult.Code for a speaker that answered
// nothing at any layer when the agent-wait budget ran out. The frontend keys
// its help checklist off it (INSTALL_HELP_STEPS in setup.js).
const speakerNotBackCode = "speaker-not-back"

// agentNotUp fills res for an install whose agent-wait budget ran out.
// genericMsg is the "the speaker did not bring up the STR agent" wording the
// caller would have used; it stays the message whenever the speaker shows any
// sign of life. When it shows none, the result says what is actually known,
// a speaker that is not back on the network yet, and a background re-check
// is scheduled that corrects the journal if the agent answers after all.
// Suffixes the callers add (the firmware note) go on after this returns.
func (a *App) agentNotUp(res InstallResult, host, model, genericMsg string) InstallResult {
	res.OK = false
	facts := agentWaitFactsFn(a.appCtx(), host)
	a.logger.Info("install_str: agent wait budget ran out; network facts", "host", host, "facts", facts.String())
	if !facts.speakerNotBackOnNetwork() {
		res.Code = "agent-not-up"
		res.Message = genericMsg
		return res
	}
	res.Code = speakerNotBackCode
	res.Message = speakerNotBackMessage(model)
	a.recordOTA(host, fmt.Sprintf("wait: the agent-wait budget (%s) ran out and the speaker answers nothing at any layer (%s): it is still restarting or reconnecting to Wi-Fi, not proven failed; re-checking once in %s",
		agentWaitBudget(model), facts, installLateRecheckDelay))
	a.scheduleInstallLateRecheck(host, model)
	return res
}

// speakerNotBackMessage is the user-facing sentence for speakerNotBackCode.
// English on purpose: InstallResult.Message reaches the UI verbatim like every
// other install message; the localized checklist under it comes from Code.
func speakerNotBackMessage(model string) string {
	msg := "STR was installed, but the speaker is not back on the network yet: right now it answers nothing at all, not even a ping, so it is still restarting or reconnecting to Wi-Fi. "
	if slowBootModel(model) {
		msg += "On Portable / BCO models that can take several minutes. "
	}
	return msg + "Wait a few minutes, then refresh the speaker list; the install may well have succeeded. ST Reborn looks once more by itself in about two minutes."
}

// scheduleInstallLateRecheck looks at the speaker once more after
// installLateRecheckDelay and writes what it finds to the journal: a
// "confirmed late" line that supersedes the UNCONFIRMED one when the agent answers
// (over HTTP on either port, or as a running process over SSH on a chassis
// whose firewall hides the ports), or a second "still not answering" line
// with fresh facts when it does not. The UI is told either way over the
// install:late event so a failure screen that is still open can update.
func (a *App) scheduleInstallLateRecheck(host, model string) {
	since := time.Now()
	go func() {
		select {
		case <-time.After(installLateRecheckDelay):
		case <-a.appCtx().Done():
			return
		}
		ctx, cancel := context.WithTimeout(a.appCtx(), 20*time.Second)
		defer cancel()
		probe := probeSTR
		if a.probeSTRFn != nil {
			probe = a.probeSTRFn
		}
		b, ok := probe(ctx, host)
		how := fmt.Sprintf("on :%d", b.Port)
		if !ok && agentRunningLateFn(a, host) {
			ok = true
			how = "as a running process over SSH (its HTTP ports are not reachable from this PC)"
		}
		late := time.Since(since).Round(time.Second)
		if ok {
			// Says UNCONFIRMED, not the other word: the failure report counts
			// journal lines carrying that other token as failed attempts, and
			// this line records a success.
			a.recordOTA(host, fmt.Sprintf("install: confirmed late - the agent answered %s %s after the wait budget ran out (version %s build %s); the UNCONFIRMED line above is superseded, the install succeeded",
				how, late, b.Version, b.Build))
			a.logger.Info("install_str: late re-check found the agent up; the install succeeded after all", "host", host, "late", late, "version", b.Version)
			// Same pin the success path sets: the box's stock :8090 answered
			// before its agent did, and discovery must not offer a reinstall.
			a.notePostOTA(host)
			a.emitInstallLate(host, true, b.Version)
			return
		}
		facts := agentWaitFactsFn(a.appCtx(), host)
		a.recordOTA(host, fmt.Sprintf("install: still not answering %s after the wait budget ran out (%s)", late, facts))
		a.logger.Warn("install_str: late re-check, agent still not answering", "host", host, "late", late, "facts", facts.String())
		a.emitInstallLate(host, false, "")
	}()
}

// InstallLate is the payload of the install:late event.
type InstallLate struct {
	Host    string `json:"host"`
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
}

func (a *App) emitInstallLate(host string, ok bool, version string) {
	if a.ctx != nil {
		wailsrt.EventsEmit(a.ctx, "install:late", InstallLate{Host: host, OK: ok, Version: version})
	}
}
