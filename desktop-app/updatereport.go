package main

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"
)

// failureReport is everything the copyable report prints, gathered before any
// of it is laid out. Split from the formatting on purpose: the layout is the
// part that keeps being got wrong (advice in the middle, facts missing), and
// as a pure function it can be asserted on without a speaker or a socket.
type failureReport struct {
	When          string
	AppVersion    string
	AppBuild      string
	Platform      string
	Host          string
	Port          int
	WantedVersion string
	Phase         string

	// ErrMsg is what the failing step said, advice paragraphs included; they
	// are peeled off during formatting and reprinted at the bottom.
	ErrMsg string

	Facts installFacts

	// BoxNow is the speaker's own report, key by key, when it answered.
	BoxNow []string
	// BoxNowErr is why it did not answer, when it did not. Its advice
	// paragraphs are peeled the same way ErrMsg's are.
	BoxNowErr string
	// BoxSkipped is set when the probe was not even attempted because the
	// port facts had already ruled both agent ports out.
	BoxSkipped bool

	History string
}

// UpdateFailureReport builds the copyable text a user sends in when an install
// or an update could not put a speaker into the state it was supposed to reach.
//
// The point is that the user should never have to describe a failure they
// cannot see, and that nobody should have to write back asking for the next
// fact. Both halves of that had drifted apart: the report gathered exactly ONE
// new thing (a single /api/agent/version call) and printed the advice the user
// had just read on screen around it. A real one, mailed in 2026-08-23 for an
// install onto 192.168.1.34, read in full: six header lines, one "context
// deadline exceeded", and three sentences about firewalls. It could not
// distinguish a speaker that was off from one on a guest network from one that
// was answering on a port nobody asked about.
//
// So the report now states facts first and advises last:
//
//	what failed          the step's own words, advice stripped out
//	network facts        ICMP, ARP, every relevant port and what it answered
//	this PC              interfaces, subnets, firewall profiles
//	on the network       what discovery can currently see
//	speaker reports now  the speaker's own values, when it answers
//	what the update did  the OTA/install journal for this speaker
//	app log              the log lines about this speaker
//	what to try          every advice paragraph, collected at the end
//
// Only data the user is already entitled to see about their own equipment, in
// plain text, so they can read it before deciding to send it. Deliberately NOT
// anonymized (the real addresses are the point), with two exceptions that are
// nobody's business either way: the speaker's MAC is cut to its vendor prefix
// and Wi-Fi names in the log tail are redacted.
func (a *App) UpdateFailureReport(host string, port int, phase, errMsg, targetVersion string) string {
	r := failureReport{
		When:          time.Now().Format(time.RFC3339),
		AppVersion:    appVersion,
		AppBuild:      appBuild,
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		Host:          host,
		Port:          port,
		WantedVersion: targetVersion,
		Phase:         phase,
		ErrMsg:        errMsg,
	}

	r.History = a.otaHistoryTail(host, 25)
	r.Facts = a.gatherInstallFacts(a.appCtx(), host)
	if a != nil && a.logger != nil {
		// One line, so the verdicts are in app.log too. A user who sends only
		// the diagnostic bundle and not the report text still arrives with the
		// findings rather than with "it does not work".
		a.logger.Info("failure report: gathered network facts", "host", host, "phase", phase,
			"pingRan", r.Facts.PingRan, "pingAlive", r.Facts.PingAlive,
			"arp", r.Facts.MACPrefix != "", "subnetKnown", r.Facts.SubnetKnown,
			"sameSubnet", r.Facts.SameSubnet)
	}

	// Skip the speaker's own report when the dials have already proven both
	// agent ports closed. That call is worth up to two full HTTP timeouts
	// against a speaker we know is silent, and the enriched report must not
	// take longer than the thin one it replaces.
	if r.Facts.agentPortsProvenClosed() {
		r.BoxSkipped = true
	} else if ver, err := a.BoxAgentVersion(host, port); err == nil {
		for _, k := range []string{
			"version", "build", "model", "friendlyName", "boxHealth",
			"goLibrespot", "goLibrespotDroppedForUpdate",
			"nandFreeBytes", "nandTotalBytes", "uptimeSec", "wlanCreds",
		} {
			if v, ok := ver[k]; ok && v != "" {
				r.BoxNow = append(r.BoxNow, fmt.Sprintf("%-27s %s", k+":", v))
			}
		}
		if fd := ver["foreignDirs"]; fd != "" {
			r.BoxNow = append(r.BoxNow, fmt.Sprintf("%-27s %s", "other software on speaker:", fd))
		}
	} else {
		// stripWrongBlame runs BEFORE the advice is peeled, so the swap still
		// decides WHICH paragraph ends up at the bottom of the report.
		r.BoxNowErr = stripWrongBlame(err, errMsg, r.History).Error()
	}

	return formatFailureReport(r)
}

