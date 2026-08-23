package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// timeoutErr is a net.Error that reports Timeout() == true, the shape
// net.DialTimeout returns when nothing ever answers.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "dial tcp 192.0.2.34:8090: i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestClassifyDialErrSeparatesRefusedFromSilent(t *testing.T) {
	// This is the whole reason the report grew a port section: portOpen()
	// returns a bool, and the difference between "refused" and "silent" is the
	// difference between a speaker that is there and one that is not.
	if got := classifyDialErr(nil, reportDialTimeout); got != "open" {
		t.Errorf("nil error should be open, got %q", got)
	}

	var ne net.Error = timeoutErr{}
	got := classifyDialErr(ne, 2*time.Second)
	if !strings.Contains(got, "no answer") || !strings.Contains(got, "2s") {
		t.Errorf("a timeout must read as silence with its budget, got %q", got)
	}
	if strings.Contains(got, "refused") {
		t.Errorf("a timeout must not be reported as a refusal, got %q", got)
	}

	refused := errors.New("dial tcp 192.0.2.34:8090: connectex: No connection could be made because the target machine actively refused it.")
	got = classifyDialErr(refused, reportDialTimeout)
	if !strings.Contains(got, "refused") {
		t.Errorf("a Windows refusal must be recognised, got %q", got)
	}
	if !strings.Contains(got, "IS reachable") {
		t.Errorf("a refusal proves reachability and must say so, got %q", got)
	}

	noRoute := errors.New("dial tcp 192.0.2.34:8090: connect: no route to host")
	if got := classifyDialErr(noRoute, reportDialTimeout); !strings.Contains(got, "no route to host") {
		t.Errorf("no route to host must survive classification, got %q", got)
	}
}

func TestPingSaysAliveIgnoresTheWindowsExitZeroTrap(t *testing.T) {
	// Windows ping exits 0 when the ROUTER answers "Destination host
	// unreachable" on the speaker's behalf. Trusting the exit code reports a
	// missing speaker as alive, which is the wrong direction for a report
	// whose job is to place blame.
	unreachable := "Pinging 192.0.2.34 with 32 bytes of data:\r\n" +
		"Reply from 192.0.2.1: Destination host unreachable.\r\n"
	if pingSaysAlive(true, unreachable) {
		t.Error("an exit-0 'Destination host unreachable' must not count as alive")
	}

	reply := "Reply from 192.0.2.34: bytes=32 time=3ms TTL=64\r\n"
	if !pingSaysAlive(true, reply) {
		t.Error("an echo reply carrying a TTL must count as alive")
	}
	// German Windows, same token, different sentence around it.
	deReply := "Antwort von 192.0.2.34: Bytes=32 Zeit=3ms TTL=64\r\n"
	if !pingSaysAlive(true, deReply) {
		t.Error("a localised reply still carries TTL= and must count as alive")
	}
	// Linux / macOS lower case.
	if !pingSaysAlive(true, "64 bytes from 192.0.2.34: icmp_seq=0 ttl=64 time=2.1 ms") {
		t.Error("a unix echo reply must count as alive")
	}
	if pingSaysAlive(false, "100% packet loss") {
		t.Error("total loss must not count as alive")
	}
}

func TestParseARPMACReadsAllThreePlatformListings(t *testing.T) {
	cases := []struct {
		name, out, ip, want string
	}{
		{
			name: "windows arp -a",
			out: "Interface: 192.0.2.10 --- 0xb\r\n" +
				"  Internet Address      Physical Address      Type\r\n" +
				"  192.0.2.34            aa-bb-cc-dd-ee-ff     dynamic\r\n",
			ip:   "192.0.2.34",
			want: "aa-bb-cc-dd-ee-ff",
		},
		{
			// macOS prints single-digit octets. A strict two-hex-digit regex
			// finds nothing here and the report silently loses the line.
			name: "macos arp -n",
			out:  "? (192.0.2.34) at 0:c:8a:dd:ee:ff on en0 ifscope [ethernet]\n",
			ip:   "192.0.2.34",
			want: "0:c:8a:dd:ee:ff",
		},
		{
			name: "linux ip neigh",
			out:  "192.0.2.34 dev wlan0 lladdr aa:bb:cc:dd:ee:ff REACHABLE\n",
			ip:   "192.0.2.34",
			want: "aa:bb:cc:dd:ee:ff",
		},
		{
			name: "no entry at all",
			out:  "192.0.2.34 dev wlan0  FAILED\n",
			ip:   "192.0.2.34",
			want: "",
		},
		{
			// A neighbouring address must not donate its hardware address.
			name: "longer address on an adjacent line",
			out: "  192.0.2.3             11-22-33-44-55-66     dynamic\r\n" +
				"  192.0.2.340           aa-bb-cc-dd-ee-ff     dynamic\r\n",
			ip:   "192.0.2.34",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseARPMAC(c.out, c.ip); got != c.want {
				t.Errorf("parseARPMAC = %q, want %q", got, c.want)
			}
		})
	}
}

