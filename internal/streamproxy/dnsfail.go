// dnsfail.go: telling "the speaker could not look up the station's address"
// apart from every other way a station fails.
//
// Support evidence (mail plus #1084 #1043 #823): routers whose name server
// answers SERVFAIL or times out, and Pi-hole / AdGuard blocking a station host.
// The owners saw "radio stops after seconds", "one station is silent" or a
// clock stuck in 2015, and the app said "this stream could not be reached",
// which sends them to the station. A single blocked host (an ad-blocker list
// that carries one station's CDN) never trips the speaker's internet verdict
// either, because every other host resolves fine, so nothing named the real
// place to look: the router's name resolution or the ad-blocker.
//
// A name that does not exist (NXDOMAIN) is kept apart: far more often than a
// blocker, it is a station whose domain expired (radio-browser carries many),
// and internet.Classify reads it the same way. Pi-hole and AdGuard Home
// answer a blocked host with 0.0.0.0 by default, which is dnsBlockerAnswer.
// So NXDOMAIN reaches the app as "nohost", whose text names the dead station
// first and the ad-blocker only second.
//
// This only classifies. It adds no resolver of its own and no retry: whether
// the agent should fall back to a public resolver is an open decision.

package streamproxy

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/JRpersonal/streborn/internal/netutil"
)

// The stream-status reasons of a lookup failure (dnsReason).
const (
	// reasonDNS: the lookup failed or a blocker answered for the host.
	reasonDNS = "dns"
	// reasonNoHost: the station's host name does not exist (NXDOMAIN).
	reasonNoHost = "nohost"
)

// The kinds of a station-host lookup failure, for the log. The app gets the
// coarser reason (dnsReason); the kind tells a diagnostic reader which of the
// field cases it is.
const (
	// dnsNotFound: the name server says the name does not exist (NXDOMAIN).
	// Usually the station's host is gone; rarely an ad-blocker in NXDOMAIN
	// mode answers for it.
	dnsNotFound = "not-found"
	// dnsServerFailure: the name server answered with an error (SERVFAIL,
	// REFUSED: Go's "server misbehaving").
	dnsServerFailure = "server-failure"
	// dnsTimeout: the name server did not answer in time.
	dnsTimeout = "timeout"
	// dnsBlockerAnswer: the name resolved to 0.0.0.0, :: or a loopback
	// address, which is how Pi-hole and AdGuard answer for a blocked host by
	// default. The SSRF guard refuses that address before any connect.
	dnsBlockerAnswer = "blocker-answer"
	// dnsOther: some other lookup error (no name server configured, a
	// malformed answer).
	dnsOther = "other"
)

// stationDNSFailure reports whether err is a failure to look up the station's
// host name, and which kind. rawURL is the station URL the error belongs to: a
// URL whose host is an IP literal involves no lookup, so a guard refusal of
// such a URL is never read as an ad-blocker answer.
func stationDNSFailure(rawURL string, err error) (kind string, ok bool) {
	if err == nil {
		return "", false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		switch {
		case dnsErr.IsNotFound:
			return dnsNotFound, true
		case dnsErr.IsTimeout:
			return dnsTimeout, true
		case strings.Contains(strings.ToLower(dnsErr.Err), "misbehaving"),
			strings.Contains(strings.ToLower(dnsErr.Err), "refused"):
			return dnsServerFailure, true
		}
		return dnsOther, true
	}
	var blocked *netutil.BlockedTargetError
	if errors.As(err, &blocked) {
		if blocked.IP != nil && (blocked.IP.IsUnspecified() || blocked.IP.IsLoopback()) && hostIsName(rawURL) {
			return dnsBlockerAnswer, true
		}
		return "", false
	}
	// Text fallback for an error that lost its type on the way (a wrapper
	// that formats with %v). Only phrases the Go resolver writes.
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "no such host"):
		return dnsNotFound, true
	case strings.Contains(msg, "server misbehaving"):
		return dnsServerFailure, true
	case strings.Contains(msg, "lookup ") && (strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "timed out")):
		return dnsTimeout, true
	}
	return "", false
}

// dnsReason maps a lookup failure kind to the stream-status reason: a name
// that does not exist is "nohost" (most often a dead station), every other
// kind is "dns" (the router's name server or an ad-blocker).
func dnsReason(kind string) string {
	if kind == dnsNotFound {
		return reasonNoHost
	}
	return reasonDNS
}

// logDNSFailure writes one WARN line naming the lookup failure and its kind,
// at most once per failLogSuppressWindow and station URL (the speaker's player
// retries a station that does not resolve several times a minute).
func (s *Server) logDNSFailure(rawURL string, err error) {
	kind, _ := stationDNSFailure(rawURL, err)
	if !s.shouldLogFail("dns|" + rawURL) {
		return
	}
	s.logger.Warn("stream proxy: the speaker could not look up the station's address (router DNS or an ad-blocker?)",
		"host", stationHost(rawURL), "kind", kind, "url", rawURL, "err", err)
}

// hostIsName reports whether rawURL names its host by a DNS name, not an IP
// literal and not "localhost" (which resolves to loopback by design, not
// because a blocker said so).
func hostIsName(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return false
	}
	return net.ParseIP(h) == nil
}

// stationHost is the host part of a station URL for the log, empty when the
// URL does not parse.
func stationHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
