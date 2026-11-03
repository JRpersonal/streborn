package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func silentPorts(result string) []portAnswer {
	var out []portAnswer
	for _, p := range reportProbePorts {
		out = append(out, portAnswer{Port: p.port, Label: p.label, Result: result})
	}
	return out
}

func vpnReport(speakerRoute bool) failureReport {
	nord := localIface{Name: "NordLynx", CIDR: "10.5.0.2/32", Up: true, Virtual: true}
	routes := pcRoutes{OS: "windows", SpeakerSrc: testPCAddr, DefaultSrc: "10.5.0.2"}
	if speakerRoute {
		routes.SpeakerSrc = "10.5.0.2"
	}
	v, _ := findVPN(testSpeaker, []localIface{testWiFi, nord}, routes)
	return failureReport{
		Host:  testSpeaker,
		Phase: "install:not-reachable",
		Facts: installFacts{
			Host:        testSpeaker,
			Ports:       silentPorts("no answer (silent, timed out after 2s)"),
			PingRan:     true,
			Ifaces:      []localIface{testWiFi, nord},
			SubnetKnown: true,
			SameSubnet:  true,
			SubnetVia:   "Wi-Fi " + testWiFi.CIDR,
			Routes:      routes,
			VPN:         &v,
		},
	}
}

func TestApplyPCSideDiagnosisLeadsWithTheVPN(t *testing.T) {
	r := vpnReport(false)
	got := applyPCSideDiagnosis([]string{isolationAdvice, firewallAdvice, notReachableAdvice, "keep me"}, r)
	if len(got) == 0 || !strings.HasPrefix(got[0], vpnAdvice) || !strings.Contains(got[0], "NordLynx") {
		t.Fatalf("the VPN advice must lead and name the adapter, got %v", got)
	}
	for _, p := range got {
		if p == isolationAdvice || p == firewallAdvice || p == notReachableAdvice {
			t.Fatalf("the router/firewall paragraphs must go when a VPN holds the route: %q", p)
		}
	}
	if got[len(got)-1] != "keep me" {
		t.Fatalf("unrelated advice must survive, got %v", got)
	}

	// A port that answered proves the path works: nothing changes.
	answered := vpnReport(false)
	answered.Facts.Ports[1].Result = "connection refused (the speaker IS reachable, nothing is listening on this port)"
	if g := applyPCSideDiagnosis([]string{firewallAdvice}, answered); len(g) != 1 || g[0] != firewallAdvice {
		t.Fatalf("a refusing speaker must keep its advice, got %v", g)
	}
	// So does a speaker that reported in.
	boxNow := vpnReport(false)
	boxNow.BoxNow = []string{"version: 1.3.2"}
	if g := applyPCSideDiagnosis([]string{firewallAdvice}, boxNow); len(g) != 1 {
		t.Fatalf("a speaker that answered must keep its advice, got %v", g)
	}
	// And so does a run that provably got inside the speaker.
	reached := vpnReport(false)
	reached.History = "install_str: ssh ok"
	if g := applyPCSideDiagnosis([]string{firewallAdvice}, reached); len(g) != 1 {
		t.Fatalf("a run that reached the speaker must keep its advice, got %v", g)
	}
	// No VPN, not a Mac: untouched.
	plain := vpnReport(false)
	plain.Facts.VPN = nil
	if g := applyPCSideDiagnosis([]string{firewallAdvice}, plain); len(g) != 1 || g[0] != firewallAdvice {
		t.Fatalf("no PC-side cause, no change, got %v", g)
	}
}

func TestApplyPCSideDiagnosisKeepsTheEvidenceThatStillCounts(t *testing.T) {
	off := fmt.Sprintf(offNetworkAdvice, "This PC has had no answer from the speaker for 3 min.")
	other := fmt.Sprintf(otherNetworkAdvice, testSpeaker, "198.51.100.0/24 on Wi-Fi")

	// Default route only: a kill switch blocks the ping and leaves the ARP
	// table empty too, so the off-the-network fingerprint fits both. The VPN
	// leads (it is the quicker thing to rule out), the off-network steps
	// follow it.
	got := applyPCSideDiagnosis([]string{off, firewallAdvice}, vpnReport(false))
	if len(got) != 2 || !strings.HasPrefix(got[0], vpnAdvice) || got[1] != off {
		t.Fatalf("default-route VPN: VPN first, off-network second, got %v", got)
	}
	// The speaker route inside the tunnel: that evidence never touched the
	// home network, so it goes.
	got = applyPCSideDiagnosis([]string{off, firewallAdvice}, vpnReport(true))
	if len(got) != 1 || !strings.HasPrefix(got[0], vpnAdvice) {
		t.Fatalf("speaker-route VPN: the VPN alone, got %v", got)
	}
	// A speaker in another network: either may be the cause, both stay.
	got = applyPCSideDiagnosis([]string{other}, vpnReport(true))
	if len(got) != 2 || got[0] != other || !strings.HasPrefix(got[1], vpnAdvice) {
		t.Fatalf("other network stays ahead of the VPN, got %v", got)
	}
}

