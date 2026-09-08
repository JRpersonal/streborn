package main

// What the install says when its agent-wait budget runs out.
//
// A SoundTouch 10 (bundle 2026-09-06) had its agent up about 120 s after the
// network install, well inside the 180 s budget, but the PC could not reach
// the box at all for several more minutes. The install was reported "FAILED
// step=reboot code=agent-not-up", the user was told the agent did not come
// up, and the speaker went on to work.
//
// The reason first written here was that the box's Wi-Fi came back late
// because it carries two saved profiles and tried the dead one first. The
// box's own forensics contradict that and the sentence is corrected rather
// than left to become received wisdom: carrier, LAN address and gateway ARP
// were healthy through the entire unreachable window. What the speaker WAS
// doing, on both #873 reporters' boxes, is running the firmware's own setup
// state machine: its Wi-Fi light blinks fast, it raises its own setup access
// point, and while that AP is up it is not on the home network at all. From
// the PC that is indistinguishable from a dead speaker.
//
// The budget stays. What changes is the verdict when it runs out, and that it
// is now a verdict of three rather than two:
//
//	agent-not-up      the speaker answers its own Bose port and STR does not.
//	                  Today's strict wording, the case it was written for.
//	speaker-in-setup  the speaker is running its own out-of-box setup, which
//	                  it leaves by itself, usually inside a quarter of an hour.
//	speaker-not-back  it answers nothing at any layer. Not a failure, a
//	                  speaker that is not back yet.
//
// The last two are waits, not failures: a bounded watcher keeps looking and
// corrects the journal and the screen when the agent answers after all.

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"sync"
	"time"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"
)

// installLateRecheckDelay is how long after the budget ran out the first extra
// look happens. A seam so tests do not wait two minutes.
var installLateRecheckDelay = 120 * time.Second

// installWaitPoll is the cadence of the watcher after that first look.
var installWaitPoll = 20 * time.Second

// installWaitCeiling / setupWaitCeiling bound the watcher. Both fifteen
// minutes, which is what every user-facing string promises ("wait about
// fifteen minutes"): a watcher that outlives its own promise leaves the user
// staring at a screen that said it would be done ten minutes ago. The setup
// case gets its own name because it is a different wait with a different
// message, not a longer one. Vars, like installLateRecheckDelay, so tests do
// not sit through them.
var (
	installWaitCeiling = 15 * time.Minute
	setupWaitCeiling   = 15 * time.Minute
)

// agentWaitFacts is what the PC can establish about a speaker that did not
// bring up its agent within the budget. Zero values mean "not established",
// never "proven absent"; the verdict below is deliberately conservative.
type agentWaitFacts struct {
	PingRan   bool // a ping was actually attempted (the binary exists and ran)
	PingAlive bool // ... and the speaker replied to it
	ARP       bool // this PC's address table has an entry for the speaker
	BosePort  bool // the speaker's own :8090 accepted a TCP connection
	BoseHTTP  bool // ... and answered a real GET /info, not just a dial
	MediaPort bool // :8091, the UPnP renderer, a separate firmware process
	SSH       bool // :22 accepted a TCP connection
}

// speakerNotBackOnNetwork is the fingerprint of a speaker that is still
// restarting, reconnecting, or running its own setup: silence at every layer.
//
// The ARP entry is deliberately NOT part of the proof-of-life set, and that
// is a correction, not an omission. Reporter A's facts line reads
// "pingRan=true pingAlive=false arp=true": a stale Windows ARP entry outlives
// the peer, and the app's own failed dials refresh it, so counting it as a
// sign of life would have called that install a hard failure and never armed
// the re-check at all. The field stays in the journal line as evidence; it is
// simply not a gate (found 2026-09-06).
func (f agentWaitFacts) speakerNotBackOnNetwork() bool {
	if f.BosePort || f.BoseHTTP || f.MediaPort || f.SSH {
		return false
	}
	if f.PingRan && f.PingAlive {
		return false
	}
	return true
}

func (f agentWaitFacts) String() string {
	return fmt.Sprintf("pingRan=%v pingAlive=%v arp=%v bosePort=%v boseHTTP=%v mediaPort=%v ssh=%v",
		f.PingRan, f.PingAlive, f.ARP, f.BosePort, f.BoseHTTP, f.MediaPort, f.SSH)
}