func TestMACVendorPrefixHidesTheDeviceHalf(t *testing.T) {
	// A SoundTouch's MAC is its Bose deviceID, and this report is mailed. The
	// vendor half answers the question that matters (is the thing at that
	// address a Bose speaker at all); the rest is not shown.
	got := macVendorPrefix("aa-bb-cc-dd-ee-ff")
	if got != "aa-bb-cc-xx-xx-xx" {
		t.Errorf("windows form: got %q", got)
	}
	if got := macVendorPrefix("0:c:8a:dd:ee:ff"); got != "00:0c:8a:xx:xx:xx" {
		t.Errorf("macos short form must be padded, got %q", got)
	}
	for _, bad := range []string{"", "aa-bb-cc", "not a mac"} {
		if got := macVendorPrefix(bad); got != "" {
			t.Errorf("macVendorPrefix(%q) = %q, want empty", bad, got)
		}
	}
	if strings.Contains(macVendorPrefix("aa:bb:cc:dd:ee:ff"), "dd") {
		t.Error("the device half of the address leaked into the report")
	}
}

func TestSameSubnetTurnsTheGuessIntoAVerdict(t *testing.T) {
	// "make sure both are on the same Wi-Fi (not a guest network)" is printed
	// on every unreachable speaker while the app is holding both addresses.
	ifaces := []localIface{
		{Name: "Wi-Fi", CIDR: "192.168.1.22/24", Up: true},
		{Name: "vEthernet", CIDR: "172.28.16.1/20", Up: true},
		{Name: "Ethernet", CIDR: "", Up: false},
	}

	known, same, via := sameSubnet("192.168.1.34", ifaces)
	if !known || !same {
		t.Fatalf("same subnet not recognised: known=%v same=%v", known, same)
	}
	if !strings.Contains(via, "Wi-Fi") || !strings.Contains(via, "192.168.1.22/24") {
		t.Errorf("the matching interface must be named, got %q", via)
	}

	known, same, _ = sameSubnet("192.168.5.34", ifaces)
	if !known {
		t.Fatal("a cross-subnet verdict is still a verdict")
	}
	if same {
		t.Error("192.168.5.34 is on no local subnet and must not be reported as same")
	}

	if known, _, _ := sameSubnet("not-an-ip", ifaces); known {
		t.Error("an unparseable host cannot yield a verdict")
	}
	if known, _, _ := sameSubnet("192.168.1.34", []localIface{{Name: "Ethernet", Up: false}}); known {
		t.Error("with no interface address there is nothing to decide with")
	}
}

func TestAgentPortsProvenClosedGatesTheExtraProbe(t *testing.T) {
	closed := installFacts{Ports: []portAnswer{
		{Port: 8888, Result: "connection refused (the speaker IS reachable, nothing is listening on this port)"},
		{Port: 17008, Result: "no answer (silent, timed out after 2s)"},
	}}
	if !closed.agentPortsProvenClosed() {
		t.Error("both agent ports dead must skip the /api/agent/version probe")
	}
	open := installFacts{Ports: []portAnswer{
		{Port: 8888, Result: "no answer (silent, timed out after 2s)"},
		{Port: 17008, Result: "open"},
	}}
	if open.agentPortsProvenClosed() {
		t.Error("an open agent port must still be asked")
	}
	if (installFacts{}).agentPortsProvenClosed() {
		t.Error("no port evidence at all proves nothing and must not skip the probe")
	}
}

