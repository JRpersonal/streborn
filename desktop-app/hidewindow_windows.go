//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideCmdWindow attaches a Windows-specific SysProcAttr to the
// exec.Cmd so subprocesses (ssh, netsh, ...) do not pop a flashing
// CMD console window during the post-eject auto-install loop. The
// loop spawns up to 20 ssh probes plus the install + reboot, so
// without this every retry would briefly raise an empty terminal
// on top of the Wails app.
func hideCmdWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
