package webui

import (
	"net"
	"net/url"
	"strings"

	"github.com/JRpersonal/streborn/stationhost"
)

// Station logo chains on saved keys (#1467).
//
// The desktop app saves a radio key with its whole logo chain (logos.js
// stationLogoCandidates): the station's own https favicon first, then the
// DuckDuckGo icon of every station host and its root domain, then an http-only
// favicon last. The phone page saved the one radio-browser favicon it had, often
// an http URL or nothing at all, so the same station held from the phone ended
// up with no artwork where the app's save had a logo. Completing the chain here,
// where every client's save passes, gives a key the same art whichever client
// saved it.

// ddgIconPrefix is the DuckDuckGo icon service the desktop chain uses, the only
// external favicon service STR asks (logos.js iconServicesFor).
const ddgIconPrefix = "https://icons.duckduckgo.com/ip3/"

// completeStationArt returns art as the desktop app's stationLogoCandidates
// would have built it for a station with this homepage and stream: https
// candidates first, then the DuckDuckGo icon of each public station host (host,
// then root domain, homepage before stream), then everything else in its
// original order. A chain that already carries a DuckDuckGo entry was built by
// a client that knows the rule and comes back as it was, as does a station
// with no public host to look up.
//
// A DuckDuckGo entry for a shared or dynamic-DNS host (stationhost.Shared) is
// dropped from the chain first, whoever built it: an older app saved one, and
// it shows the provider's icon instead of the station's (#1773).
func completeStationArt(art, homepage, stream string) string {
	var cands []string
	dropped := false
	for _, c := range strings.Split(art, "|") {
		c = strings.TrimSpace(c)
		if ddgSharedHostIcon(c) {
			dropped = true
			continue
		}
		if c != "" && !containsString(cands, c) {
			cands = append(cands, c)
		}
	}
	if dropped {
		art = strings.Join(cands, "|")
	}
	for _, c := range cands {
		if strings.HasPrefix(c, ddgIconPrefix) {
			return art
		}
	}
	hosts := stationLogoHosts(homepage, stream)
	if len(hosts) == 0 {
		return art
	}
	out := make([]string, 0, len(cands)+len(hosts))
	push := func(c string) {
		if !containsString(out, c) {
			out = append(out, c)
		}
	}
	for _, c := range cands {
		if strings.HasPrefix(strings.ToLower(c), "https://") {
			push(c)
		}
	}
	for _, h := range hosts {
		push(ddgIconPrefix + h + ".ico")
	}
	for _, c := range cands {
		push(c)
	}
	return strings.Join(out, "|")
}

// stationLogoHosts lists the hosts a station logo is looked up by: each URL's
// host, then its root domain (logos.js extractHost + rootDomain). A LAN host
// (an IP literal, a single label, .local and the other home suffixes) is left
// out: it has no public icon, and asking a third party about it would hand
// over a private address for nothing. So is a host under a shared or
// dynamic-DNS domain, with its root domain (stationhost.Shared, #1773): its
// icon is the provider's, never the station's.
func stationLogoHosts(urls ...string) []string {
	var hosts []string
	push := func(h string) {
		if h != "" && !containsString(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	for _, raw := range urls {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		h := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
		if !publicLogoHost(h) || stationhost.Shared(h) {
			continue
		}
		push(h)
		push(logoRootDomain(h))
	}
	return hosts
}

// ddgSharedHostIcon reports whether u is a DuckDuckGo icon URL for a host
// stationhost.Shared rules out. The /art proxy refuses those as well, so a
// chain saved before the rule (or the phone page's own candidates) cannot
// bring the provider's icon back.
func ddgSharedHostIcon(u string) bool {
	if !strings.HasPrefix(u, ddgIconPrefix) {
		return false
	}
	host := strings.TrimSuffix(strings.TrimPrefix(u, ddgIconPrefix), ".ico")
	return stationhost.Shared(host)
}

// publicLogoHost reports whether h names a public host worth an icon lookup.
func publicLogoHost(h string) bool {
	if h == "" || net.ParseIP(h) != nil || !strings.Contains(h, ".") {
		return false
	}
	for _, sfx := range []string{".local", ".lan", ".home", ".internal", ".localdomain", ".home.arpa", ".fritz.box"} {
		if strings.HasSuffix(h, sfx) {
			return false
		}
	}
	return true
}

// logoRootDomain strips the first label of a host with three or more labels
// ("stream.example.de" becomes "example.de"), the desktop's rootDomain.
func logoRootDomain(h string) string {
	parts := strings.Split(h, ".")
	if len(parts) < 3 {
		return h
	}
	return strings.Join(parts[1:], ".")
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