// gatherAgentWaitFacts collects the facts in parallel (ports and ping at once;
// ARP afterwards, so the dials have populated the cache it reads). A few
// seconds at most, and only on the failure path.
func gatherAgentWaitFacts(ctx context.Context, host string) agentWaitFacts {
	var f agentWaitFacts
	var wg sync.WaitGroup
	wg.Add(5)
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
		// A real HTTP exchange, not a bare dial: on the whitelisted chassis a
		// Bose port can accept a connection and then never answer, and "it
		// accepted a TCP connection" is not the same claim as "the speaker's
		// firmware is serving".
		b, err := httpGetSmall(ctx, fmt.Sprintf("http://%s:%d/info", host, bosePort), 4*time.Second, 4096)
		f.BoseHTTP = err == nil && len(b) > 0
	}()
	go func() {
		defer wg.Done()
		// A separate firmware process from the control API: :8090 dead and
		// :8091 alive is the wedged-control-stack signature, and either one
		// answering means the speaker is on the network.
		f.MediaPort = tcpReachable(host, boseUPnPPort, 3*time.Second)
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
	// boxInSetupFn is the "is the speaker running its own out-of-box setup"
	// read, behind the same kind of seam so the three-way verdict is testable
	// without a speaker on the LAN.
	boxInSetupFn = (*App).boxInSetupNow
)

// speakerNotBackCode is the InstallResult.Code for a speaker that answered
// nothing at any layer when the agent-wait budget ran out. The frontend keys
// its help checklist off it (INSTALL_HELP_STEPS in setup.js).
const speakerNotBackCode = "speaker-not-back"

// speakerInSetupCode is the InstallResult.Code for a speaker that is running
// the firmware's OWN out-of-box setup. It is not a failed install and there is
// nothing to repair: the state ends by itself, and STR is already on the box.
const speakerInSetupCode = "speaker-in-setup"

// boxSetupPhase reads the speaker's own documents and answers TWO different
// questions, because they have two different answers and one wrong story each.
// Pure over the two bodies so both verdicts are table-testable without a
// speaker.
//
//	oob         a REAL out-of-box setup: /setup says state=SETUP_AP* (the
//	            speaker is raising its own access point) or a systemstate that
//	            is genuinely unset (SETUP_LANG_NOT_SET). This is the one that
//	            takes the Wi-Fi down.
//	stuckSource /now_playing says source="SETUP" while the speaker is fully on
//	            the network: #367's stuck source, a different bug with a
//	            different fix (cmd/agent/boxprobe.go boxSetupState,
//	            docs/FIRMWARE-NOTES.md).
//
// What the prefix rule must NOT do is match the HEALTHY post-setup state.
// SETUP_LANG_SET is what a fully configured speaker reports (boxapi.SetupStatus,
// setup_ap_push.go: "POSTing any value >= 1 transitions systemstate to
// SETUP_LANG_SET"), so a generic SETUP_ prefix classified every working
// speaker on the LAN as mid-setup. Only the _NOT_SET shape is evidence.
func boxSetupPhase(setupXML, nowPlayingXML string) (oob, stuckSource bool) {
	var setup struct {
		State       string `xml:"state,attr"`
		SystemState string `xml:"systemstate,attr"`
	}
	// Decoded, not scanned. A substring scan for `state="` also matches inside
	// `systemstate="`, so on a body that emits systemstate first the SETUP_AP
	// rule silently read the wrong attribute. The same document is already
	// decoded this way by boxapi.GetSetupStatus, which both the agent and the
	// bundle exporter use for exactly this job; a few hundred bytes of
	// firmware XML is not a parser risk. A body that does not decode leaves
	// both verdicts false, which is the honest answer for a speaker that has
	// told us nothing.
	_ = xml.Unmarshal([]byte(setupXML), &setup)
	state := strings.TrimSpace(setup.State)
	system := strings.TrimSpace(setup.SystemState)
	oob = strings.HasPrefix(state, "SETUP_AP") || strings.HasSuffix(system, "_NOT_SET")

	var np struct {
		Source string `xml:"source,attr"`
	}
	_ = xml.Unmarshal([]byte(nowPlayingXML), &np)
	stuckSource = strings.TrimSpace(np.Source) == "SETUP"
	return oob, stuckSource
}