// macDeniedReport is a Mac whose every port says "no route to host" for a
// speaker in its own network that discovery saw a moment ago.
func macDeniedReport() failureReport {
	en0 := localIface{Name: "en0", CIDR: testPCAddr + "/24", Up: true}
	return failureReport{
		Host:  testSpeaker,
		Phase: "install:not-reachable",
		Facts: installFacts{
			Host:        testSpeaker,
			Ports:       silentPorts("no route to host (this PC has no path to that address)"),
			PingRan:     true,
			Ifaces:      []localIface{en0},
			SubnetKnown: true,
			SameSubnet:  true,
			SubnetVia:   "en0 " + en0.CIDR,
			Routes:      pcRoutes{OS: "darwin", SpeakerSrc: testPCAddr, DefaultSrc: testPCAddr},
			TargetSeen:  true,
			TargetFresh: true,
		},
	}
}

func TestApplyPCSideDiagnosisNamesTheMacPermission(t *testing.T) {
	r := macDeniedReport()
	got := applyPCSideDiagnosis([]string{isolationAdvice, noNetworkAdvice, "keep me"}, r)
	if len(got) != 2 || got[0] != macLocalNetworkAdvice || got[1] != "keep me" {
		t.Fatalf("the Local Network advice must replace isolation and no-network, got %v", got)
	}

	// One port with a different answer breaks the fingerprint.
	mixed := macDeniedReport()
	mixed.Facts.Ports[0].Result = "host unreachable (nothing answered at the network layer)"
	if g := applyPCSideDiagnosis([]string{isolationAdvice}, mixed); len(g) != 1 || g[0] != isolationAdvice {
		t.Fatalf("a mixed answer is not the permission, got %v", g)
	}
	// Windows with the same picture is not the permission either.
	win := macDeniedReport()
	win.Facts.Routes.OS = "windows"
	if g := applyPCSideDiagnosis([]string{isolationAdvice}, win); len(g) != 1 || g[0] != isolationAdvice {
		t.Fatalf("windows must not get the macOS advice, got %v", g)
	}
	// A speaker outside the Mac's network: "no route" may simply be true.
	other := macDeniedReport()
	other.Facts.SameSubnet = false
	if g := applyPCSideDiagnosis([]string{isolationAdvice}, other); len(g) != 1 {
		t.Fatalf("another network is not the permission, got %v", g)
	}
	// "no route to host" with nothing showing the speaker alive is also a
	// speaker that is off: not the permission.
	unproven := macDeniedReport()
	unproven.Facts.TargetFresh = false
	if g := applyPCSideDiagnosis([]string{isolationAdvice}, unproven); len(g) != 1 || g[0] != isolationAdvice {
		t.Fatalf("no sign of life: not the permission, got %v", g)
	}
	// A ping reply or an ARP entry shows it alive just as well.
	pinged := macDeniedReport()
	pinged.Facts.TargetFresh = false
	pinged.Facts.PingAlive = true
	if g := applyPCSideDiagnosis([]string{isolationAdvice}, pinged); len(g) != 1 || g[0] != macLocalNetworkAdvice {
		t.Fatalf("speaker answers ping but not the app: the permission, got %v", g)
	}
	// "network unreachable" inside the Mac's own network needs no more.
	netun := macDeniedReport()
	netun.Facts.TargetFresh = false
	netun.Facts.Ports = silentPorts("network unreachable (this PC is not on that network at all)")
	if g := applyPCSideDiagnosis([]string{noNetworkAdvice}, netun); len(g) != 1 || g[0] != macLocalNetworkAdvice {
		t.Fatalf("ENETUNREACH in the Mac's own network: the permission, got %v", g)
	}
	// The permission covers the whole app: another speaker answering rules
	// it out.
	others := macDeniedReport()
	others.Facts.OtherSpeakerAnswered = true
	if g := applyPCSideDiagnosis([]string{isolationAdvice}, others); len(g) != 1 || g[0] != isolationAdvice {
		t.Fatalf("another speaker answered: not the permission, got %v", g)
	}
}

func TestFailureReportOnAMacWithTheSpeakerOffKeepsTheOffNetworkSteps(t *testing.T) {
	r := macDeniedReport()
	r.Facts.TargetFresh = false
	r.Facts.TargetMissingSec = 240
	// The live hint may have carried the permission paragraph in before
	// the facts were known; the report must not keep it either.
	r.ErrMsg = "dial tcp 192.0.2.34:8888: connect: no route to host\n\n" + macLocalNetworkAdvice
	out := formatFailureReport(r)
	tail := out[strings.Index(out, "what to try"):]
	if !strings.Contains(tail, offNetworkLead) {
		t.Fatalf("a speaker with no ping, no ARP entry and a miss streak is off the network:\n%s", tail)
	}
	if strings.Contains(tail, "Local Network") {
		t.Errorf("the permission must not be blamed for a speaker that is off:\n%s", tail)
	}
}

