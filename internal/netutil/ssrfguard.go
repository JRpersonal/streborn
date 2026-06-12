package netutil

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// SafeHTTPURL accepts a URL only if its scheme is http or https. It is the
// project's belt-and-braces filter at every outbound HTTP call site that takes
// a URL from a not-strictly-trusted source (the preset store, radio-browser
// search results, playlist auto-discovery). A single rogue value with file://,
// ftp:// or jar:// would otherwise reach Go's stdlib HTTP client and become an
// SSRF vector. Host-level protection runs at dial time (DialGuardSSRF); this
// only gates the scheme.
func SafeHTTPURL(raw string) error {
	if raw == "" {
		return errors.New("url is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("disallowed url scheme %q (only http/https accepted)", u.Scheme)
	}
}

// DialGuardSSRF runs as the net.Dialer Control hook, i.e. AFTER DNS resolution
// and BEFORE the TCP connect, on the concrete resolved address. It refuses
// connections to addresses that are never a legitimate public stream but that
// an attacker-controlled upstream URL could abuse to make the agent fetch its
// own privileged loopback services (the Bose firmware / STR webui on the box)
// or a cloud metadata endpoint (169.254.169.254). Because it inspects the
// resolved IP, a hostname that resolves to a blocked address (DNS-rebinding) is
// caught too.
//
// Private LAN ranges (192.168/10/172.16) are deliberately NOT blocked so a
// user's own local Icecast/DLNA stream keeps working; the dangerous
// self-targeting case is loopback, which is blocked.
func DialGuardSSRF(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil // not an IP literal (should not happen post-resolution)
	}
	if ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("stream target %s blocked (loopback/link-local/metadata)", ip)
	}
	return nil
}

// GuardedClient returns an *http.Client whose dialer refuses loopback /
// link-local / metadata targets via DialGuardSSRF, for fetching
// not-strictly-trusted upstream URLs (radio playlists, stream reachability
// probes). Pair it with SafeHTTPURL to gate the scheme before dialing.
func GuardedClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: timeout, Control: DialGuardSSRF}).DialContext,
		},
	}
}
