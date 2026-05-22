// Package netutil provides shared low-level networking helpers for the
// STR agent. The main one is a TCP ListenConfig that enables
// SO_REUSEADDR so a freshly-respawned agent can bind on a port whose
// previous owner is still in TIME_WAIT.
//
// Why this exists: the watchdog in usb-stick/run.sh kills the agent
// (kill -TERM, then kill -KILL) and immediately restarts it. The
// previous TCP listeners on :8081 (bmx) and :9080 (marge) sit in
// TIME_WAIT for ~60 s on the Bose kernel (Linux 3.14, tcp_fin_timeout
// default). Without SO_REUSEADDR the new agent dies on bind with
// "address already in use", the watchdog respawns it, same failure,
// perpetual loop. Issue #60 (deqw, 2026-05-22) hit exactly that.
package netutil

import (
	"context"
	"net"
	"syscall"
)

// ReusableListenConfig returns a net.ListenConfig that sets
// SO_REUSEADDR on the listening socket. On Linux this also makes the
// bind survive the previous process's TIME_WAIT window. Portable: the
// SO_REUSEADDR constant exists on Linux, Darwin and Windows.
func ReusableListenConfig() *net.ListenConfig {
	return &net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var sockErr error
			err := c.Control(func(fd uintptr) {
				sockErr = setReuseAddr(fd)
			})
			if err != nil {
				return err
			}
			return sockErr
		},
	}
}

// ListenTCP is a convenience wrapper that opens a TCP listener with
// SO_REUSEADDR enabled. Callers pass the address (":8081", "127.0.0.1:9080",
// ...) and get back a normal net.Listener.
func ListenTCP(ctx context.Context, addr string) (net.Listener, error) {
	return ReusableListenConfig().Listen(ctx, "tcp", addr)
}
