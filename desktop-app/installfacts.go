package main

// Everything ST Reborn can find out about a failed install or update at the
// moment it fails, without asking the user a single question.
//
// Why this file exists: the report a user mailed in on 2026-08-23 (an install
// onto 192.168.1.34 that never reached the speaker) consisted of six header
// lines, one "context deadline exceeded" string, and three sentences of
// firewall advice the same user had just read on the screen above it. Nothing
// in it could tell apart the four failures that actually happen here:
//
//	the speaker is off or asleep,
//	the speaker is on a different subnet than this PC (guest Wi-Fi),
//	something dropped the packets (firewall / client isolation),
//	the speaker answered fine but on a port we did not ask about.
//
// All four are decidable from this PC in a few seconds, and every fact is
// about the user's own equipment, so the report carries them now and nobody
// has to write back asking for a second attempt that nobody makes.
//
// Hard rules for everything below:
//   - best effort. A probe that cannot run omits its line. A failure report
//     that itself fails is worse than a thin one.
//   - bounded. The user is staring at a failure screen; the whole gather stays
//     inside a few seconds and every subprocess runs under a context.
//   - no more of the user's data than the user already sees. The report is
//     deliberately NOT anonymized (the real IPs are the point of it), but a
//     speaker MAC is its Bose deviceID and a Wi-Fi name is nobody's business,
//     so the MAC is cut to its vendor prefix and the log tail goes through
//     redactSSIDs, same rule as commit 074b5dd.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// reportDialTimeout is the per-request budget: one dial, and on a port that
// opens one follow-up request under the same figure. Every port is probed in
// parallel, so the port section costs one budget for a dead LAN and at most
// two for a port that opens and then says nothing. Two seconds is long enough
// that a busy speaker on marginal Wi-Fi still answers and short enough that
// five dead ports do not make the failure screen feel hung.
const reportDialTimeout = 2 * time.Second

// portAnswer is one port's verdict. Result is always filled (it is the whole
// point of the section); Detail carries the extra fact an OPEN port gives up
// for free, which is the one that usually decides the case: an HTTP status
// from the agent port, or the SSH server banner.
type portAnswer struct {
	Port   int
	Label  string
	Result string
	Detail string
}

// localIface is one of this PC's own network interfaces as the report prints
// it. CIDR is empty when the interface has no IPv4 address at all, which is
// itself worth showing: a PC whose only "up" interface is a VPN tunnel cannot
// reach a speaker on the house LAN, and that has been reported as a firewall
// problem more than once.
type localIface struct {
	Name string
	CIDR string
	Up   bool
}

// lanSighting is one speaker out of the discovery cache. Taken from the cache
// rather than probed: a fresh discovery run would cost seconds and, worse,
// would race the install that just failed.
type lanSighting struct {
	Host    string
	Model   string
	Version string
	Build   string
	Kind    string
	Offline bool
}

// installFacts is the gathered set. Zero values mean "not established", never
// "false": a missing ping line says the probe could not run, not that the
// speaker is dead.
type installFacts struct {
	Host string

	Ports []portAnswer

	// PingRan is false when ping could not be executed at all (locked-down PC,
	// no ping on PATH, policy). PingAlive is only meaningful when PingRan.
	PingRan   bool
	PingAlive bool

	// MACPrefix is the vendor half of the ARP entry for the speaker, or empty
	// when the host has no ARP entry. An entry proves the host answered at the
	// Ethernet/Wi-Fi layer even when every TCP port is silent, which is the
	// signature of a firewall or of client isolation rather than an absent box.
	MACPrefix string

	Ifaces []localIface

	// SameSubnet is filled only when the verdict could be reached at all
	// (a parseable host address and at least one interface with an IPv4).
	SubnetKnown bool
	SameSubnet  bool
	SubnetVia   string

	// Firewall is a plain-text line about this PC's firewall, or empty on
	// platforms and configurations where it cannot be read without prompting.
	Firewall string

	LAN []lanSighting

	LogTail string
}

