package mdnshost

import (
	"os"
	"strconv"
	"strings"
)

// udpCounters reads the kernel's UDP totals and the drops on every socket bound
// to the mDNS port, for the diagnostic bundle. A report on #841 traced a frozen
// standby clock and stalling radio to a device on the network flooding mDNS,
// and the evidence that proved it (datagrams per second, drops per socket) had
// to be collected by hand over SSH. With these in the bundle the same case
// shows up in the first file. nil off Linux or when /proc is unreadable.
func udpCounters() map[string]any {
	out := map[string]any{}
	if b, err := os.ReadFile("/proc/net/snmp"); err == nil {
		for k, v := range parseSNMPUdp(string(b)) {
			out[k] = v
		}
	}
	if b, err := os.ReadFile("/proc/net/udp"); err == nil {
		out["mdnsSocketDrops"] = mdnsSocketDrops(string(b))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseSNMPUdp picks the Udp header and value lines out of /proc/net/snmp and
// returns the counters that tell a flood apart from a quiet network.
func parseSNMPUdp(s string) map[string]int64 {
	var names []string
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "Udp:" {
			continue
		}
		if names == nil {
			names = f[1:]
			continue
		}
		want := map[string]bool{"InDatagrams": true, "InErrors": true, "RcvbufErrors": true, "NoPorts": true}
		out := map[string]int64{}
		for i, v := range f[1:] {
			if i < len(names) && want[names[i]] {
				n, err := strconv.ParseInt(v, 10, 64)
				if err == nil {
					out["udp"+names[i]] = n
				}
			}
		}
		return out
	}
	return nil
}

// mdnsSocketDrops lists the drop counter of each socket bound to port 5353
// (hex 14E9) in /proc/net/udp. Several programs listen there on a speaker, the
// firmware's avahi and STR among them, and each one drops on its own.
func mdnsSocketDrops(s string) []int64 {
	var drops []int64
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 13 || !strings.HasSuffix(f[1], ":14E9") {
			continue
		}
		n, err := strconv.ParseInt(f[len(f)-1], 10, 64)
		if err == nil {
			drops = append(drops, n)
		}
	}
	return drops
}
