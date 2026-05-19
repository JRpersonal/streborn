//go:build !windows

package main

import "os/exec"

// hideCmdWindow is a no-op on macOS and Linux: those platforms do
// not attach a console window to spawned subprocesses by default,
// so there is nothing to hide.
func hideCmdWindow(cmd *exec.Cmd) {}
