//go:build windows

package netutil

import "syscall"

// setReuseAddr enables SO_REUSEADDR on the given socket file descriptor.
// Windows accepts the same constant via syscall.SetsockoptInt; the
// semantics differ from Linux (Windows SO_REUSEADDR is more like
// Linux SO_REUSEPORT) but for our use case (test runs on dev
// workstations binding loopback ports) the effect is equivalent.
func setReuseAddr(fd uintptr) error {
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}
