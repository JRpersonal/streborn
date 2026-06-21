//go:build !windows

package wifiprofiles

import "os/exec"

// hideWindow is a no-op on Mac/Linux, since no console pops up there.
func hideWindow(cmd *exec.Cmd) {}