// TestFormatFailureReportPutsFactsAboveAdvice is the layout contract this
// whole change exists for. The report a user mailed in on 2026-08-23 was
// advice, one timeout string, and nothing measurable; the advice also sat in
// the MIDDLE, glued to the closing probe's error.
func TestFormatFailureReportPutsFactsAboveAdvice(t *testing.T) {
	r := failureReport{
		When:       "2026-08-23T10:00:00+02:00",
		AppVersion: "0.9.54",
		AppBuild:   "2026-08-23-1000",
		Platform:   "windows/amd64",
		Host:       "192.168.1.34",
		Port:       8888,
		Phase:      "install:not-reachable",
		ErrMsg: "The speaker is not reachable on the network (no answer on SSH port 22, the Bose port 8090, or the media port 8091 at 192.168.1.34).\n\n" +
			notReachableAdvice,
		BoxNowErr: `Get "http://192.168.1.34:8888/api/agent/version": context deadline exceeded` + "\n\n" + firewallAdvice,
		History:   "time=2026-08-23T09:59:00+02:00 host=192.168.1.34 install: started (model=ST10)",
		Facts: installFacts{
			Host: "192.168.1.34",
			Ports: []portAnswer{
				{Port: 22, Label: "install access (SSH)", Result: "no answer (silent, timed out after 2s)"},
				{Port: 8090, Label: "Bose control API", Result: "connection refused (the speaker IS reachable, nothing is listening on this port)"},
				{Port: 8091, Label: "media renderer (UPnP)", Result: "open", Detail: "HTTP 200 OK"},
			},
			PingRan:     true,
			PingAlive:   true,
			MACPrefix:   "aa-bb-cc-xx-xx-xx",
			Ifaces:      []localIface{{Name: "Wi-Fi", CIDR: "192.168.1.22/24", Up: true}},
			SubnetKnown: true,
			SameSubnet:  true,
			SubnetVia:   "Wi-Fi 192.168.1.22/24",
			Firewall:    "Windows firewall domain=on private=on public=off (this only says the firewall is switched on, not whether ST Reborn is allowed through it)",
			LAN: []lanSighting{
				{Host: "192.168.1.40", Model: "ST10", Kind: "str", Version: "0.9.53", Build: "2026-08-22-1200"},
			},
			LogTail: "time=... msg=\"install_str: preflight failed\" host=192.168.1.34 ssid=\"Koens Netz\"",
		},
	}

	out := formatFailureReport(r)

	adviceAt := strings.Index(out, "what to try")
	if adviceAt < 0 {
		t.Fatal("the advice section is gone; it must be kept, only moved")
	}
	// Every measured fact has to be readable before the first word of advice.
	for _, fact := range []string{
		"port 22", "port 8090", "connection refused", "ping (ICMP)",
		"ARP entry", "aa-bb-cc-xx-xx-xx", "same network as this PC",
		"192.168.1.22/24", "Windows firewall",
		"what ST Reborn can see on the network", "192.168.1.40",
		"what the update did", "install: started",
		"app log",
	} {
		at := strings.Index(out, fact)
		if at < 0 {
			t.Errorf("fact %q is missing from the report", fact)
			continue
		}
		if at > adviceAt {
			t.Errorf("fact %q is printed below the advice; facts come first", fact)
		}
	}

	// Both advice paragraphs end up at the bottom, and neither is left in the
	// middle glued to the error it came with.
	if strings.Index(out, notReachableAdvice) < adviceAt {
		t.Error("the install advice is still above the facts")
	}
	if strings.Index(out, firewallAdvice) < adviceAt {
		t.Error("the firewall paragraph is still glued into the middle of the report")
	}
	// The failure statement itself stays at the top, without its advice.
	whatFailed := strings.Index(out, "what failed")
	if whatFailed < 0 || whatFailed > adviceAt {
		t.Error("the failing step's own words must open the report")
	}

	// A Wi-Fi name never ships, even in the log tail (074b5dd).
	if strings.Contains(out, "Koens Netz") {
		t.Error("an SSID leaked into the failure report")
	}
	// ...but the addresses the report exists to show are NOT masked.
	if !strings.Contains(out, "192.168.1.34") || !strings.Contains(out, "192.168.1.22") {
		t.Error("the real addresses were scrubbed away; the report is deliberately not anonymized")
	}

	// The closing line names the button, instead of wishing for a file.
	if !strings.Contains(out, "Save diagnostic logs") {
		t.Error("the report must name the button that writes the diagnostic file")
	}
	// The frontend rewrites this closing block to name the file the user just
	// saved, and it finds it by this exact literal (REPORT_SEND_MARKER in
	// frontend/src/failreport.js). Rewording past it degrades to appending the
	// path at the very end, so pin it here: the drift would otherwise only be
	// visible to someone reading the JS.
	if !strings.Contains(out, "Please send this text to str@sichtbar-app.de") {
		t.Error("the closing marker the frontend rewrites is gone; failreport.js must be changed with it")
	}
}

