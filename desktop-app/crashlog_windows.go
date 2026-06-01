//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// redirectStderrTo points the process's stderr handle at f so the Go
// runtime's crash traceback, which it writes to stderr on an unrecovered
// panic or a fatal runtime error, lands in the log file. Best-effort;
// see newFileLogger for why this is only done in production builds.
func redirectStderrTo(f *os.File) {
	if f == nil {
		return
	}
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd())); err == nil {
		os.Stderr = f
	}
}