// adviceParagraphs are the closing paragraphs a failure can end on. They are
// constants (see app_transport.go and install_str.go) precisely so this list
// can recognise them: the report has to be able to lift them out of the middle
// of the text and reprint them under the facts, and prose-matching a paragraph
// that keeps being reworded would quietly stop working the first time someone
// fixes a typo in it.
func adviceParagraphs() []string {
	return []string{
		firewallAdvice,
		answeredNotSTRAdvice,
		notReachableAdvice,
		installWindowClosedAdvice,
		controlUnresponsiveAdvice,
		restartingAfterUnlockAdvice,
		alreadyInstalledAdvice,
	}
}

// splitAdvice separates a failure message into the statement of fact at the
// top and the advice paragraphs under it.
//
// The messages are built as fact + "\n\n" + advice, so the split is on
// paragraph boundaries and each paragraph is compared against the known
// constants. Matched by PREFIX, not equality: two branches append a
// model-specific extra sentence to their advice (the Portable's ten-second
// AUX hold), and that must not stop the paragraph being recognised.
func splitAdvice(s string) (body string, advice []string) {
	var keep []string
	for _, para := range strings.Split(s, "\n\n") {
		trimmed := strings.TrimSpace(para)
		if trimmed == "" {
			continue
		}
		matched := false
		for _, known := range adviceParagraphs() {
			if strings.HasPrefix(trimmed, known) {
				advice = append(advice, trimmed)
				matched = true
				break
			}
		}
		if !matched {
			keep = append(keep, trimmed)
		}
	}
	return strings.Join(keep, "\n\n"), advice
}

// section writes a heading underlined to its own width, the shape the report
// has always used.
func section(b *strings.Builder, title string) {
	fmt.Fprintf(b, "\n%s\n%s\n", title, strings.Repeat("-", len(title)))
}

