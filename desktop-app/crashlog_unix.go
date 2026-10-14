//go:build !windows

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// redirectStderrTo points the process's stderr (fd 2) at f so the Go
// runtime's crash traceback, which it writes to fd 2 on an unrecovered
// panic or a fatal runtime error, lands in the log file. Best-effort;
// see newFileLogger for why this is only done in production builds.
func redirectStderrTo(f *os.File) {
	if f == nil {
		return
	}
	if err := unix.Dup2(int(f.Fd()), int(os.Stderr.Fd())); err == nil {
		os.Stderr = f
	}
}
