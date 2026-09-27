package streamproxy

import (
	"errors"
	"fmt"
	"net"
	"testing"
)

// A station that never delivers a byte used to leave nothing in the log but
// "bose disconnected". Two diagnostics nine hours apart could not say whether
// the name lookup, the connection or the TLS handshake was the part that hung,
// which made the report unanswerable (#960).
func TestFetchPhaseNamesWhereItHung(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&net.DNSError{Err: "no such host", Name: "live.example"}, "dns"},
		{errors.New("dial tcp 192.0.2.9:443: connect: connection refused"), "connect"},
		{errors.New("tls: handshake failure"), "tls"},
		{errors.New("remote error: tls: certificate required"), "tls"},
		{errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)"), "waiting for the first header"},
		{fmt.Errorf("wrapped: %w", &net.DNSError{Err: "server misbehaving"}), "dns"},
		{errors.New("something nobody has seen yet"), "unknown"},
		{nil, "unknown"},
	}
	for _, c := range cases {
		if got := fetchPhase(c.err); got != c.want {
			t.Errorf("fetchPhase(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
