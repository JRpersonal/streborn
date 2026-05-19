//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideCmdWindow attaches a Windows-specific SysProcAttr to the
// exec.Cmd so subprocesses (ssh, netsh, ...) do not pop a flashing
// CMD console window during loops that spawn many short-lived
// processes (cold-bootstrap netsh dance, post-eject auto-install
// SSH retry loop). Without this every probe briefly raises an
// empty terminal on top of the Wails app.
func hideCmdWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
