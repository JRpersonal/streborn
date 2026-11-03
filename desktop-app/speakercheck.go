package main

// The "speaker not answering" self-check: the network facts the failure
// report gathers (installfacts.go), run for one speaker on a click, and turned
// into ONE verdict the UI can explain with steps.
//
// Why this exists: the advice for the three most frequent causes of a speaker
// that stops answering was already written, but it only appeared inside the
// failure report after an install or update had failed. Support mail and
// GitHub (#763 #1307 #983, #420 #815 #897) show the order:
//
//	the speaker is off the network (unpowered, lost its Wi-Fi, new address),
//	the speaker and this PC are in different networks,
//	the Bose firmware hung: ping answers, its ports do not.
//
// Meanwhile the speaker list told the owner of such a speaker to "install STR
// again", which cannot help with any of the three. The list and the support
// mail now run this check first.
//
// Rules:
//   - no polling. The check runs on a click only. A second click while one
//     runs joins it, and a finished result is reused for a few seconds.
//   - the gatherer is the failure report's own (gatherInstallFacts), and the
//     other-network and off-network rules are the report's own predicates
//     (addressInOtherNetwork, silentAtNetworkLayer in updatereport.go), so the
//     check and the report cannot drift apart about what the network said.

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// The verdicts, in the order classifySpeakerCheck tests them.
const (
	// checkOK: STR answered on one of its ports.
	checkOK = "ok"
	// checkSTRSilent: the Bose firmware answers on :8090, STR on neither of
	// its ports. Either STR is still starting or it is gone; the only verdict
	// that keeps "install STR again" on offer.
	checkSTRSilent = "str-silent"
	// checkPCOffline: this PC has no network address at all.
	checkPCOffline = "pc-offline"
	// checkHung: something answers live at the address (a ping reply, a port
	// that refuses or opens without answering) but no healthy Bose firmware.
	// An ARP entry alone does not count: the PC keeps a speaker's hardware
	// address for a while after it left.
	checkHung = "hung"
	// checkOtherNetwork: nothing answers and the address is outside every
	// network this PC is on.
	checkOtherNetwork = "other-network"
	// checkOffNetwork: nothing answers at an address on this PC's network,
	// measured down to the network layer: ping ran without a reply and there
	// is no ARP entry.
	checkOffNetwork = "off-network"
	// checkNoAnswer: nothing answers on any port and no ping reply, but the
	// network layer could not be ruled out: ping could not run on this PC, or
	// an ARP entry is left over. Same steps as off-network, without the claim
	// that not even a ping answered.
	checkNoAnswer = "no-answer"
)

// speakerCheckAdviceKeys maps a verdict to the i18n key of the sentence that
// explains it (desktop-app/frontend/src/i18n/bundles). The steps that follow
// it are chosen by the frontend from the verdict (speakercheck.js).
var speakerCheckAdviceKeys = map[string]string{
	checkOK:           "speakerCheck.lead.ok",
	checkSTRSilent:    "speakerCheck.lead.strSilent",
	checkPCOffline:    "speakerCheck.lead.pcOffline",
	checkHung:         "speakerCheck.lead.hung",
	checkOtherNetwork: "speakerCheck.lead.otherNetwork",
	checkOffNetwork:   "speakerCheck.lead.offNetwork",
	checkNoAnswer:     "speakerCheck.lead.noAnswer",
}

// speakerCheckWords is the verdict in plain English for the "speaker check"
// log line, which the diagnostic bundle carries to support.
var speakerCheckWords = map[string]string{
	checkOK:           "STR answers",
	checkSTRSilent:    "the Bose firmware answers, STR does not",
	checkPCOffline:    "this PC has no network address",
	checkHung:         "something is at the address, but the Bose firmware does not answer",
	checkOtherNetwork: "nothing answers, and the address is in a different network than this PC",
	checkOffNetwork:   "nothing answers at the address, not even at the network layer",
	checkNoAnswer:     "nothing answers, the network layer could not be ruled out",
}

// SpeakerCheck is what CheckSpeaker returns to the frontend.
type SpeakerCheck struct {
	Host string `json:"host"`
	// Verdict is one of the check* constants, empty when the check could not
	// run (Error says why).
	Verdict string `json:"verdict"`
	// AdviceKey is the i18n key of the sentence that explains the verdict.
	AdviceKey string `json:"adviceKey"`
	// PCNetworks names the networks this PC is on ("192.0.2.0/24 on WLAN"),
	// for the other-network sentence.
	PCNetworks string `json:"pcNetworks"`
	// Summary is the result in one plain-English line for the support mail:
	// "speaker check 192.0.2.20: off-network (22=silent ...; ping no, arp no)".
	// One line on purpose: a mailto URL past about 2 KB is cut off or refused
	// by some mail programs, and the full facts are in the "speaker check" log
	// line the diagnostic bundle carries.
	Summary string `json:"summary"`
	Error   string `json:"error,omitempty"`
}