// reportProbePorts are the ports whose answers separate the failure modes we
// actually see. Every one of them earns its place:
//
//	22    Bose only opens sshd while the speaker boots with the STR stick in,
//	      so open-vs-closed here is "install window open" vs "shut".
//	8090  the stock Bose control API. Open means the speaker is on the network
//	      and healthy enough to serve its own firmware, whatever STR says.
//	8091  the UPnP media renderer, a SEPARATE firmware process. 8090 dead and
//	      8091 alive is the wedged-control-stack signature, not a dead box.
//	8888  STR's webui on sm2 chassis (ST10 rhino, ST30 mojo, Wave lisa).
//	17008 STR's webui on BCO/whitelisted chassis (Portable taigan, ST20
//	      spotty), where :8888 is loopback-only. Probing only one of the two
//	      has already produced a false "STR absent" verdict once.
//
// The HTTP path is fetched when the port opens, so an open port reports what
// answered rather than only that something did. Port 22 has no path: it gets
// its banner read instead, which names the SSH server the box is running.
var reportProbePorts = []struct {
	port  int
	label string
	path  string
}{
	{22, "install access (SSH)", ""},
	{8090, "Bose control API", "/info"},
	{8091, "media renderer (UPnP)", "/"},
	{8888, "STR agent (sm2 chassis)", "/api/agent/version"},
	{17008, "STR agent (BCO chassis)", "/api/agent/version"},
}

// classifyDialErr turns a dial outcome into a sentence a user can act on.
//
// The distinction that matters is refused vs silent. A refusal is an answer:
// the host is there and reachable, it simply has nothing listening on that
// port. Silence is not an answer, and it is what a firewall, a guest network,
// and a powered-off speaker all look like. The current report throws this away
// (portOpen in logexport.go returns a bool) and the user is then told to hunt
// through antivirus settings for a speaker that was politely refusing every
// connection.
//
// Matched on the error text rather than on syscall constants because the
// constants differ per platform (ECONNREFUSED vs WSAECONNREFUSED) while the
// rendered text does not, and this string is read by a human, not switched on.
func classifyDialErr(err error, timeout time.Duration) string {
	if err == nil {
		return "open"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Sprintf("no answer (silent, timed out after %s)", timeout.Round(100*time.Millisecond))
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "refused"):
		return "connection refused (the speaker IS reachable, nothing is listening on this port)"
	case strings.Contains(msg, "no route to host"):
		return "no route to host (this PC has no path to that address)"
	case strings.Contains(msg, "network is unreachable"):
		return "network unreachable (this PC is not on that network at all)"
	case strings.Contains(msg, "host is down"), strings.Contains(msg, "host unreachable"):
		return "host unreachable (nothing answered at the network layer)"
	}
	return strings.TrimSpace(err.Error())
}

// probeReportPort dials one port and, when it opens, spends one more short
// request on finding out WHAT is listening.
func probeReportPort(host string, port int, label, path string, timeout time.Duration) portAnswer {
	ans := portAnswer{Port: port, Label: label}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	ans.Result = classifyDialErr(err, timeout)
	if err != nil {
		return ans
	}
	if path == "" {
		// SSH speaks first. The banner names the server, and on these speakers
		// that is a dropbear build old enough that its version is a diagnostic
		// in its own right.
		ans.Detail = readServerBanner(conn, timeout)
		_ = conn.Close()
		return ans
	}
	_ = conn.Close()
	ans.Detail = httpStatusLine(fmt.Sprintf("http://%s:%d%s", host, port, path), timeout)
	return ans
}

// bannerPrintable keeps a server greeting to the characters that survive being
// pasted into a mail. A banner is remote input, so it is truncated and
// stripped rather than trusted.
var bannerPrintable = regexp.MustCompile(`[^\x20-\x7e]`)

func readServerBanner(conn net.Conn, timeout time.Duration) string {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 96)
	n, err := conn.Read(buf)
	if n <= 0 || (err != nil && n == 0) {
		return ""
	}
	line := string(buf[:n])
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(bannerPrintable.ReplaceAllString(line, ""))
}

