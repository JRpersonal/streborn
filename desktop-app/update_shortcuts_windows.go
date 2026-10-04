//go:build windows

package main

import (
	"context"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// psShortcutRetargeter rewrites .lnk files through WScript.Shell in a hidden
// PowerShell, the same host the relaunch helper already relies on. It looks in
// the three places a user keeps a shortcut to an app they downloaded: the
// Desktop (wherever it is redirected to, OneDrive included), the Start Menu's
// Programs folder, and the taskbar pins. Only links whose target IS the old
// file are touched, compared case-insensitively as Windows paths are.
type psShortcutRetargeter struct{}

func (psShortcutRetargeter) Retarget(oldPath, newPath string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", shortcutRetargetScript(oldPath, newPath))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	var changed []string
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			changed = append(changed, line)
		}
	}
	return changed, err
}

func newShortcutRetargeter() shortcutRetargeter { return psShortcutRetargeter{} }

// firewallRulesNaming counts Windows Firewall rules whose program is path. Read
// only: listing rules needs no elevation, changing them does, and STR never
// asks for elevation. Windows ties an "allow" to the program's PATH, so a file
// that moves to the stable name gets the firewall question once more on its
// first start; this count is what lets the log say so instead of leaving a
// silent empty speaker list to explain later. -1 when the list is unreadable.
func firewallRulesNaming(path string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "netsh", "advfirewall", "firewall", "show", "rule", "name=all", "verbose")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return -1
	}
	return strings.Count(strings.ToLower(string(out)), strings.ToLower(path))
}
