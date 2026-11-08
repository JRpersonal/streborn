package main

import (
	"fmt"
	"strings"
)

// writeRouteFacts prints which local address the routing table picks for the
// speaker and for the internet, and the VPN in the way when there is one.
// These are the lines that settle "is a VPN taking the connection?" from the
// report alone, without a second mail asking which VPN the user runs.
func writeRouteFacts(b *strings.Builder, f installFacts) {
	src := func(ip string) string {
		if ip == "" {
			return "none (the system offers no route)"
		}
		if in, ok := ifaceByIP(ip, f.Ifaces); ok {
			return fmt.Sprintf("from %s (%s)", ip, in.Name)
		}
		return "from " + ip
	}
	if f.Routes.OS == "" && f.Routes.SpeakerSrc == "" && f.Routes.DefaultSrc == "" {
		return
	}
	fmt.Fprintf(b, "%-*s %s\n", factCol, "route to the speaker", src(f.Routes.SpeakerSrc))
	fmt.Fprintf(b, "%-*s %s\n", factCol, "route to the internet", src(f.Routes.DefaultSrc))
	if f.VPN != nil {
		fmt.Fprintf(b, "%-*s %s\n", factCol, "VPN in the way", f.VPN.describe())
	}
	if f.VPN != nil || f.Routes.OS == "darwin" {
		answered := "no"
		if f.OtherSpeakerAnswered {
			answered = "yes, in the last few minutes"
		}
		fmt.Fprintf(b, "%-*s %s\n", factCol, "another speaker answered", answered)
	}
}

// anyPortAnswered reports whether a probed port gave an answer: it opened, or
// it refused, which only a reachable speaker can do. Either one proves the
// path from this PC to the speaker works, so no PC-side cause applies.
func anyPortAnswered(f installFacts) bool {
	for _, p := range f.Ports {
		if p.Result == "open" || strings.HasPrefix(p.Result, "connection refused") {
			return true
		}
	}
	return false
}

// targetAliveElsewhere reports whether something other than this app's own
// connections shows the speaker on the network: a ping reply, an ARP entry,
// or a fresh discovery sighting.
func targetAliveElsewhere(f installFacts) bool {
	return (f.PingRan && f.PingAlive) || f.MACPrefix != "" || f.TargetFresh
}

// macPortsDenied is macLocalNetworkDenied over the report's port probes:
// every port came back "no route" or "network unreachable" for a speaker
// inside the Mac's own network. "no route to host" alone is also a speaker
// that is off, so it needs the speaker shown alive some other way, and the
// off-the-network fingerprint or another speaker answering rules it out.
func macPortsDenied(f installFacts) bool {
	if f.Routes.OS != "darwin" || !f.SubnetKnown || !f.SameSubnet || len(f.Ports) == 0 {
		return false
	}
	if f.OtherSpeakerAnswered || speakerOffTheNetwork(f) {
		return false
	}
	needAlive := false
	for _, p := range f.Ports {
		switch {
		case strings.HasPrefix(p.Result, "network unreachable"):
		case strings.HasPrefix(p.Result, "no route to host"):
			needAlive = true
		default:
			return false
		}
	}
	return !needAlive || targetAliveElsewhere(f)
}

// vpnReportExplains is vpnMayExplain over the report's facts. A tunnel that
// carries the speaker connection explains any silence. One that only holds
// the default route is also how a full-tunnel VPN with working LAN access
// looks, so it counts only when nothing shows the home network getting
// through: a kill switch would have stopped the ping and the ARP traffic as
// well, and every other speaker with them.
func vpnReportExplains(f installFacts) bool {
	if f.VPN == nil {
		return false
	}
	if f.VPN.SpeakerRoute {
		return true
	}
	return !f.OtherSpeakerAnswered && !targetAliveElsewhere(f)
}

// isPCSideAdvice reports the two PC-side paragraphs, which reach the report
// either from applyPCSideDiagnosis or carried inside a probe error (see
// reachabilityHintFor).
func isPCSideAdvice(p string) bool {
	return strings.HasPrefix(p, vpnAdvice) || strings.HasPrefix(p, macLocalNetworkAdvice)
}