// formatFailureReport lays the gathered facts out. Pure: no speaker, no
// socket, no clock, so the ordering rule this whole change is about (facts
// above advice, always) is assertable in a test.
func formatFailureReport(r failureReport) string {
	var b strings.Builder
	b.WriteString("ST Reborn failure report\n")
	b.WriteString("========================\n\n")
	fmt.Fprintf(&b, "when          : %s\n", r.When)
	fmt.Fprintf(&b, "app version   : %s (build %s)\n", r.AppVersion, r.AppBuild)
	fmt.Fprintf(&b, "app platform  : %s\n", r.Platform)
	fmt.Fprintf(&b, "speaker       : %s:%d\n", r.Host, r.Port)
	if r.WantedVersion != "" {
		fmt.Fprintf(&b, "wanted version: %s\n", r.WantedVersion)
	}
	fmt.Fprintf(&b, "failed at     : %s\n", r.Phase)

	// Advice is collected as it is peeled and printed once, at the end. The
	// same paragraph reaches the report twice (the failing step's message and
	// the closing probe's error both carry the firewall paragraph), and
	// reading it twice makes the report look like it is insisting.
	var advice []string
	addAdvice := func(paras []string) {
		for _, p := range paras {
			dup := false
			for _, have := range advice {
				if have == p {
					dup = true
					break
				}
			}
			if !dup {
				advice = append(advice, p)
			}
		}
	}

	errBody, errAdvice := splitAdvice(strings.TrimSpace(r.ErrMsg))
	addAdvice(errAdvice)
	if errBody != "" {
		section(&b, "what failed")
		b.WriteString(errBody + "\n")
	}

	writeNetworkFacts(&b, r.Facts)
	writeThisPC(&b, r.Facts)
	writeLANSnapshot(&b, r.Facts)

	switch {
	case len(r.BoxNow) > 0:
		section(&b, "speaker reports now")
		b.WriteString(strings.Join(r.BoxNow, "\n") + "\n")
	case r.BoxSkipped:
		section(&b, "speaker reports now")
		b.WriteString("not asked: neither agent port (8888, 17008) accepted a connection, see above\n")
	case r.BoxNowErr != "":
		probeBody, probeAdvice := splitAdvice(strings.TrimSpace(r.BoxNowErr))
		addAdvice(probeAdvice)
		section(&b, "speaker reports now")
		fmt.Fprintf(&b, "NOT REACHABLE (%s)\n", probeBody)
	}

	if r.History != "" {
		section(&b, "what the update did")
		// Same pass as the log tail below. The journal now carries the install
		// path's own lines, and those end in `err=` followed by whatever the
		// failing step said, which on the SSH branches is arbitrary stderr.
		// The diagnostic bundle has run this exact file through scrubPII since
		// #187/#197; the mailed report must not be the softer of the two.
		b.WriteString(scrubIdentities(r.History) + "\n")
	}

	if r.Facts.LogTail != "" {
		section(&b, "app log (recent lines, personal details removed)")
		// Scrubbed here as well as in appLogTailFor. This is the point where
		// the text actually leaves the app, so the guarantee holds no matter
		// who filled the field. scrubIdentities is scrubPII minus the IP
		// masking: the addresses stay, because showing the user their own
		// addresses is the point of the report, while the Windows account
		// name, the MAC, the Bose deviceID and the friendly name go.
		b.WriteString(scrubIdentities(r.Facts.LogTail) + "\n")
	}

	advice = dropContradictedBlame(advice)
	advice = applyIsolationDiagnosis(advice, r)
	if len(advice) > 0 {
		section(&b, "what to try")
		for i, p := range advice {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p + "\n")
		}
	}

	b.WriteString("\nPlease send this text to str@sichtbar-app.de. Use the\n")
	b.WriteString("\"Save diagnostic logs\" button on this screen to write the diagnostic\n")
	b.WriteString("file, and attach that file to the same mail.\n")
	return b.String()
}

