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

// macPortsDenied is macLocalNetworkDenied over the report's port probes:
// every port came back "no route" or "network unreachable" for a speaker
// inside the Mac's own network.
func macPortsDenied(f installFacts) bool {
	if f.Routes.OS != "darwin" || !f.SubnetKnown || !f.SameSubnet || len(f.Ports) == 0 {
		return false
	}
	for _, p := range f.Ports {
		if !strings.HasPrefix(p.Result, "no route to host") && !strings.HasPrefix(p.Result, "network unreachable") {
			return false
		}
	}
	return true
}

// applyPCSideDiagnosis leads the advice with a cause on this PC when the facts
// show one: a VPN that holds the route, or on a Mac the Local Network
// permission. Both make the speaker look exactly like one behind a firewall,
// behind client isolation, or switched off, so those paragraphs go: they send
// the user to the router or the speaker while the cause sits on the PC.
//
// Two paragraphs stay, ahead of the VPN advice rather than replaced by it: the
// other-network one (a speaker outside every network of this PC is routed
// into the VPN by its default route, so either may be the cause), and the
// off-the-network one when the VPN only holds the default route (the ARP and
// ping evidence behind it then still went over the home network and still
// means something).
func applyPCSideDiagnosis(advice []string, r failureReport) []string {
	f := r.Facts
	if reachedThisSession(r) || len(r.BoxNow) > 0 || anyPortAnswered(f) {
		return advice
	}
	var lead string
	keepOffNetwork := false
	switch {
	case f.VPN != nil:
		lead = vpnAdviceFor(*f.VPN)
		keepOffNetwork = !f.VPN.SpeakerRoute
	case macPortsDenied(f):
		lead = macLocalNetworkAdvice
	default:
		return advice
	}
	var first, rest []string
	for _, p := range advice {
		switch {
		case strings.HasPrefix(p, vpnAdvice), strings.HasPrefix(p, macLocalNetworkAdvice):
			// Replaced by the lead, which carries the facts' verdict.
		case strings.HasPrefix(p, firewallAdvice), strings.HasPrefix(p, notReachableAdvice),
			strings.HasPrefix(p, isolationAdvice), strings.HasPrefix(p, noNetworkAdvice),
			strings.HasPrefix(p, noLocalRouteAdvice):
		case strings.HasPrefix(p, offNetworkLead):
			if keepOffNetwork && f.VPN != nil {
				first = append(first, p)
			}
		case strings.HasPrefix(p, otherNetworkLead) && f.VPN != nil:
			// otherNetworkAdvice: kept ahead of the VPN, see above.
			first = append(first, p)
		default:
			rest = append(rest, p)
		}
	}
	out := append(first, lead)
	return append(out, rest...)
}

// otherNetworkLead is the opening of otherNetworkAdvice, the part before its
// first format verb, so the paragraph can be recognised in the collected
// advice the same way offNetworkLead is.
var otherNetworkLead = otherNetworkAdvice[:strings.Index(otherNetworkAdvice, "%s")]
