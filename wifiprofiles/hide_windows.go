//go:build windows

package wifiprofiles

import (
	"os/exec"
	"syscall"
)

// hideWindow sets the CREATE_NO_WINDOW flag so console tools do not
// briefly flash up a cmd window.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
