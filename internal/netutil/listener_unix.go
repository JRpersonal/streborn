//go:build !windows

package netutil

import "syscall"

// SO_REUSEPORT — the Go stdlib syscall package on Linux/arm 1.22 does
// not export this constant (it lives in golang.org/x/sys/unix), so we
// hard-code the wire value. Stable since Linux 3.9 (released 2013);
// the Bose speakers run Linux 3.14, so this is always supported there.
const soReusePort = 15

// setReuseAddr enables SO_REUSEADDR AND SO_REUSEPORT on the given socket
// file descriptor. Linux/Darwin path.
//
// SO_REUSEADDR alone covers TIME_WAIT-state previous sockets, but on
// the Bose speakers the Bose firmware holds long-lived connections to
// :8081 (bmx) and :9080 (marge); when our agent dies those connections
// stick in FIN_WAIT_2 or ESTABLISHED with FINs the peer never ACKs,
// and the kernel keeps the listening socket bound effectively forever
// from SO_REUSEADDR's point of view. SO_REUSEPORT allows the
// respawned agent's new listener to bind on the same port regardless.
//
// SO_REUSEPORT is best-effort: if the kernel rejects it (e.g. on a
// fork of Linux that does not implement it), we still set
// SO_REUSEADDR and proceed — the bind will work in the common case.
func setReuseAddr(fd uintptr) error {
	if err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		return err
	}
	_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
	return nil
}
