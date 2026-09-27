package webui

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/netutil"
)

// The agent runs as root on a speaker, on an unauthenticated LAN port, and these
// reads take a host that arrived from outside: a zone member, a group the app
// formed. CodeQL called it go/request-forgery, critical, and it was right in the
// same way it was right about the artwork proxy on 2026-08-04.
//
// The fix is the dialer, not the string, so this test asks the only question
// that matters: does a loopback target actually get refused, and does a normal
// speaker address still work.
func TestPeerReadsRefuseLoopbackAndAllowTheLAN(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<nowPlaying source="STANDBY"/>`))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	// httptest listens on loopback, which is exactly what a peer read must not
	// reach: the speaker's own firmware answers there, and some of its :8090
	// endpoints ACT on a plain GET.
	if _, err := boxGetPeer(context.Background(), "http://"+host+"/now_playing", 1<<10); err == nil {
		t.Fatal("a peer read reached a loopback address; the guard is not in the dialer")
	}

	// The same read for the speaker this agent runs on is a different thing and
	// must keep working, or the agent cannot read its own firmware.
	if _, err := boxGet(context.Background(), "http://"+host+"/now_playing", 1<<10); err != nil {
		t.Fatalf("the agent can no longer read its own box: %v", err)
	}
}

// A LAN address is not blocked: the other speakers live there, and blocking
// 192.168/10/172.16 would turn multiroom off in the name of security.
func TestPeerReadsAllowPrivateAddresses(t *testing.T) {
	for _, ip := range []string{"192.168.178.31", "10.0.0.5", "172.16.4.9"} {
		if err := dialGuardFor(ip); err != nil {
			t.Errorf("a private LAN speaker at %s was refused: %v", ip, err)
		}
	}
	for _, ip := range []string{"127.0.0.1", "169.254.169.254", "0.0.0.0"} {
		if err := dialGuardFor(ip); err == nil {
			t.Errorf("%s was allowed; loopback, link-local and metadata must be refused", ip)
		}
	}
}

// dialGuardFor exercises the guard the peer client installs, without needing a
// listener on each address.
func dialGuardFor(ip string) error {
	return netutil.DialGuardSSRF("tcp", net.JoinHostPort(ip, "8090"), nil)
}

// The dial guard answers "which address" and cannot answer "was this address
// smuggled". A member address of "192.0.2.9:80/removeGroup?x=" concatenated into
// the URL parses as host 192.0.2.9:80 and path /removeGroup, which the guard is
// perfectly happy with: it is an ordinary LAN address. Verified by running it on
// 2026-09-26, on :8090 endpoints that ACT on a plain GET.
func TestAPeerAddressCannotCarryItsOwnPathOrPort(t *testing.T) {
	escapes := []string{
		"192.168.178.9:80/removeGroup?x=",
		"192.168.178.9/removeGroup",
		"192.168.178.9:22",
		"speaker.local", // a name: it can resolve elsewhere later
		"127.0.0.1",     // our own services
		"8.8.8.8",       // not on this LAN at all
		"",
	}
	for _, host := range escapes {
		if got := peerBoxURL(host, "/now_playing"); got != "" {
			t.Errorf("peer %q built %q; only a bare LAN address may build a URL", host, got)
		}
		if snap := fetchNowPlayingPeer(context.Background(), host); snap.Source != "" {
			t.Errorf("peer %q produced a reading", host)
		}
	}

	for _, host := range []string{"192.168.178.31", "10.0.0.5", "172.16.4.9"} {
		if got := peerBoxURL(host, "/now_playing"); got == "" {
			t.Errorf("a real speaker at %s was refused", host)
		}
	}
}