func TestFormatFailureReportSaysWhyItDidNotAskTheSpeaker(t *testing.T) {
	// A skipped probe must be visible as a decision, not as a missing section:
	// "we did not ask" and "we asked and got nothing" are different facts.
	out := formatFailureReport(failureReport{
		Host: "192.168.1.34", Port: 8888, Phase: "install:not-reachable",
		BoxSkipped: true,
	})
	if !strings.Contains(out, "not asked") || !strings.Contains(out, "8888") {
		t.Errorf("the skipped probe must be explained, got:\n%s", out)
	}
}

func TestSplitAdviceRecognisesEveryPreflightParagraph(t *testing.T) {
	// Each preflight branch builds fact + "\n\n" + a constant from this list.
	// A branch whose advice is not recognised leaves the advice in the middle
	// of the report again, so assert on the full set rather than a sample.
	for _, known := range adviceParagraphs() {
		body, advice := splitAdvice("Something went wrong at 192.0.2.5.\n\n" + known)
		if body != "Something went wrong at 192.0.2.5." {
			t.Errorf("fact half mangled for %.40q...: %q", known, body)
		}
		if len(advice) != 1 || advice[0] != known {
			t.Errorf("advice not peeled for %.40q...: %#v", known, advice)
		}
	}
}

func TestSplitAdviceKeepsModelSpecificTailsWithTheirParagraph(t *testing.T) {
	// The control-unresponsive branch appends a Portable-only sentence to its
	// advice. Matching the constants by equality would orphan that paragraph
	// in the middle of the report, which is the bug being fixed.
	tail := " The Portable never fully powers off while it still has battery: hold the AUX button for about 10 seconds to force a restart."
	body, advice := splitAdvice("The speaker at 192.0.2.5 answers on 8091.\n\n" + controlUnresponsiveAdvice + tail)
	if body != "The speaker at 192.0.2.5 answers on 8091." {
		t.Errorf("fact half: %q", body)
	}
	if len(advice) != 1 || !strings.HasSuffix(advice[0], "force a restart.") {
		t.Errorf("the Portable sentence must travel with its paragraph: %#v", advice)
	}
}

func TestFormatFailureReportPrintsRepeatedAdviceOnce(t *testing.T) {
	// The failing step and the closing probe both carry the firewall
	// paragraph; reading it twice makes the report look like it is insisting.
	out := formatFailureReport(failureReport{
		Host:      "192.0.2.5",
		Phase:     "update",
		ErrMsg:    "push failed\n\n" + firewallAdvice,
		BoxNowErr: "context deadline exceeded\n\n" + firewallAdvice,
	})
	if n := strings.Count(out, firewallAdvice); n != 1 {
		t.Errorf("firewall advice printed %d times, want 1", n)
	}
}

// TestGatherInstallFactsStaysWithinItsBudget runs the real gatherer against
// the loopback address. It is the only test that exercises the concurrent
// half, so it is also where a data race between the parallel port probes and
// the ping goroutine would surface under -race.
//
// Bounded on purpose: a failure report that hangs is worse than a thin one,
// and this screen is shown to a user who has just been kept waiting by a
// failed install.
func TestGatherInstallFactsStaysWithinItsBudget(t *testing.T) {
	a := &App{}
	start := time.Now()
	f := a.gatherInstallFacts(context.Background(), "127.0.0.1")
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("gather took %s; the failure screen cannot wait that long", elapsed)
	}
	if len(f.Ports) != len(reportProbePorts) {
		t.Fatalf("got %d port answers, want %d", len(f.Ports), len(reportProbePorts))
	}
	for i, p := range f.Ports {
		if p.Result == "" {
			t.Errorf("port %d has no verdict; every probed port must report one", p.Port)
		}
		if p.Port != reportProbePorts[i].port {
			t.Errorf("port answers must stay in the declared order: got %d at %d", p.Port, i)
		}
	}
	// Loopback is filtered out of the interface list, so 127.0.0.1 can never
	// be "on the same network as this PC" by that route. Whatever the verdict,
	// it must not claim a match it cannot name.
	if f.SameSubnet && f.SubnetVia == "" {
		t.Error("a same-network verdict must name the interface it matched on")
	}
	if f.MACPrefix != "" && !strings.Contains(f.MACPrefix, "xx") {
		t.Errorf("a hardware address reached the report unmasked: %q", f.MACPrefix)
	}
}