// speakerCheckReuse is how long a finished check is handed out again instead
// of probing the speaker anew. Long enough to absorb a double click and the
// support mail's check right after a list check, short enough that "I plugged
// it back in, check again" really checks again a minute later.
const speakerCheckReuse = 5 * time.Second

// speakerCheckGate keeps the check to one probe set per speaker at a time.
// The zero value is ready to use.
type speakerCheckGate struct {
	mu       sync.Mutex
	inflight map[string]*speakerCheckCall
	last     map[string]speakerCheckDone
	// gather replaces gatherInstallFacts in tests.
	gather func(ctx context.Context, host string) installFacts
	// now replaces time.Now in tests.
	now func() time.Time
}

type speakerCheckCall struct {
	done chan struct{}
	res  SpeakerCheck
}

type speakerCheckDone struct {
	at  time.Time
	res SpeakerCheck
}

// CheckSpeaker runs the network facts for one speaker and says what they
// mean. Bound to the frontend: the speaker list's Check action and the
// self-check before the support mail call it, on a click only.
func (a *App) CheckSpeaker(host string) (out SpeakerCheck) {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, " /\\?#@") {
		return SpeakerCheck{Host: host, Error: "no speaker address"}
	}
	g := &a.speakerCheck
	now := time.Now
	if g.now != nil {
		now = g.now
	}

	g.mu.Lock()
	if d, ok := g.last[host]; ok && now().Sub(d.at) < speakerCheckReuse {
		g.mu.Unlock()
		return d.res
	}
	if c, ok := g.inflight[host]; ok {
		g.mu.Unlock()
		<-c.done
		return c.res
	}
	if g.inflight == nil {
		g.inflight = map[string]*speakerCheckCall{}
	}
	c := &speakerCheckCall{done: make(chan struct{})}
	g.inflight[host] = c
	gather := g.gather
	g.mu.Unlock()

	if gather == nil {
		gather = a.gatherInstallFacts
	}
	// Whatever happens below, the waiters are released and the slot freed:
	// a check that died must not leave every later click waiting on it.
	defer func() {
		if r := recover(); r != nil {
			c.res = SpeakerCheck{Host: host, Error: fmt.Sprint("the check failed: ", r)}
		}
		g.mu.Lock()
		delete(g.inflight, host)
		if c.res.Verdict != "" {
			if g.last == nil {
				g.last = map[string]speakerCheckDone{}
			}
			g.last[host] = speakerCheckDone{at: now(), res: c.res}
		}
		g.mu.Unlock()
		close(c.done)
		out = c.res
	}()

	f := gather(a.appCtx(), host)
	c.res = speakerCheckResult(f)
	if a.logger != nil {
		a.logger.Info("speaker check", "host", host, "verdict", c.res.Verdict,
			"meaning", speakerCheckWords[c.res.Verdict],
			"pingRan", f.PingRan, "pingAlive", f.PingAlive, "arp", f.MACPrefix != "",
			"subnetKnown", f.SubnetKnown, "sameSubnet", f.SameSubnet,
			"ports", portsSummary(f.Ports))
	}
	return c.res
}

// speakerCheckResult turns the gathered facts into the answer. Pure.
func speakerCheckResult(f installFacts) SpeakerCheck {
	v := classifySpeakerCheck(f)
	return SpeakerCheck{
		Host:       f.Host,
		Verdict:    v,
		AdviceKey:  speakerCheckAdviceKeys[v],
		PCNetworks: pcNetworksText(f.Ifaces),
		Summary:    summarizeSpeakerCheck(f, v),
	}
}

// classifySpeakerCheck decides the verdict. Every rule asks for positive
// evidence before the next, more blaming one is allowed to speak:
//
//  1. STR answered: nothing is wrong now (the list just has not caught up).
//  2. The Bose firmware answered on :8090 but STR on neither port: the one
//     case in which installing STR again can be the fix.
//  3. This PC has no network address at all: nothing about the speaker can
//     be known, and the PC is what to fix.
//  4. Something answers live at the address (ping reply, a port that refused
//     or opened): the speaker is on the network, its firmware is not
//     answering. A hung speaker; a restart is the fix.
//  5. Nothing live, and the address is outside this PC's networks: the two
//     are in different networks. Checked before 6 because a missing ping and
//     ARP entry is the expected answer across networks and says nothing about
//     power (report 2026-11-01). The failure report's own rule.
//  6. Nothing on this PC's own network, measured: ping ran without a reply
//     and there is no ARP entry. The speaker is off it. The failure report's
//     own rule, without its "seen in the list before" part: the check is only
//     offered for a speaker the list knows.
//  7. Nothing live, but ping could not run or an ARP entry is left over: the
//     same steps as 6, without claiming the network layer was silent.
func classifySpeakerCheck(f installFacts) string {
	if agentAnswered(f) {
		return checkOK
	}
	if boseFirmwareAnswers(f) {
		return checkSTRSilent
	}
	life := signOfLife(f)
	if !life && !pcHasAddress(f) {
		return checkPCOffline
	}
	if life {
		return checkHung
	}
	if addressInOtherNetwork(f) {
		return checkOtherNetwork
	}
	if silentAtNetworkLayer(f) {
		return checkOffNetwork
	}
	return checkNoAnswer
}

