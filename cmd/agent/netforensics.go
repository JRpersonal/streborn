package main

// Network reachability forensics for the "plays radio but unreachable" hunt.
//
// A box in that state keeps its outbound radio stream (an ESTABLISHED
// connection) while every inbound packet is dropped, so it is invisible over the
// network exactly when we most want to look at it (confirmed on a mojo ST30 and
// reported on an ST10; see project_st30_blackhole_usb_stick). The box cannot
// test its own EXTERNAL inbound reachability from the inside, that needs an
// outside prober, but it CAN watch the two things that fail underneath it:
//
//   1. the LAN interface losing carrier while it stays administratively up (the
//      USB-bridged eth0 coprocessor degrading on scm/BCO chassis), and
//   2. the default gateway falling out of the ARP table (an L2 breakdown that
//      would strand both chassis, wlan0 and the USB bridge alike).
//
// Both are read from /proc and the net package, cost nothing, and ride the
// existing 5-minute resource-health tick, so there is NO new timer and NO
// per-tick exec (keeping the box hardware spared). A degradation is logged at
// INFO so it lands in the 32 KB NAND ring that survives the reboot the user does
// to recover; the full picture (the INPUT chain, routes, the whole ARP table,
// the listeners) is exposed only on demand as the net_reachability debug
// section, where the exec cost is paid once per diagnostic bundle, not on a tick.

import (
	"bufio"
	"encoding/binary"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

type netForensicsSnapshot struct {
	IfaceName    string   `json:"ifaceName"`
	IfaceUp      bool     `json:"ifaceUp"`      // administratively up
	IfaceCarrier bool     `json:"ifaceCarrier"` // IFF_RUNNING: the link is actually up
	HasLANIP     bool     `json:"hasLANIP"`
	LANAddrs     []string `json:"lanAddrs"`
	GatewayIP    string   `json:"gatewayIP"`
	GatewayARP   string   `json:"gatewayARP"` // reachable | incomplete | absent | unknown
}

var (
	netForensicsMu   sync.Mutex
	netForensicsLast netForensicsSnapshot
	netForensicsHave bool
)

// defaultRouteV4 reads /proc/net/route and returns the IPv4 default route's
// (gatewayIP, ifaceName), or ("","") if there is none. The gateway is stored
// little-endian hex in the file.
func defaultRouteV4() (string, string) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || fields[1] != "00000000" { // Destination 0 = default
			continue
		}
		v, err := strconv.ParseUint(fields[2], 16, 32)
		if err != nil {
			continue
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(v))
		return net.IP(b[:]).String(), fields[0]
	}
	return "", ""
}

// gatewayARPState reads /proc/net/arp and reports the gateway's entry state:
// "reachable" (flags 0x2 = ATF_COM, a completed MAC), "incomplete", "absent"
// (no entry at all), or "unknown" (no gateway / file unreadable).
func gatewayARPState(gw string) string {
	if gw == "" {
		return "unknown"
	}
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || fields[0] != gw {
			continue
		}
		if fields[2] == "0x2" {
			return "reachable"
		}
		return "incomplete"
	}
	return "absent"
}

// isRFC1918 reports whether s is a private-range IPv4, so the fallback interface
// pick skips the usb0 203.0.113.x internal link and any link-local address.
func isRFC1918(s string) bool {
	ip := net.ParseIP(s).To4()
	if ip == nil {
		return false
	}
	switch {
	case ip[0] == 10:
		return true
	case ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31:
		return true
	case ip[0] == 192 && ip[1] == 168:
		return true
	}
	return false
}

// readNetForensics builds the cheap snapshot from /proc + the net package. No
// exec, so it is safe to call on every health tick.
func readNetForensics() netForensicsSnapshot {
	var s netForensicsSnapshot
	gwIP, gwIface := defaultRouteV4()
	s.GatewayIP = gwIP
	s.GatewayARP = gatewayARPState(gwIP)

	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		var v4s []string
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip := ipn.IP.To4(); ip != nil {
					v4s = append(v4s, ip.String())
				}
			}
		}
		// Prefer the interface that owns the default route (the real LAN path);
		// otherwise the first non-loopback carrying a private-range address, so
		// the usb0 203.0.113.x internal bridge link is never mistaken for the LAN.
		isGwIface := gwIface != "" && iface.Name == gwIface
		fallback := s.IfaceName == "" && anyRFC1918(v4s)
		if isGwIface || fallback {
			s.IfaceName = iface.Name
			s.IfaceUp = iface.Flags&net.FlagUp != 0
			s.IfaceCarrier = iface.Flags&net.FlagRunning != 0
			s.LANAddrs = v4s
			s.HasLANIP = anyRFC1918(v4s)
			if isGwIface {
				break // exact match wins over the fallback
			}
		}
	}
	return s
}