// dropPCSideAdvice removes the PC-side paragraphs and says whether there were
// any.
func dropPCSideAdvice(advice []string) (out []string, had bool) {
	out = advice[:0:0]
	for _, p := range advice {
		if isPCSideAdvice(p) {
			had = true
			continue
		}
		out = append(out, p)
	}
	return out, had
}

// hasReachabilityAdvice reports whether the advice already says something
// about why the speaker could not be reached.
func hasReachabilityAdvice(advice []string) bool {
	for _, p := range advice {
		for _, k := range []string{
			firewallAdvice, notReachableAdvice, isolationAdvice, noNetworkAdvice,
			noLocalRouteAdvice, answeredNotSTRAdvice, agentNotUpAdvice,
			repairBoxRestartedAdvice, boxInSetupAdvice, offNetworkLead, otherNetworkLead,
			stillRestartingAdvice,
		} {
			if strings.HasPrefix(p, k) {
				return true
			}
		}
	}
	return false
}

// applyPCSideDiagnosis leads the advice with a cause on this PC when the facts
// show one: a VPN that holds the route, or on a Mac the Local Network
// permission. Both make the speaker look exactly like one behind a firewall,
// behind client isolation, or switched off, so the firewall, router and
// isolation paragraphs go: they send the user to the router while the cause
// sits on the PC.
//
// When the facts do NOT show one, a PC-side paragraph that a probe error
// carried in goes instead. The facts gathered for the report are the fresher
// and the fuller evidence, and a VPN or permission paragraph next to "the
// speaker answered" or "ST Reborn reached your speaker" is the 2026-08-07
// wrong-blame in new words. Where that leaves the report with nothing on why
// the speaker was unreachable, the generic firewall paragraph the probe would
// have carried without the PC-side check comes back.
//
// Two paragraphs stay beside the VPN advice. The other-network one leads (a
// speaker outside every network of this PC is routed into the VPN by its
// default route, so either may be the cause). The off-the-network one follows
// it when the VPN only holds the default route: a kill switch blocks the ping
// and leaves the address table empty too, so that fingerprint fits both, and
// its steps (is the speaker on, does the Bose app see it) are the next thing
// to check once the VPN is off.
func applyPCSideDiagnosis(advice []string, r failureReport) []string {
	f := r.Facts
	speakerAnswered := reachedThisSession(r) || len(r.BoxNow) > 0 || anyPortAnswered(f) ||
		hasAdvice(advice, answeredNotSTRAdvice) || hasAdvice(advice, agentNotUpAdvice) ||
		hasAdvice(advice, repairBoxRestartedAdvice) || hasAdvice(advice, boxInSetupAdvice)
	if speakerAnswered {
		out, _ := dropPCSideAdvice(advice)
		return out
	}
	var lead string
	switch {
	case vpnReportExplains(f):
		lead = vpnAdviceFor(*f.VPN)
	case macPortsDenied(f):
		lead = macLocalNetworkAdvice
	default:
		out, had := dropPCSideAdvice(advice)
		if had && !hasReachabilityAdvice(out) {
			out = append([]string{firewallAdvice}, out...)
		}
		return out
	}
	vpn := f.VPN != nil && lead != macLocalNetworkAdvice
	var first, after, rest []string
	for _, p := range advice {
		switch {
		case isPCSideAdvice(p):
			// Replaced by the lead, which carries the facts' verdict.
		case strings.HasPrefix(p, firewallAdvice), strings.HasPrefix(p, notReachableAdvice),
			strings.HasPrefix(p, isolationAdvice), strings.HasPrefix(p, noNetworkAdvice),
			strings.HasPrefix(p, noLocalRouteAdvice):
		case strings.HasPrefix(p, offNetworkLead):
			if vpn && !f.VPN.SpeakerRoute {
				after = append(after, p)
			}
		case strings.HasPrefix(p, otherNetworkLead) && vpn:
			// otherNetworkAdvice: kept ahead of the VPN, see above.
			first = append(first, p)
		default:
			rest = append(rest, p)
		}
	}
	out := append(first, lead)
	out = append(out, after...)
	return append(out, rest...)
}

// otherNetworkLead is the opening of otherNetworkAdvice, the part before its
// first format verb, so the paragraph can be recognised in the collected
// advice the same way offNetworkLead is.
var otherNetworkLead = otherNetworkAdvice[:strings.Index(otherNetworkAdvice, "%s")]
