package main

import (
	"strings"
	"testing"
)

// otherNetworkReport is the shape of the 2026-11-01 report: the speaker's
// address is outside the PC's only network, no ping reply, no ARP entry, and
// a sticky list entry that missed its probes for 91 s.
func otherNetworkReport() failureReport {
	return failureReport{
		Phase:   "install:not-reachable",
		ErrMsg:  "The speaker is not reachable on the network.\n\n" + notReachableAdvice,
		History: "install: FAILED step=preflight code=not-reachable",
		Facts: installFacts{
			Host:    "192.0.2.111",
			PingRan: true, PingAlive: false, MACPrefix: "",
			Ifaces: []localIface{
				{Name: "WLAN", CIDR: "198.51.100.28/24", Up: true},
				{Name: "Ethernet", CIDR: "203.0.113.5/24", Up: false},
			},
			SubnetKnown: true, SameSubnet: false,
			TargetSeen: true, TargetMissingSec: 91,
		},
	}
}

// A speaker in a different network than the PC must get the network-mismatch
// lead naming both sides, never the "off the network" verdict that a missing
// ARP entry cannot support outside the PC's own network.
func TestFormatFailureReportLeadsWithOtherNetwork(t *testing.T) {
	out := formatFailureReport(otherNetworkReport())

	idx := strings.Index(out, "what to try\n-----------\n")
	if idx < 0 {
		t.Fatalf("no what-to-try section:\n%s", out)
	}
	advice := out[idx+len("what to try\n-----------\n"):]
	wantLead := "The speaker has the address 192.0.2.111, which is in a different network than this PC (198.51.100.0/24 on WLAN)."
	if !strings.HasPrefix(advice, wantLead) {
		t.Errorf("advice must lead with the network mismatch naming both networks, got:\n%s", advice)
	}
	for _, want := range []string{"a second router, a repeater or mesh unit", "guest Wi-Fi", "Bose SoundTouch app", "without the Bose cloud", "Press Refresh in the speaker list"} {
		if !strings.Contains(advice, want) {
			t.Errorf("advice missing %q:\n%s", want, advice)
		}
	}
	for _, bad := range []string{"off the network, not a firewall", "Right now nothing at the speaker's address answers", notReachableAdvice[:40], "client isolation"} {
		if strings.Contains(out, bad) {
			t.Errorf("report must not contain %q for a speaker in another network:\n%s", bad, out)
		}
	}
	// The down interface is not named as "this PC's network".
	if strings.Contains(advice, "203.0.113.0/24") {
		t.Errorf("a down interface must not be named as the PC's network:\n%s", advice)
	}
	// The fact line no longer reads as if nothing answered.
	if !strings.Contains(out, "none (expected: this PC only has entries for its own network)") {
		t.Errorf("ARP fact line must say the empty entry is expected:\n%s", out)
	}
}

func TestOtherNetworkDiagnosisStaysOutOfOtherCases(t *testing.T) {
	// Same network: the off-the-network steps keep their place.
	same := otherNetworkReport()
	same.Facts.SameSubnet = true
	same.Facts.SubnetVia = "WLAN 198.51.100.28/24"
	out := formatFailureReport(same)
	if !strings.Contains(out, "off the network, not a firewall") || strings.Contains(out, "in a different network than this PC") {
		t.Errorf("same-network absent speaker must keep the off-the-network steps:\n%s", out)
	}

	// Routed: a ping answers across networks, so the mismatch is not the cause.
	routed := otherNetworkReport()
	routed.Facts.PingAlive = true
	if g := applyOtherNetworkDiagnosis([]string{notReachableAdvice}, routed); len(g) != 1 || g[0] != notReachableAdvice {
		t.Errorf("a routed speaker must keep the existing advice, got %v", g)
	}

	// Wrong phase.
	phase := otherNetworkReport()
	phase.Phase = "install:ssh-handshake"
	if g := applyOtherNetworkDiagnosis([]string{notReachableAdvice}, phase); len(g) != 1 {
		t.Errorf("wrong phase must not trigger the mismatch lead, got %v", g)
	}

	// The speaker answered (its agent reported): the mismatch cannot be what
	// stopped the app.
	answered := otherNetworkReport()
	answered.BoxNow = []string{"version: 1.3.1"}
	if g := applyOtherNetworkDiagnosis([]string{notReachableAdvice}, answered); len(g) != 1 {
		t.Errorf("a speaker that answered must not get the mismatch lead, got %v", g)
	}
}

func TestSpeakerOffTheNetworkIgnoresARPOutsideOwnNetwork(t *testing.T) {
	f := otherNetworkReport().Facts
	if speakerOffTheNetwork(f) {
		t.Fatal("a missing ARP entry for an address in another network is not evidence of an absent speaker")
	}
	f.SameSubnet = true
	if !speakerOffTheNetwork(f) {
		t.Fatal("same-network speaker with no ping, no ARP and a miss streak is off the network")
	}
}

// The miss streak counts from the first missed probe, and the sentence says
// exactly that, in plain English.
func TestLastSeenTextGrammar(t *testing.T) {
	cases := map[int]string{
		91:   "This PC has had no answer from the speaker for 91 s.",
		1500: "This PC has had no answer from the speaker for 25 min.",
		9000: "This PC has had no answer from the speaker for 2 h.",
	}
	for sec, want := range cases {
		if got := lastSeenText(installFacts{TargetMissingSec: sec}); got != want {
			t.Errorf("lastSeenText(%d) = %q, want %q", sec, got, want)
		}
		if strings.Contains(lastSeenText(installFacts{TargetMissingSec: sec}), "ago") {
			t.Errorf("lastSeenText(%d) must not combine a duration with \"ago\"", sec)
		}
	}
}

func TestPCNetworksText(t *testing.T) {
	if got := pcNetworksText(nil); got != "the networks this PC is on" {
		t.Errorf("no interfaces: %q", got)
	}
	got := pcNetworksText([]localIface{
		{Name: "WLAN", CIDR: "198.51.100.28/24", Up: true},
		{Name: "vEthernet", CIDR: "203.0.113.1/20", Up: true},
		{Name: "Bluetooth", CIDR: "", Up: true},
	})
	if want := "198.51.100.0/24 on WLAN, 203.0.112.0/20 on vEthernet"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Nothing up: fall back to every interface with an address.
	if got := pcNetworksText([]localIface{{Name: "WLAN", CIDR: "198.51.100.28/24"}}); got != "198.51.100.0/24 on WLAN" {
		t.Errorf("fallback: %q", got)
	}
}