// dropContradictedBlame removes the firewall paragraph from the collected
// advice when the report also carries the paragraph that says the speaker
// answered.
//
// stripWrongBlame already makes this swap, but only inside the CLOSING PROBE's
// error. The failing step's own message carries its own copy of the firewall
// paragraph (reachabilityHint appends it to every transport error), and
// gathering all advice at the bottom of the report put the two side by side
// for the first time: "chase your firewall" immediately above "this is not
// your firewall, the speaker answered". Field report 2026-08-07 is the case
// that produced both at once, and the user chased a firewall for two days on
// the strength of the wrong one. The two can never both be right, and the one
// that says the speaker answered is the one with evidence behind it, so the
// other goes.
//
// Only that pair. The install preflight's not-reachable paragraph also
// mentions firewalls, but it is only ever printed when :22, :8090 AND :8091
// were all silent, which is not a case this contradiction can reach.
// applyIsolationDiagnosis leads the advice with the client-isolation cause and
// drops the generic firewall paragraph when the facts fit it: the speaker sits
// on THIS PC's subnet (it was discovered and shares the subnet) yet a ping to
// it, which no PC firewall can block, gets no answer. That is a Wi-Fi keeping
// its clients apart, not a firewall, and pointing at the firewall sent users
// chasing the wrong thing (Jens, #763). Only fires on the not-reachable phase,
// i.e. when :22, :8090 and :8091 were all silent.
func applyIsolationDiagnosis(advice []string, r failureReport) []string {
	f := r.Facts
	isolated := strings.Contains(r.Phase, "not-reachable") &&
		f.SubnetKnown && f.SameSubnet && f.PingRan && !f.PingAlive
	if !isolated {
		return advice
	}
	out := []string{isolationAdvice}
	for _, p := range advice {
		if strings.HasPrefix(p, firewallAdvice) || strings.HasPrefix(p, notReachableAdvice) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func dropContradictedBlame(advice []string) []string {
	answered := false
	for _, p := range advice {
		if strings.HasPrefix(p, answeredNotSTRAdvice) {
			answered = true
			break
		}
	}
	if !answered {
		return advice
	}
	out := advice[:0:0]
	for _, p := range advice {
		if strings.HasPrefix(p, firewallAdvice) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// factCol is the width of the left column every fact line shares. Wide enough
// for the longest port label, so the answers line up and the report can be
// read down its right-hand edge.
const factCol = 36

func writeNetworkFacts(b *strings.Builder, f installFacts) {
	if len(f.Ports) == 0 && !f.PingRan && !f.SubnetKnown {
		return
	}
	section(b, "network facts")
	fmt.Fprintf(b, "%-*s %s\n", factCol, "speaker address", f.Host)
	if f.PingRan {
		answer := "no reply"
		if f.PingAlive {
			answer = "answered"
		}
		fmt.Fprintf(b, "%-*s %s\n", factCol, "ping (ICMP)", answer)
	}
	if f.MACPrefix != "" {
		// An ARP entry with every TCP port silent is the signature of a
		// firewall or of Wi-Fi client isolation: something IS at that address
		// and answering at the Ethernet layer.
		fmt.Fprintf(b, "%-*s yes, hardware address starts %s\n", factCol, "ARP entry for the speaker", f.MACPrefix)
	} else {
		fmt.Fprintf(b, "%-*s none (nothing answered at that address)\n", factCol, "ARP entry for the speaker")
	}
	for _, p := range f.Ports {
		answer := p.Result
		if p.Detail != "" {
			answer += ", " + p.Detail
		}
		fmt.Fprintf(b, "%-*s %s\n", factCol, fmt.Sprintf("port %d, %s", p.Port, p.Label), answer)
	}
	if f.SubnetKnown {
		if f.SameSubnet {
			fmt.Fprintf(b, "%-*s yes, via %s\n", factCol, "same network as this PC", f.SubnetVia)
		} else {
			fmt.Fprintf(b, "%-*s NO, the speaker address is not in any network this PC is on\n",
				factCol, "same network as this PC")
		}
	}
}

func writeThisPC(b *strings.Builder, f installFacts) {
	if len(f.Ifaces) == 0 && f.Firewall == "" {
		return
	}
	section(b, "this PC")
	for _, in := range f.Ifaces {
		state := "down"
		if in.Up {
			state = "up"
		}
		addr := in.CIDR
		if addr == "" {
			addr = "(no IPv4 address)"
		}
		fmt.Fprintf(b, "%-*s %-20s %s\n", factCol, in.Name, addr, state)
	}
	if f.Firewall != "" {
		b.WriteString(f.Firewall + "\n")
	}
}

func writeLANSnapshot(b *strings.Builder, f installFacts) {
	if len(f.LAN) == 0 {
		return
	}
	section(b, "what ST Reborn can see on the network")
	for _, s := range f.LAN {
		kind := s.Kind
		if kind == "" {
			kind = "unknown"
		}
		line := fmt.Sprintf("%-16s %-12s %-8s %s", s.Host, s.Model, kind, s.Version)
		if s.Build != "" {
			line += " (build " + s.Build + ")"
		}
		if s.Offline {
			line += " [not answering right now]"
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
}

// stripWrongBlame rewrites the closing probe's advice when the rest of the
// report contradicts it.
//
// The probe that fills the "speaker reports now" line is a single request, and
// its advice is chosen from that request alone. When it times out, the user is
// told to go through their firewall and antivirus settings. That is the right
// advice for a speaker that never answers, and the wrong advice for the case
// this report was actually written for: field report 2026-08-07, where every
// line of the journal above it reads `status 400 ... body=""`. The speaker was
// answering all along, on the wrong port, and the closing probe simply happened
// to end in a timeout. The user chased a firewall for two days.
//
// So the whole attempt decides, not the last request: if anything in the error
// the update reported or in the journal shows the speaker returning an HTTP
// status, the firewall paragraph is replaced. A missing journal changes
// nothing, and a probe that already carries the right advice is left alone.
//
// This must keep running BEFORE splitAdvice peels the paragraphs out, or the
// swap decides nothing and the 2026-08-07 wrong-blame comes straight back.
func stripWrongBlame(probeErr error, errMsg, history string) error {
	if probeErr == nil || !strings.Contains(probeErr.Error(), firewallAdvice) {
		return probeErr
	}
	if !answeredNotSTR(errors.New(errMsg + "\n" + history)) {
		return probeErr
	}
	return errors.New(strings.Replace(probeErr.Error(), firewallAdvice, answeredNotSTRAdvice, 1))
}