func anyRFC1918(v4s []string) bool {
	for _, s := range v4s {
		if isRFC1918(s) {
			return true
		}
	}
	return false
}

// netDegradations returns the human-readable list of ways cur is worse than
// last for inbound reachability. Pure, so the decision is unit-testable without
// touching /proc.
func netDegradations(last, cur netForensicsSnapshot) []string {
	var d []string
	if last.IfaceCarrier && !cur.IfaceCarrier {
		d = append(d, "interface lost carrier (link down while the box stays up)")
	}
	if last.HasLANIP && !cur.HasLANIP {
		d = append(d, "LAN IP disappeared")
	}
	if last.GatewayARP == "reachable" && cur.GatewayARP != "reachable" {
		d = append(d, "default gateway fell out of the ARP table ("+cur.GatewayARP+")")
	}
	return d
}

// netRecovered reports whether cur is healthy again after last was degraded, so
// a bundle shows the box came back on its own instead of needing the reboot.
func netRecovered(last, cur netForensicsSnapshot) bool {
	return (!last.IfaceCarrier && cur.IfaceCarrier) ||
		(!last.HasLANIP && cur.HasLANIP) ||
		(last.GatewayARP != "reachable" && cur.GatewayARP == "reachable")
}

// checkNetworkReachability rides the resource-health tick. It logs the first
// reading and any degradation or recovery at INFO so the evidence lands in the
// NAND log that survives the recovery reboot; a quiet reading costs nothing
// (Debug only, no NAND write), matching the resource-health philosophy.
func checkNetworkReachability(logger *slog.Logger) {
	cur := readNetForensics()
	netForensicsMu.Lock()
	last, have := netForensicsLast, netForensicsHave
	netForensicsLast, netForensicsHave = cur, true
	netForensicsMu.Unlock()

	attrs := []any{
		"iface", cur.IfaceName, "up", cur.IfaceUp, "carrier", cur.IfaceCarrier,
		"lanAddrs", strings.Join(cur.LANAddrs, ","),
		"gateway", cur.GatewayIP, "gatewayARP", cur.GatewayARP,
	}
	if !have {
		logger.Info("network forensics: baseline", attrs...)
		return
	}
	if deg := netDegradations(last, cur); len(deg) > 0 {
		logger.Info("network forensics: reachability DEGRADED - "+strings.Join(deg, "; ")+
			" (the 'plays but unreachable' signature; kept in the NAND log across the reboot)",
			append(attrs, "prevCarrier", last.IfaceCarrier, "prevHasLANIP", last.HasLANIP,
				"prevGatewayARP", last.GatewayARP)...)
		return
	}
	if netRecovered(last, cur) {
		logger.Info("network forensics: reachability recovered on its own", attrs...)
		return
	}
	logger.Debug("network forensics", attrs...)
}

// netReachabilityDebugSection is the on-demand full picture for a diagnostic
// bundle: the cheap snapshot plus the state that needs a command. Exec is fine
// here because it runs only when someone fetches /api/debug/state, never on the
// tick.
func netReachabilityDebugSection() any {
	run := func(name string, args ...string) string {
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil && len(out) == 0 {
			return "(" + name + ": " + err.Error() + ")"
		}
		return strings.TrimRight(string(out), "\n")
	}
	readFile := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			return "(" + err.Error() + ")"
		}
		return strings.TrimRight(string(b), "\n")
	}
	listeners := run("netstat", "-ltn")
	if strings.HasPrefix(listeners, "(") {
		listeners = run("ss", "-ltn")
	}
	return map[string]any{
		"snapshot":                snapshotOrEmpty(),
		"iptables_input":          run("iptables", "-w", "-S", "INPUT"),
		"iptables_nat_prerouting": run("iptables", "-w", "-t", "nat", "-S", "PREROUTING"),
		"ip_route":                run("ip", "route"),
		"arp_table":               readFile("/proc/net/arp"),
		"listeners":               listeners,
	}
}

func snapshotOrEmpty() netForensicsSnapshot {
	return readNetForensics()
}
