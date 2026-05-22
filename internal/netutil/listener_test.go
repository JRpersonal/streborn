package netutil

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestReusableListener verifies that two listeners can bind the same
// address back-to-back without a TIME_WAIT cooldown. Without
// SO_REUSEADDR the second Listen would fail with "address already in
// use" if a connection from the first was in TIME_WAIT.
func TestReusableListener(t *testing.T) {
	ln, err := ListenTCP(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("first listen: %v", err)
	}
	addr := ln.Addr().String()

	// Make a quick connection to push the socket into a state that
	// would normally leave it in TIME_WAIT, then close.
	go func() {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
		}
	}()
	if conn, err := ln.Accept(); err == nil {
		_ = conn.Close()
	}
	_ = ln.Close()

	// Re-bind immediately. With SO_REUSEADDR this must succeed.
	ln2, err := ListenTCP(context.Background(), addr)
	if err != nil {
		t.Fatalf("second listen on %s after close: %v", addr, err)
	}
	_ = ln2.Close()
}
