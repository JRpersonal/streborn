package webui

import (
	"net/http"
	"syscall"
	"testing"
)

// The art proxy takes its target URL from a query parameter, so anything that
// can reach the agent can ask the SPEAKER to fetch an address of its choosing.
// The speaker sits inside the user's network and can reach what the caller
// cannot, including its own loopback, where the Bose firmware answers on :8090
// with endpoints that act on a plain GET. CodeQL flagged this as
// go/request-forgery on the day the file was written.
//
// The check lives in the dialer, so it sees the address actually connected to.
// That is what makes it hold against a hostname resolving to 127.0.0.1, a
// redirect back into the network, and the IPv6 spellings of the same address.
func TestArtProxyRefusesNonPublicAddresses(t *testing.T) {
	blocked := []struct{ name, addr string }{
		{"loopback", "127.0.0.1:8090"},
		{"loopback, another address in the range", "127.99.1.2:80"},
		{"the box's own Bose API", "127.0.0.1:8090"},
		{"IPv6 loopback", "[::1]:80"},
		{"IPv4-mapped loopback", "[::ffff:127.0.0.1]:80"},
		{"private 10/8", "10.0.0.5:80"},
		{"private 172.16/12", "172.16.4.9:80"},
		{"private 192.168/16", "192.168.178.44:8090"},
		{"IPv4-mapped private", "[::ffff:192.168.178.44]:8090"},
		{"link-local, cloud metadata lives here", "169.254.169.254:80"},
		{"IPv6 link-local", "[fe80::1]:80"},
		{"unique local IPv6", "[fc00::1]:80"},
		{"carrier-grade NAT", "100.64.0.1:80"},
		{"unspecified", "0.0.0.0:80"},
		{"multicast", "224.0.0.1:80"},
	}
	for _, tc := range blocked {
		t.Run(tc.name, func(t *testing.T) {
			if err := publicOnlyControl("tcp", tc.addr, rawConn(nil)); err == nil {
				t.Errorf("%s was allowed; the speaker must not be usable to reach it", tc.addr)
			}
		})
	}

	allowed := []struct{ name, addr string }{
		{"public IPv4", "93.184.216.34:443"},
		{"public IPv6", "[2606:2800:220:1:248:1893:25c8:1946]:443"},
		{"just outside the carrier-grade NAT range", "100.128.0.1:80"},
		{"just below the carrier-grade NAT range", "100.63.255.255:80"},
	}
	for _, tc := range allowed {
		t.Run(tc.name, func(t *testing.T) {
			if err := publicOnlyControl("tcp", tc.addr, rawConn(nil)); err != nil {
				t.Errorf("%s was refused, but station artwork lives on public addresses: %v", tc.addr, err)
			}
		})
	}

	t.Run("an address that did not resolve is refused, not passed through", func(t *testing.T) {
		if err := publicOnlyControl("tcp", "not-an-address", rawConn(nil)); err == nil {
			t.Error("an unparseable address was allowed")
		}
		if err := publicOnlyControl("tcp", "example.com:80", rawConn(nil)); err == nil {
			t.Error("a hostname reached the dialer unresolved and was allowed")
		}
	})
}

// The client must not be the shared default one, or the address check is not
// in the path at all.
func TestArtProxyUsesTheGuardedClient(t *testing.T) {
	if artFetchClient == nil {
		t.Fatal("no dedicated client, so the fetch would use http.DefaultClient and skip the address check")
	}
	tr, ok := artFetchClient.Transport.(*http.Transport)
	if !ok || tr.DialContext == nil {
		t.Fatal("the client has no custom dialer, so the address check never runs")
	}
	if artFetchClient.CheckRedirect == nil {
		t.Fatal("redirects are unbounded; a redirect could walk the fetch back into the network")
	}
	if err := artFetchClient.CheckRedirect(nil, make([]*http.Request, 4)); err == nil {
		t.Error("a fourth redirect was allowed")
	}
	if err := artFetchClient.CheckRedirect(nil, make([]*http.Request, 1)); err != nil {
		t.Errorf("a first redirect was refused: %v", err)
	}
}

// rawConn keeps the test readable: Control never touches it.
func rawConn(c syscall.RawConn) syscall.RawConn { return c }
