//go:build !windows

package wifiprofiles

import "os/exec"

// hideWindow ist auf Mac/Linux kein-op, da dort keine Console aufpoppt.
func hideWindow(cmd *exec.Cmd) {}