func TestAppLogTailFallsBackQuietly(t *testing.T) {
	// A PC where the log file cannot be read must yield a thin report, never a
	// second failure on the screen the user is already stuck on.
	if got := appLogTailFor("192.0.2.5", 1024); len(got) > 1024 {
		t.Errorf("log tail escaped its cap: %d bytes", len(got))
	}
}

// TestFailureReportScrubsIdentitiesButKeepsAddresses is the privacy contract of
// the app-log tail. The report deliberately shows the user their own IPs, and
// the first cut of it read that as licence to ship the log tail through
// redactSSIDs alone: the Windows account name, the speaker's MAC and its Bose
// deviceID, and the user-chosen friendly name all travelled into a text meant
// to be mailed, two sections below the ARP line that masks the very same
// hardware address down to its vendor prefix. The diagnostic bundle has
// scrubbed all four out of the identical log file since #187/#197.
func TestFailureReportScrubsIdentitiesButKeepsAddresses(t *testing.T) {
	out := formatFailureReport(failureReport{
		Host: "192.168.1.34", Port: 8888, Phase: "install",
		Facts: installFacts{
			Host: "192.168.1.34",
			LogTail: `time=... msg="install_str: starting" host=192.168.1.34 ` +
				`mac=AA:BB:CC:DD:EE:FF deviceID=AABBCCDDEEFF ` +
				`"friendlyName":"Koens Kueche" ssid="Koens Netz" ` +
				`logFile=C:\Users\koen\AppData\Local\STReborn\str.log`,
		},
	})

	for _, leak := range []string{
		"AA:BB:CC:DD:EE:FF", // the MAC the ARP section masks on purpose
		"AABBCCDDEEFF",      // the same value as a Bose deviceID
		"Koens Kueche",      // the user-chosen speaker name
		"Koens Netz",        // the Wi-Fi name (074b5dd)
		`Users\koen`,        // the Windows account name, often a real first name
	} {
		if strings.Contains(out, leak) {
			t.Errorf("%q reached the mailed report; the diagnostic bundle removes it from the same log", leak)
		}
	}
	// ...and the addresses the report exists to show are NOT masked. maskIP
	// would rewrite them to 192.0.2.x, which is exactly the value the user
	// needs to read back to us.
	if !strings.Contains(out, "192.168.1.34") {
		t.Error("the speaker address was masked away; the report is deliberately not anonymized")
	}
}

// TestFailureReportDropsFirewallBlameWhenTheSpeakerAnswered guards the
// 2026-08-07 wrong-blame fix at its new weak point. stripWrongBlame swaps the
// paragraph inside the closing probe's error only; the failing step carries its
// own copy, and collecting all advice at the bottom put the two side by side.
func TestFailureReportDropsFirewallBlameWhenTheSpeakerAnswered(t *testing.T) {
	out := formatFailureReport(failureReport{
		Host: "192.0.2.5", Phase: "update",
		ErrMsg:    "status 400 on 192.0.2.5\n\n" + firewallAdvice,
		BoxNowErr: "context deadline exceeded\n\n" + answeredNotSTRAdvice,
	})
	if strings.Contains(out, firewallAdvice) {
		t.Error("the report tells the user to chase a firewall directly above telling them the speaker answered")
	}
	if !strings.Contains(out, answeredNotSTRAdvice) {
		t.Error("the advice with the evidence behind it must survive")
	}

	// With no contradiction the firewall paragraph is untouched: it is the
	// right advice for a speaker that never answered.
	out = formatFailureReport(failureReport{
		Host: "192.0.2.5", Phase: "update",
		ErrMsg: "context deadline exceeded\n\n" + firewallAdvice,
	})
	if !strings.Contains(out, firewallAdvice) {
		t.Error("a speaker that never answered must still get the firewall advice")
	}
}