// portFact returns the answer for one probed port.
func portFact(f installFacts, port int) (portAnswer, bool) {
	for _, p := range f.Ports {
		if p.Port == port {
			return p, true
		}
	}
	return portAnswer{}, false
}

// agentAnswered: an STR port answered its version endpoint with 200. An open
// port that answers anything else is not counted: on a BCO chassis whose STR
// is gone, something other than the agent can sit behind the port.
func agentAnswered(f installFacts) bool {
	for _, port := range []int{8888, 17008} {
		if p, ok := portFact(f, port); ok && p.Result == "open" && strings.HasPrefix(p.Detail, "HTTP 200") {
			return true
		}
	}
	return false
}

// boseFirmwareAnswers: :8090 opened AND gave an HTTP answer. A port that
// opens and then says nothing is the wedged control stack, not a healthy
// firmware.
func boseFirmwareAnswers(f installFacts) bool {
	p, ok := portFact(f, 8090)
	return ok && p.Result == "open" && strings.HasPrefix(p.Detail, "HTTP ")
}

// signOfLife: something answered live at the speaker's address just now. An
// ARP entry alone is not one: Windows, Linux and macOS keep a speaker's
// hardware address for seconds to minutes after it left, and the check's own
// dials refresh a stale entry right before the lookup. Counting it called a
// speaker unplugged a minute ago "hung" and left out the power and Bose app
// steps. A hung firmware with a live kernel still answers the ping or
// refuses a port.
func signOfLife(f installFacts) bool {
	if f.PingRan && f.PingAlive {
		return true
	}
	for _, p := range f.Ports {
		if p.Result == "open" || strings.HasPrefix(p.Result, "connection refused") {
			return true
		}
	}
	return false
}

// pcHasAddress: this PC has at least one interface with an IPv4 address.
func pcHasAddress(f installFacts) bool {
	for _, in := range f.Ifaces {
		if in.CIDR == "" {
			continue
		}
		if ip, _, err := net.ParseCIDR(in.CIDR); err == nil && ip.To4() != nil {
			return true
		}
	}
	return false
}

// portsSummary is the port verdicts in one log field: "8090=open 8888=refused".
func portsSummary(ports []portAnswer) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		word := p.Result
		switch {
		case p.Result == "open":
		case strings.HasPrefix(p.Result, "connection refused"):
			word = "refused"
		case strings.HasPrefix(p.Result, "no answer"):
			word = "silent"
		case strings.HasPrefix(p.Result, "no route"):
			word = "noroute"
		case strings.Contains(p.Result, "unreachable"):
			word = "unreachable"
		default:
			if i := strings.IndexAny(word, " ("); i > 0 {
				word = word[:i]
			}
		}
		parts = append(parts, fmt.Sprintf("%d=%s", p.Port, word))
	}
	return strings.Join(parts, " ")
}

// summarizeSpeakerCheck is the one line the support mail carries: the verdict
// and the facts it rests on, in English like the rest of the mail body.
func summarizeSpeakerCheck(f installFacts, verdict string) string {
	ping := "not run"
	if f.PingRan {
		ping = "no"
		if f.PingAlive {
			ping = "yes"
		}
	}
	arp := "no"
	if f.MACPrefix != "" {
		arp = "yes"
	}
	var facts []string
	if ps := portsSummary(f.Ports); ps != "" {
		facts = append(facts, ps)
	}
	extra := "ping " + ping + ", arp " + arp
	if f.TargetSeen && f.TargetMissingSec > 0 {
		extra += ", list: no answer" + offlineForText(f.TargetMissingSec)
	}
	if verdict == checkOtherNetwork {
		extra += ", this PC on " + pcNetworksText(f.Ifaces)
	}
	facts = append(facts, extra)
	return fmt.Sprintf("speaker check %s: %s (%s)", f.Host, verdict, strings.Join(facts, "; "))
}