// readBoxSetupState asks the SPEAKER what it is doing: a real out-of-box setup
// (oob), and separately whether its source is merely stuck on SETUP.
// Both false on any read failure, which is the honest answer: a speaker that
// cannot be asked has not told us anything.
//
// Only oob may drive the "it left the network" story. A speaker with #367's
// stuck source is fully ON the network, so telling that user its Wi-Fi light
// is blinking and it dropped off would be the wrong story, and letting it
// displace the agent-gone verdict would replace the one correct piece of
// advice (power cycle) with "wait fifteen minutes" - in the very case where
// the dead agent is what leaves the source stuck.
func (a *App) readBoxSetupState(host string) (oob, stuckSource bool) {
	setupXML, err := a.boseGet(host, "/setup")
	if err != nil {
		return false, false
	}
	npXML, _ := a.boseGet(host, "/now_playing")
	return boxSetupPhase(setupXML, npXML)
}

// boxInSetupNow is readBoxSetupState's oob verdict with the "can the speaker
// be asked at all" precondition in front of it, which is the form every caller
// wants.
func (a *App) boxInSetupNow(host string) bool {
	if !a.boxAnswersBoseAPI(host) {
		return false
	}
	oob, _ := a.readBoxSetupState(host)
	return oob
}

// agentNotUp fills res for an install whose agent-wait budget ran out.
// genericMsg is the "the speaker did not bring up the STR agent" wording the
// caller would have used; it stays the message whenever the speaker is
// demonstrably on the network and not in setup. Suffixes the callers add (the
// firmware note) go on after this returns.
func (a *App) agentNotUp(res InstallResult, host, model, genericMsg string) InstallResult {
	res.OK = false
	facts := agentWaitFactsFn(a.appCtx(), host)
	// Two ways to know: the speaker says so now, or the wait watched it say so
	// while it ran. The second one matters most, because a speaker in setup is
	// off the network for minutes at a time and is very likely answering
	// nothing at the moment the budget expires - which is exactly the shape
	// Reporter A's install had.
	inSetup := (facts.BoseHTTP && boxInSetupFn(a, host)) || a.installSawSetup
	a.logger.Info("install_str: agent wait budget ran out; network facts",
		"host", host, "facts", facts.String(), "inSetup", inSetup, "sawSetupDuringWait", a.installSawSetup)

	switch {
	case inSetup:
		// The speaker is busy with its own out-of-box setup. STR is already on
		// it and nothing here can undo that; the state ends by itself.
		res.Code = speakerInSetupCode
		res.Message = speakerInSetupMessage(model)
		a.recordOTA(host, fmt.Sprintf("wait: the agent-wait budget (%s) ran out while the speaker is running its OWN out-of-box setup (%s): STR is installed, the speaker leaves this state by itself; watching for up to %s",
			agentWaitBudget(model), facts, setupWaitCeiling))
		a.watchInstallLate(host, model, true)
	case !facts.speakerNotBackOnNetwork():
		// The speaker is demonstrably there and STR is not running on it. This
		// is the case the strict wording was written for; it keeps it.
		res.Code = "agent-not-up"
		res.Message = genericMsg
	default:
		res.Code = speakerNotBackCode
		res.Message = speakerNotBackMessage(model)
		a.recordOTA(host, fmt.Sprintf("wait: the agent-wait budget (%s) ran out and the speaker answers nothing at any layer (%s): it is still restarting, reconnecting to Wi-Fi or finishing its own setup, not proven failed; watching for up to %s",
			agentWaitBudget(model), facts, installWaitCeiling))
		a.watchInstallLate(host, model, false)
	}
	return res
}

// speakerNotBackMessage is the user-facing sentence for speakerNotBackCode.
// English on purpose: InstallResult.Message reaches the UI verbatim like every
// other install message; the localized checklist under it comes from Code.
func speakerNotBackMessage(model string) string {
	msg := "STR was installed, but the speaker is not back on the network yet: right now it answers nothing at all, not even a ping, so it is still restarting, reconnecting to Wi-Fi, or finishing its own setup. "
	if slowBootModel(model) {
		msg += "On Portable / BCO models that can take several minutes. "
	}
	return msg + "Your network is not the problem: ST Reborn ran the install over it a few minutes ago. ST Reborn keeps looking by itself and will say so here when the speaker answers."
}

