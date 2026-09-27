package boxapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A speaker address that is not an address.
//
// Several callers take this host from a request body on the agent's
// unauthenticated LAN port: a zone member list, for one. While the URL was built
// by interpolation, such a host could carry its own port, path and query, and
// this client POSTs as well as GETs. A member address of
// "192.0.2.9:80/removeGroup?x=" produced a request to /removeGroup on port 80 of
// a host of the caller's choosing, which is a write primitive aimed at whatever
// the speaker can reach. Verified by running it on 2026-09-26, then closed here.
func TestAHostCannotCarryItsOwnPathOrPort(t *testing.T) {
	escapes := []string{
		"192.0.2.9:80/removeGroup?x=",
		"192.0.2.9/removeGroup",
		"192.0.2.9:22",
		"evil.example/#",
		"user@192.0.2.9",
		"192.0.2.9 /x",
		"",
	}
	for _, host := range escapes {
		if got := New(host).url("/now_playing"); got != "" {
			t.Errorf("host %q built the URL %q; a host that is not a bare address must build nothing", host, got)
		}
	}

	// And the ordinary cases still work, or the fix would just be an outage.
	for _, host := range []string{"192.168.178.31", "10.0.0.5", "fe80::1", "speaker-lan"} {
		got := New(host).url("/now_playing")
		if got == "" || !strings.HasSuffix(got, ":8090/now_playing") {
			t.Errorf("host %q built %q, want a normal speaker URL", host, got)
		}
	}
}

// The refusal has to reach the caller as a failed request, not as a request to
// somewhere else.
func TestARefusedHostNeverDials(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	target := strings.TrimPrefix(srv.URL, "http://") + "/removeGroup?x="
	if _, err := New(target).GetInfo(context.Background()); err == nil {
		t.Error("a smuggled host produced a successful call")
	}
	if hit {
		t.Error("the speaker dialled the smuggled target")
	}
}
