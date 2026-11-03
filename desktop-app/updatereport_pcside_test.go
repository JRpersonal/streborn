package main

import (
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

	// Default route only: the ARP and ping behind the off-the-network verdict
	// still went over the home network, so it stays and leads.
	got := applyPCSideDiagnosis([]string{off, firewallAdvice}, vpnReport(false))
	if len(got) != 2 || got[0] != off || !strings.HasPrefix(got[1], vpnAdvice) {
		t.Fatalf("default-route VPN: off-network first, VPN second, got %v", got)
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
