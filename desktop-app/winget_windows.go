//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// lstatExists accepts winget's app execution alias, a reparse point that
// os.Stat cannot always follow.
func lstatExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// runWingetUpgrade asks winget whether it offers version yet, then arms the
// hidden upgrade helper and quits. Any step that fails leaves the app running
// and returns the manual command instead.
func (a *App) runWingetUpgrade(exe, version string) WingetUpdateResult {
	winget := findWinget(exec.LookPath, os.Getenv("LOCALAPPDATA"), lstatExists)
	if winget == "" {
		a.logger.Info("app update via winget: winget.exe not found on PATH or in WindowsApps")
		return wingetOutcome(wingetReasonNotFound)
	}
	if version != "" {
		ctx, cancel := context.WithTimeout(a.appCtx(), 90*time.Second)
		cmd := exec.CommandContext(ctx, winget, wingetShowArgs(version)...)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		started := time.Now()
		err := cmd.Run()
		cancel()
		var exitErr *exec.ExitError
		switch {
		case err == nil:
			a.logger.Info("app update via winget: winget offers the version", "version", version,
				"ms", time.Since(started).Milliseconds())
		case errors.As(err, &exitErr):
			a.logger.Info("app update via winget: winget does not offer the version yet", "version", version,
				"exitCode", exitErr.ExitCode(), "ms", time.Since(started).Milliseconds())
			return wingetOutcome(wingetReasonNotOffered)
		default:
			// winget could not be asked at all (timeout, start error). Do not
			// guess: show the command rather than quitting on a maybe.
			a.logger.Info("app update via winget: could not ask winget", "err", err)
			return wingetOutcome(wingetReasonStartFailed)
		}
	}
	marker, err := wingetFailMarkerPath()
	if err != nil {
		a.logger.Info("app update via winget: no config folder for the failure note", "err", err)
		return wingetOutcome(wingetReasonStartFailed)
	}
	_ = os.Remove(marker) // an old note must not be read as this run's
	pid := os.Getpid()
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", wingetUpgradeScript(pid, winget, exe, marker))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		a.logger.Warn("app update via winget: helper failed to start", "err", err)
		return wingetOutcome(wingetReasonStartFailed)
	}
	a.logger.Info("app update via winget: helper armed, quitting so winget can replace the app",
		"winget", winget, "command", wingetManualCommand(), "exe", exe)
	a.quitAfterRelaunchArmed(pid)
	return wingetOutcome(wingetReasonStarted)
}

// createShortcut writes lnk pointing at exe through WScript.Shell in a hidden
// PowerShell. No COM binding in Go: go-ole is only an indirect dependency, and
// the retargeter after a rename already goes this way.
func createShortcut(lnk, exe string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", startMenuShortcutScript(lnk, exe))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	if !fileExists(lnk) {
		return errors.New("shortcut missing although PowerShell reported success")
	}
	return nil
}
