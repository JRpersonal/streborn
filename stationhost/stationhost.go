// Package stationhost decides which of a radio station's hosts STR may look a
// logo up by. The speaker's agent (internal/webui stationart.go, the logo chain
// completed on every saved key) and the desktop app (ResolveStationLogo) both
// ask it, so the two never disagree. It lives outside internal/ because the
// desktop app is its own Go module and Go forbids importing another module's
// internal packages. The desktop frontend keeps a copy of the list in
// desktop-app/frontend/src/logos.js (SHARED_HOST_DOMAINS); a test here fails
// when the two drift apart.
//
// Why (#1773): a station without a logo of its own got the icon of whatever
// its hosts are under. "Radio Eure du Blues" streams from a *.ddns.net host and
// got ddns.net's favicon through DuckDuckGo, a logo that belongs to the
// dynamic-DNS provider and says nothing about the station. Under a shared or
// dynamic-DNS domain neither the host itself (DuckDuckGo falls back to the
// domain's icon for an unknown subdomain) nor its root domain is the
// station's, so such a host is never looked up. With nothing left the key
// keeps its letter tile.
package stationhost

import (
	"net"
	"strings"
)

// SharedDomains are domains under which anyone can get a host name: dynamic
// DNS providers and tunnel services. A host equal to one of them or under it
// has no logo of its own to find. Lower case, no leading dot.
var SharedDomains = []string{
	// No-IP
	"ddns.net", "no-ip.com", "no-ip.org", "no-ip.biz", "no-ip.info", "noip.me",
	"hopto.org", "zapto.org", "sytes.net", "myftp.org", "myftp.biz",
	"servemp3.com", "serveftp.com", "redirectme.net",
	// Dyn (the dyndns.* and dnsalias.* families are in SharedApexLabels)
	"homeip.net",
	// Others
	"duckdns.org", "freeddns.org", "myddns.me", "dynu.net", "ddnsfree.com",
	"mooo.com", "afraid.org", "chickenkiller.com",
	// Tunnels
	"ngrok.io", "trycloudflare.com",
}

// SharedApexLabels are provider names that come under many top-level domains
// (dyndns.org, dyndns.info, dyndns.tv, dnsalias.com, dnsalias.net, ...): a
// host whose registered domain is "<label>.<tld>" counts as shared.
var SharedApexLabels = []string{"dyndns", "dnsalias"}

// Shared reports whether host is an IP literal or sits under a shared or
// dynamic-DNS domain (SharedDomains, SharedApexLabels), so that neither it nor
// its root domain may be used to look up a station logo. host may carry a
// port, brackets or a trailing dot; it is compared case-insensitively.
func Shared(host string) bool {
	h := normalize(host)
	if h == "" {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	for _, d := range SharedDomains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	labels := strings.Split(h, ".")
	if n := len(labels); n >= 2 {
		apex := labels[n-2]
		for _, l := range SharedApexLabels {
			if apex == l {
				return true
			}
		}
	}
	return false
}

// normalize lower-cases host and strips a port, IPv6 brackets and a trailing
// dot.
func normalize(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	return strings.TrimSuffix(h, ".")
}