// httpStatusLine reports the status an open HTTP port answers with. The status
// alone settles the case the 2026-08-07 field report cost two days: a speaker
// that returns 400 or 404 is answering, so it is not a firewall.
func httpStatusLine(url string, timeout time.Duration) string {
	c := &http.Client{Timeout: timeout}
	resp, err := c.Get(url)
	if err != nil {
		return "opened, but no HTTP answer (" + shortErr(err) + ")"
	}
	defer resp.Body.Close()
	return "HTTP " + resp.Status
}

// shortErr trims Go's URL-prefixed transport errors down to the part a reader
// needs; the full form repeats the URL that is already on the line.
func shortErr(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 && i+2 < len(msg) {
		msg = msg[i+2:]
	}
	return strings.TrimSpace(msg)
}

// pingSaysAlive decides whether a ping run proves the host answered ICMP.
//
// The trap this exists for is Windows: `ping` exits 0 for
// "Reply from 192.168.1.1: Destination host unreachable", because the router
// did reply. Trusting the exit code alone therefore reports a missing speaker
// as alive, which is exactly the wrong direction for a report meant to place
// blame. Only an echo reply carries a TTL, and every localisation of every
// ping we target prints it as "TTL=" or "ttl=", so that token is the test and
// the exit code is only a second opinion.
func pingSaysAlive(exitOK bool, out string) bool {
	return exitOK && strings.Contains(strings.ToLower(out), "ttl=")
}

// pingHost runs one ICMP echo. Flags differ more than they look:
// Windows -w is a per-reply timeout in MILLISECONDS, Linux -W is in seconds,
// and macOS -W is in milliseconds while its -t is the overall deadline in
// seconds. Using the Linux form on macOS asks for a 1 ms reply and always
// reports the speaker dead.
func pingHost(ctx context.Context, host string) (ran, alive bool) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "ping", "-n", "1", "-w", "1500", host)
	case "darwin":
		cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-t", "2", host)
	default:
		cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", "2", host)
	}
	hideCmdWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		// Never started (not on PATH, blocked by policy). Say nothing rather
		// than turn a locked-down PC into a second failure.
		return false, false
	}
	return true, pingSaysAlive(err == nil, string(out))
}