func TestApplyPCSideDiagnosisLeavesAWorkingHomeNetworkAlone(t *testing.T) {
	// The speaker answers ping over the Wi-Fi route: a kill switch would
	// have stopped that too, so the VPN is not what blocks the ports.
	pinged := vpnReport(false)
	pinged.Facts.PingAlive = true
	carried := vpnAdviceFor(*pinged.Facts.VPN)
	got := applyPCSideDiagnosis([]string{firewallAdvice, carried}, pinged)
	if len(got) != 1 || got[0] != firewallAdvice {
		t.Fatalf("ping alive through a default-route VPN: firewall stays, VPN goes, got %v", got)
	}
	// An ARP entry for the speaker says the same.
	arp := vpnReport(false)
	arp.Facts.MACPrefix = "AA:BB:CC"
	if got := applyPCSideDiagnosis([]string{firewallAdvice}, arp); len(got) != 1 || got[0] != firewallAdvice {
		t.Fatalf("ARP entry through a default-route VPN: firewall stays, got %v", got)
	}
	// Another speaker answered through the same VPN: the home network works.
	others := vpnReport(false)
	others.Facts.OtherSpeakerAnswered = true
	if got := applyPCSideDiagnosis([]string{notReachableAdvice}, others); len(got) != 1 || got[0] != notReachableAdvice {
		t.Fatalf("another speaker answered: the old advice stays, got %v", got)
	}
	// The carried VPN paragraph was the only reachability advice: the
	// generic one it replaced comes back rather than nothing at all.
	if got := applyPCSideDiagnosis([]string{carried, "keep me"}, others); len(got) != 2 || got[0] != firewallAdvice || got[1] != "keep me" {
		t.Fatalf("a dropped VPN paragraph falls back to the firewall advice, got %v", got)
	}
	// A tunnel that carries the speaker connection itself is still the
	// VPN's, whatever the ping says.
	route := vpnReport(true)
	route.Facts.PingAlive = true
	if got := applyPCSideDiagnosis([]string{firewallAdvice}, route); len(got) != 1 || !strings.HasPrefix(got[0], vpnAdvice) {
		t.Fatalf("speaker route in the tunnel: the VPN leads, got %v", got)
	}
}

func TestApplyPCSideDiagnosisDropsACarriedVPNParagraphWhenTheSpeakerAnswered(t *testing.T) {
	carried := vpnAdviceFor(*vpnReport(false).Facts.VPN)
	cases := map[string]func(*failureReport){
		"the run reached the speaker": func(r *failureReport) { r.History = "install_str: ssh ok" },
		"the speaker reported in":     func(r *failureReport) { r.BoxNow = []string{"version: 1.3.2"} },
		"a port answered": func(r *failureReport) {
			r.Facts.Ports[0].Result = "open"
		},
	}
	for name, mod := range cases {
		r := vpnReport(false)
		mod(&r)
		got := applyPCSideDiagnosis([]string{firewallAdvice, carried, macLocalNetworkAdvice}, r)
		for _, p := range got {
			if isPCSideAdvice(p) {
				t.Errorf("%s: the carried PC-side paragraph must go, got %v", name, got)
			}
		}
	}
}

func TestFailureReportNeverBlamesTheVPNForASpeakerThatAnswered(t *testing.T) {
	// (a) The run got inside the speaker over SSH, the closing error still
	// carries the VPN paragraph.
	r := vpnReport(false)
	r.History = "install_str: ssh ok"
	r.ErrMsg = "the agent did not come up: dial tcp 192.0.2.34:8888: i/o timeout\n\n" + vpnAdviceFor(*r.Facts.VPN)
	out := formatFailureReport(r)
	tail := out[strings.Index(out, "what to try"):]
	if !strings.Contains(tail, agentNotUpAdvice) {
		t.Fatalf("(a) the reached-this-session advice must lead:\n%s", tail)
	}
	if strings.Contains(tail, vpnAdvice) {
		t.Errorf("(a) the VPN must not be blamed next to \"your network is fine\":\n%s", tail)
	}

	// (b) The journal shows a status 400, the closing probe timed out with
	// the VPN paragraph, one port is open.
	b := vpnReport(false)
	b.ErrMsg = "status 400 Bad Request body=\"\"\n\n" + answeredNotSTRAdvice
	b.BoxNowErr = stripWrongBlame(errors.New("dial tcp 192.0.2.34:8888: i/o timeout\n\n"+vpnAdviceFor(*b.Facts.VPN)), b.ErrMsg, "").Error()
	b.Facts.Ports[0].Result = "open"
	out = formatFailureReport(b)
	tail = out[strings.Index(out, "what to try"):]
	if !strings.Contains(tail, answeredNotSTRAdvice) {
		t.Fatalf("(b) the answered advice must stay:\n%s", tail)
	}
	if strings.Contains(tail, vpnAdvice) || strings.Contains(tail, "VPN adapter on this PC") {
		t.Errorf("(b) the VPN must not be blamed next to \"the speaker answered\":\n%s", tail)
	}

	// The same pair without an open port: the answered paragraph alone
	// settles it.
	c := b
	c.Facts.Ports = silentPorts("no answer (silent, timed out after 2s)")
	out = formatFailureReport(c)
	tail = out[strings.Index(out, "what to try"):]
	if strings.Contains(tail, vpnAdvice) || !strings.Contains(tail, answeredNotSTRAdvice) {
		t.Errorf("(c) the speaker answered, so no VPN blame:\n%s", tail)
	}
}