// speakerInSetupMessage is the user-facing sentence for speakerInSetupCode.
func speakerInSetupMessage(model string) string {
	msg := "The speaker is running its own out-of-box setup: its Wi-Fi light blinks fast and it drops off the network for a few minutes at a time while it does. ST Reborn is already on the speaker and this cannot undo it. "
	if slowBootModel(model) {
		msg += "On Portable / BCO models that phase runs longer. "
	}
	return msg + "The state usually ends by itself within about fifteen minutes, and ST Reborn keeps looking meanwhile."
}

// InstallWaiting is the payload of the install:waiting event: the screen's
// countdown while the watcher is still looking.
type InstallWaiting struct {
	Host        string `json:"host"`
	ElapsedMs   int    `json:"elapsedMs"`
	RemainingMs int    `json:"remainingMs"`
	InSetup     bool   `json:"inSetup"`
}

// watchInstallLate keeps looking at the speaker after the wait budget ran out,
// and writes what it finds to the journal: a "confirmed late" line that
// supersedes the UNCONFIRMED one when the agent answers (over HTTP on either
// port, or as a running process over SSH on a chassis whose firewall hides the
// ports), or a final "still not answering" line with fresh facts when the
// ceiling is reached. The UI is told over install:waiting while it runs and
// install:late when it ends, so a screen that is still open updates itself.
//
// A watcher, not the single look it replaces. The single look fired once at
// two minutes and then gave up, which is inside the very window both #873
// reporters were still waiting out; and the screen it left behind told the
// user to press Install again, which is what produced the second, worse
// failure on the reporter's box.
func (a *App) watchInstallLate(host, model string, inSetup bool) {
	since := time.Now()
	ceiling := installWaitCeiling
	if inSetup {
		ceiling = setupWaitCeiling
	}
	go func() {
		emitWaiting := func(inSetup bool, deadline time.Time) {
			remaining := time.Until(deadline)
			if remaining < 0 {
				remaining = 0
			}
			a.emitInstallWaiting(InstallWaiting{
				Host:        host,
				ElapsedMs:   int(time.Since(since) / time.Millisecond),
				RemainingMs: int(remaining / time.Millisecond),
				InSetup:     inSetup,
			})
		}
		deadline := since.Add(ceiling)
		// Say straight away how long this will wait, then keep saying it while
		// the first look is still pending. One emit here would be lost: this
		// goroutine starts before installSTROnBox has returned, so the panel
		// that renders the countdown has not subscribed yet, and the next emit
		// would be two minutes away - the stretch in which the user most needs
		// to see that the app has a plan.
		emitWaiting(inSetup, deadline)
		// The first look keeps its old delay: a speaker that is merely slow is
		// usually back inside it, and probing sooner only adds noise.
		firstProbeAt := time.Now().Add(installLateRecheckDelay)
		for time.Now().Before(firstProbeAt) {
			wait := time.Until(firstProbeAt)
			if wait > installWaitPoll {
				wait = installWaitPoll
			}
			select {
			case <-time.After(wait):
			case <-a.appCtx().Done():
				return
			}
			emitWaiting(inSetup, deadline)
		}
		for {
			ctx, cancel := context.WithTimeout(a.appCtx(), 20*time.Second)
			probe := probeSTR
			if a.probeSTRFn != nil {
				probe = a.probeSTRFn
			}
			b, ok := probe(ctx, host)
			cancel()
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
			if !time.Now().Before(deadline) {
				facts := agentWaitFactsFn(a.appCtx(), host)
				a.recordOTA(host, fmt.Sprintf("install: still not answering %s after the wait budget ran out (%s)", late, facts))
				a.logger.Warn("install_str: gave up waiting, agent still not answering", "host", host, "late", late, "facts", facts.String())
				a.emitInstallLate(host, false, "")
				return
			}
			// Re-read the setup state as we go: a speaker that only starts its
			// setup phase after the budget ran out must still be described
			// correctly on the screen the user is looking at.
			if !inSetup && boxInSetupFn(a, host) {
				inSetup = true
				deadline = since.Add(setupWaitCeiling)
				a.recordOTA(host, "wait: the speaker is now running its OWN out-of-box setup; it leaves that state by itself")
			}
			emitWaiting(inSetup, deadline)
			select {
			case <-time.After(installWaitPoll):
			case <-a.appCtx().Done():
				return
			}
		}
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

func (a *App) emitInstallWaiting(w InstallWaiting) {
	if a.ctx != nil {
		wailsrt.EventsEmit(a.ctx, "install:waiting", w)
	}
}