// macAnywhere matches a MAC in either separator style. One or two hex digits
// per octet on purpose: macOS prints the short form (0:c:8a:...), and a
// six-octet-exact regex silently finds nothing there.
var macAnywhere = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{1,2}[:-]){5}[0-9a-f]{1,2}\b`)

// parseARPMAC pulls the hardware address the OS has cached for one IP out of
// the platform's ARP listing. The line must genuinely name that IP as a whole
// token, so a listing that also contains 192.168.1.340 or 192.168.1.3 cannot
// donate its MAC to 192.168.1.34.
func parseARPMAC(out, ip string) string {
	for _, line := range strings.Split(out, "\n") {
		if !lineMentionsIP(line, ip) {
			continue
		}
		if m := macAnywhere.FindString(line); m != "" {
			return m
		}
	}
	return ""
}

// lineMentionsIP splits on every separator the three listings use, including
// the parentheses macOS wraps the address in ("? (192.0.2.5) at 0:c:8a:...").
func lineMentionsIP(line, ip string) bool {
	fields := strings.FieldsFunc(line, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '(' || r == ')' || r == ',' || r == ';'
	})
	for _, f := range fields {
		if f == ip {
			return true
		}
	}
	return false
}

// macVendorPrefix keeps the manufacturer half of a MAC and blanks the rest.
//
// The vendor prefix is the useful half: it says whether the thing at that
// address is a Bose speaker at all, which is the question when an install
// lands on an IP the router has since handed to a printer. The device half is
// not shown, because a SoundTouch's MAC IS its Bose deviceID (see the marge
// registration work) and this report goes into a mail.
func macVendorPrefix(mac string) string {
	sep := ":"
	if strings.Contains(mac, "-") {
		sep = "-"
	}
	parts := strings.Split(mac, sep)
	if len(parts) != 6 {
		return ""
	}
	for i := 0; i < 3; i++ {
		if len(parts[i]) == 1 {
			parts[i] = "0" + parts[i]
		}
	}
	return strings.ToLower(strings.Join(parts[:3], sep)) + sep + "xx" + sep + "xx" + sep + "xx"
}

// arpLookup asks the OS what it has cached for one address. Run AFTER the port
// dials so the entry exists: on a cold cache the answer to "is anything at
// this IP" is only populated once something has tried to talk to it.
func arpLookup(ctx context.Context, host string) string {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "arp", "-a", host)
	case "darwin":
		cmd = exec.CommandContext(ctx, "arp", "-n", host)
	default:
		cmd = exec.CommandContext(ctx, "ip", "neigh", "show", host)
	}
	hideCmdWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	return parseARPMAC(string(out), host)
}

// localIPv4Ifaces lists this PC's own interfaces the way the report prints
// them. Loopback is dropped (it can never reach a speaker); an interface with
// no IPv4 is kept, because "the only interface that is up is a VPN tunnel" is
// a real cause that looks like a firewall from the outside.
func localIPv4Ifaces() []localIface {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []localIface
	for _, in := range ifs {
		if in.Flags&net.FlagLoopback != 0 {
			continue
		}
		up := in.Flags&net.FlagUp != 0
		addrs, aerr := in.Addrs()
		if aerr != nil {
			out = append(out, localIface{Name: in.Name, Up: up})
			continue
		}
		var v4 []string
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil || ipnet.IP.IsLinkLocalUnicast() {
				continue
			}
			v4 = append(v4, ipnet.String())
		}
		if len(v4) == 0 {
			if !up {
				// A down interface with no address is noise on a laptop that
				// enumerates a dozen virtual adapters.
				continue
			}
			out = append(out, localIface{Name: in.Name, Up: up})
			continue
		}
		for _, c := range v4 {
			out = append(out, localIface{Name: in.Name, CIDR: c, Up: up})
		}
	}
	return out
}

// sameSubnet answers the question the current advice only guesses at.
//
// "Make sure both are on the same Wi-Fi (not a guest network)" is printed on
// every unreachable speaker, and the app is holding the two addresses that
// settle it. Returns known=false only when there is nothing to decide with
// (an unparseable host, or no interface with an IPv4 address).
func sameSubnet(host string, ifaces []localIface) (known, same bool, via string) {
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil || ip.To4() == nil {
		return false, false, ""
	}
	any := false
	for _, in := range ifaces {
		if in.CIDR == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(in.CIDR)
		if err != nil || ipnet.IP.To4() == nil {
			continue
		}
		any = true
		if ipnet.Contains(ip) {
			return true, true, in.Name + " " + in.CIDR
		}
	}
	if !any {
		return false, false, ""
	}
	return true, false, ""
}

// lanSightings reads the discovery cache. No network: a discovery run here
// would cost seconds on a screen the user is already waiting on, and would
// race the install that just failed.
func (a *App) lanSightings(max int) []lanSighting {
	if a == nil {
		return nil
	}
	a.discMu.Lock()
	out := make([]lanSighting, 0, len(a.discCache))
	for _, e := range a.discCache {
		out = append(out, lanSighting{
			Host:    e.box.Host,
			Model:   e.box.Model,
			Version: e.box.Version,
			Build:   e.box.Build,
			Kind:    e.box.Kind,
			Offline: e.box.Offline,
		})
	}
	a.discMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// reportLogTailBytes caps how much of the app log the report may carry. The
// text has to stay something a person will paste into a mail; the full log
// travels in the diagnostic bundle the same screen now offers.
const reportLogTailBytes = 4 * 1024

// appLogTailFor returns the lines of the app log that are about this speaker,
// plus the last few lines whatever they are about, in file order.
//
// The host filter alone is not enough: the interesting line is often the one
// that names no host (a panic, a discovery cycle that found nothing), and the
// interesting host lines are often older than the tail. So both are kept and
// merged rather than choosing between them.
//
// Identities are scrubbed with scrubIdentities, which is scrubPII minus the IP
// masking: the report exists to show the user their own real addresses, so
// maskIP would blank the values it was written to display, but the Windows
// account name, the speaker MAC, its Bose deviceID and the friendly name have
// no business in a mailed text. redactSSIDs alone (the first cut of this) was
// not enough, and it sat two sections below the ARP line that goes out of its
// way to mask the very same hardware address.
func appLogTailFor(host string, limit int) string {
	f, err := os.Open(LogFilePath())
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	const window = 512 * 1024
	off := int64(0)
	if st.Size() > window {
		off = st.Size() - window
	}
	buf := make([]byte, st.Size()-off)
	// Take the byte count, not the buffer length: a log that is rotated or
	// truncated between the Stat and the read comes back short, and the unread
	// remainder of the buffer would otherwise reach the report as a run of NUL
	// bytes in the middle of a text the user is about to paste into a mail.
	// A short read is fine and is not an error worth reporting here: whatever
	// arrived is still the tail of the log.
	n, _ := f.ReadAt(buf, off)
	if n <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(buf[:n]), "\n"), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // the window almost certainly cut the first line in half
	}
	keep := map[int]bool{}
	hostHits := 0
	for i := len(lines) - 1; i >= 0 && hostHits < 40; i-- {
		if host != "" && strings.Contains(lines[i], host) {
			keep[i] = true
			hostHits++
		}
	}
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-30; i-- {
		keep[i] = true
	}
	idx := make([]int, 0, len(keep))
	for i := range keep {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	var out []string
	for _, i := range idx {
		out = append(out, lines[i])
	}
	tail := scrubIdentities(strings.Join(out, "\n"))
	if len(tail) > limit {
		tail = tail[len(tail)-limit:]
		if i := strings.IndexByte(tail, '\n'); i >= 0 {
			tail = tail[i+1:]
		}
	}
	return strings.TrimSpace(tail)
}

// gatherInstallFacts collects the whole set. Ports and ping run in parallel
// (the ports are the long pole and ping is free alongside them); the ARP
// lookup runs afterwards so the dials have populated the cache it reads.
func (a *App) gatherInstallFacts(ctx context.Context, host string) installFacts {
	f := installFacts{Host: host}
	if host == "" {
		return f
	}

	f.Ports = make([]portAnswer, len(reportProbePorts))
	var wg sync.WaitGroup
	for i, p := range reportProbePorts {
		wg.Add(1)
		go func(i int, port int, label, path string) {
			defer wg.Done()
			f.Ports[i] = probeReportPort(host, port, label, path, reportDialTimeout)
		}(i, p.port, p.label, p.path)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		pctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		f.PingRan, f.PingAlive = pingHost(pctx, host)
	}()
	wg.Wait()

	actx, cancel := context.WithTimeout(ctx, 3*time.Second)
	f.MACPrefix = macVendorPrefix(arpLookup(actx, host))
	cancel()

	f.Ifaces = localIPv4Ifaces()
	f.SubnetKnown, f.SameSubnet, f.SubnetVia = sameSubnet(host, f.Ifaces)
	f.Firewall = localFirewallState()
	f.LAN = a.lanSightings(12)
	f.LogTail = appLogTailFor(host, reportLogTailBytes)
	return f
}

// agentPortsProvenClosed reports whether the gathered facts already rule out
// both agent ports. When they do, the report skips its own /api/agent/version
// call: that call is worth up to two full HTTP timeouts against a speaker the
// dials have just shown to be silent, and the enriched report must not be
// slower than the thin one it replaces.
func (f installFacts) agentPortsProvenClosed() bool {
	seen := 0
	for _, p := range f.Ports {
		if p.Port != 8888 && p.Port != 17008 {
			continue
		}
		if p.Result == "open" {
			return false
		}
		seen++
	}
	return seen == 2
}