func TestStripWrongBlameSwapsThePCSideParagraphs(t *testing.T) {
	v := vpnAdviceFor(*vpnReport(false).Facts.VPN)
	probe := errors.New("dial tcp 192.0.2.34:8888: i/o timeout\n\n" + v + "\n\nkeep me")
	got := stripWrongBlame(probe, "status 400 Bad Request", "").Error()
	if strings.Contains(got, vpnAdvice) || strings.Contains(got, "NordLynx") {
		t.Fatalf("the whole VPN paragraph, adapter note included, must go: %q", got)
	}
	if !strings.Contains(got, answeredNotSTRAdvice) || !strings.HasSuffix(got, "\n\nkeep me") {
		t.Fatalf("swapped for the answered advice, the rest intact: %q", got)
	}
	mac := errors.New("connect: no route to host\n\n" + macLocalNetworkAdvice)
	if got := stripWrongBlame(mac, "", "status 404 on 192.0.2.34").Error(); strings.Contains(got, macLocalNetworkAdvice) || !strings.Contains(got, answeredNotSTRAdvice) {
		t.Fatalf("the macOS paragraph is swapped the same way: %q", got)
	}
	// Without evidence of an answer nothing changes.
	if got := stripWrongBlame(probe, "dial tcp: i/o timeout", ""); got != probe {
		t.Fatalf("no answer in the run: probe unchanged, got %q", got)
	}
}

func TestFailureReportPrintsTheRouteFactsAndTheVPNAdvice(t *testing.T) {
	r := vpnReport(false)
	r.ErrMsg = "The speaker is not reachable on the network.\n\n" + notReachableAdvice
	out := formatFailureReport(r)
	for _, want := range []string{
		"route to the speaker",
		"from " + testPCAddr + " (Wi-Fi)",
		"route to the internet",
		"from 10.5.0.2 (NordLynx)",
		"VPN in the way",
		"NordLynx 10.5.0.2/32 holds this PC's default route",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report misses %q:\n%s", want, out)
		}
	}
	tail := out[strings.Index(out, "what to try"):]
	if !strings.Contains(tail, vpnAdvice) {
		t.Errorf("the VPN advice must be in what to try:\n%s", tail)
	}
	if strings.Contains(tail, notReachableAdvice) || strings.Contains(tail, "client isolation") {
		t.Errorf("firewall and isolation blame must be gone:\n%s", tail)
	}

	// The VPN paragraph carried by a probe error is recognised as advice, so
	// it is lifted out of the facts and printed once.
	_, adv := splitAdvice("dial tcp: i/o timeout\n\n" + vpnAdviceFor(*r.Facts.VPN))
	if len(adv) != 1 {
		t.Fatalf("the named VPN paragraph must be recognised as advice, got %v", adv)
	}
	_, adv = splitAdvice("connect: no route to host\n\n" + macLocalNetworkAdvice)
	if len(adv) != 1 {
		t.Fatalf("the macOS paragraph must be recognised as advice, got %v", adv)
	}
}

func TestFailureReportOnADeniedMacLeadsWithThePermission(t *testing.T) {
	r := macDeniedReport()
	r.ErrMsg = "The speaker is not reachable on the network.\n\n" + notReachableAdvice
	out := formatFailureReport(r)
	tail := out[strings.Index(out, "what to try"):]
	if !strings.Contains(tail, macLocalNetworkAdvice) {
		t.Fatalf("the Local Network advice must be in what to try:\n%s", tail)
	}
	if strings.Contains(tail, "client isolation") || strings.Contains(tail, notReachableAdvice) {
		t.Errorf("isolation and firewall blame must be gone on a denied Mac:\n%s", tail)
	}
}
