//go:build !windows

package netutil

import "syscall"

// setReuseAddr enables SO_REUSEADDR on the given socket file descriptor.
// Linux/Darwin path.
func setReuseAddr(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}
