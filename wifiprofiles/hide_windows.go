//go:build windows

package wifiprofiles

import (
	"os/exec"
	"syscall"
)

// hideWindow setzt das CREATE_NO_WINDOW Flag damit Console Tools nicht
// kurz ein cmd Fenster aufflackern lassen.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
