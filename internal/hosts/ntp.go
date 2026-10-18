package hosts

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os"
	"strings"
)

// The speaker's display clock in standby only shows once the firmware has
// fetched the time over NTP itself (APServer's NTPController, servers listed
// in /etc/ntpservers.txt). A speaker that came up before its Wi-Fi was ready
// never managed that again: APServer logged "Unable to resolve NTP Server" for
// every name, all day, while STR's own lookups on the same box worked and the
// radio played. Its resolver had kept the empty configuration it started with.
// The display controller then went to StandbyNoClock and the clock was gone
// after an update reboot (field report, ST30, 2026-10-18).
//
// /etc/hosts, unlike the DNS configuration, is read again on every lookup. So
// the agent looks the NTP names up itself and writes the answers into its own
// hosts block, and the firmware's clock sync works however its start went.

// NTPServersPath is the firmware's own list of time servers.
const NTPServersPath = "/etc/ntpservers.txt"

// NTPServers returns the host names in the firmware's NTP server list, in
// order, without comments, blanks or duplicates. A missing file gives none.
func NTPServers(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseNTPServers(b)
}

func parseNTPServers(b []byte) []string {
	var out []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		host := strings.ToLower(f[0])
		// an address needs no lookup, and a bare word is no public server
		if net.ParseIP(host) != nil || !strings.Contains(host, ".") || seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	return out
}

// LookupFunc resolves a host name to its addresses (net.Resolver.LookupHost).
type LookupFunc func(ctx context.Context, host string) ([]string, error)

// NTPEntries looks every name up and returns one hosts entry per name that
// resolved to an IPv4 address (the firmware's NTP client is IPv4 only). Names
// that fail are left out, so they keep going to DNS as before.
func NTPEntries(ctx context.Context, names []string, lookup LookupFunc) []Entry {
	var out []Entry
	for _, name := range names {
		addrs, err := lookup(ctx, name)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ip := net.ParseIP(a); ip != nil && ip.To4() != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
				out = append(out, Entry{IP: ip.String(), Host: name})
				break
			}
		}
	}
	return out
}
