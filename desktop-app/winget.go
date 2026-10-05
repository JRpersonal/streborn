package main

// A copy of ST Reborn that winget installed (package JRpersonal.STReborn,
// InstallerType portable) belongs to winget, not to the in-app updater.
//
// What winget does for a portable package (checked against winget-cli's
// PortableFlow.cpp, PortableInstaller.cpp and Runtime.cpp on 2026-10-06):
//   - it copies the exe into <root>\<PackageId>_<SourceId>\, where <root> is
//     %LOCALAPPDATA%\Microsoft\WinGet\Packages for user scope and
//     %ProgramFiles%\WinGet\Packages (or the x86 one) for machine scope. Both
//     roots can be moved with the portablePackageUserRoot /
//     portablePackageMachineRoot settings, the folder name stays the same;
//   - it links the command alias (streborn.exe) from ...\WinGet\Links, or puts
//     the package folder on PATH where it cannot create a symlink;
//   - it records the installed version in its own uninstall (ARP) entry;
//   - it creates no Start menu entry.
//
// So the self-update's exe swap would leave winget's record on the old version:
// `winget list` shows it, and `winget upgrade` installs the same release again.
// Such a copy updates through `winget upgrade` instead, and gets the Start menu
// entry winget never makes.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// wingetPackageID is the identifier in microsoft/winget-pkgs. winget names the
// package folder after it, followed by "_" and the source identifier.
const wingetPackageID = "JRpersonal.STReborn"

// wingetUpgradeArgs is the one upgrade command STR runs, and the one it shows
// when it cannot run it. --exact keeps the id from matching anything else;
// --silent and the two agreement flags keep a hidden run from stopping at a
// question nobody can see.
var wingetUpgradeArgs = []string{
	"upgrade", "--id", wingetPackageID, "--exact", "--silent",
	"--accept-source-agreements", "--accept-package-agreements",
}

// wingetManualCommand is the command line a user can paste into a terminal when
// STR cannot start winget itself.
func wingetManualCommand() string {
	return "winget " + strings.Join(wingetUpgradeArgs, " ")
}

// wingetShowArgs asks winget whether it already offers version. The winget
// manifests carry the bare number ("1.0.3"), the update check may say "v1.0.3".
func wingetShowArgs(version string) []string {
	v := strings.TrimLeft(strings.TrimSpace(version), "vV")
	return []string{"show", "--id", wingetPackageID, "--exact", "--version", v, "--accept-source-agreements"}
}

// isWingetManagedPath reports whether exe sits directly in a winget package
// folder of STR, i.e. its folder is named "JRpersonal.STReborn_<source>". Only
// the folder name is checked, case-insensitively and with either separator, so
// both scopes and a moved package root are recognised; a copy the user keeps
// anywhere else, even inside a WinGet tree, is not.
func isWingetManagedPath(exe string) bool {
	p := strings.TrimSpace(strings.ReplaceAll(exe, `\`, "/"))
	if p == "" || strings.HasSuffix(p, "/") {
		return false
	}
	parts := strings.Split(p, "/")
	if len(parts) < 2 {
		return false
	}
	parent := strings.ToLower(parts[len(parts)-2])
	prefix := strings.ToLower(wingetPackageID) + "_"
	return strings.HasPrefix(parent, prefix) && len(parent) > len(prefix)
}

var (
	wingetOnce    sync.Once
	wingetExePath string
	wingetManaged bool
)

// wingetManagedExe returns the path of the running exe inside its winget
// package folder, and whether this is a winget-managed install at all. Both the
// path the process was started by and its resolved target are checked: started
// through the alias in WinGet\Links, the former is the link and only the latter
// is in the package folder. The answer cannot change while the app runs, so it
// is worked out once.
func wingetManagedExe() (string, bool) {
	wingetOnce.Do(func() {
		if runtime.GOOS != "windows" {
			return
		}
		exe, err := os.Executable()
		if err != nil {
			return
		}
		candidates := []string{exe}
		if r, e := filepath.EvalSymlinks(exe); e == nil && r != exe {
			candidates = append([]string{r}, candidates...)
		}
		for _, c := range candidates {
			if isWingetManagedPath(c) {
				wingetExePath, wingetManaged = c, true
				return
			}
		}
	})
	return wingetExePath, wingetManaged
}

// errWingetManaged is what the download and install steps answer in a
// winget-managed install, so no caller fetches a file that would only be thrown
// away, and nothing swaps the exe behind winget's back.
var errWingetManaged = fmt.Errorf("this copy of ST Reborn is managed by winget; update it with: %s", wingetManualCommand())

// findWinget locates winget.exe: on PATH first, then the per-user app
// execution alias every Windows with App Installer has. Empty when neither is
// there. The alias is a zero-byte reparse point, so exists has to accept it
// (os.Lstat does, a plain open does not).
func findWinget(lookPath func(string) (string, error), localAppData string, exists func(string) bool) string {
	if p, err := lookPath("winget.exe"); err == nil && p != "" {
		return p
	}
	if localAppData != "" {
		c := filepath.Join(localAppData, "Microsoft", "WindowsApps", "winget.exe")
		if exists(c) {
			return c
		}
	}
	return ""
}

// WingetUpdateResult is UpdateViaWinget's answer to the frontend.
//
// Started means the helper is armed and the app is about to quit: winget
// replaces the exe once this process is gone and starts the new version. When
// it is false, Command is what the user can run themselves, and NotYet says
// that winget did not offer the requested version (the winget-pkgs pull request
// for a release is usually merged a few days after it).
type WingetUpdateResult struct {
	Started bool   `json:"started"`
	NotYet  bool   `json:"notYet"`
	Command string `json:"command"`
	Reason  string `json:"reason"`
}

// Reasons a winget update did not start, in WingetUpdateResult.Reason and the log.
const (
	wingetReasonStarted     = "started"
	wingetReasonNotManaged  = "not-winget"
	wingetReasonNotFound    = "winget-not-found"
	wingetReasonStartFailed = "start-failed"
	wingetReasonNotOffered  = "not-offered"
	wingetReasonUnsupported = "unsupported-os"
)

// wingetOutcome builds the result for reason. Every outcome carries the manual
// command, so the frontend always has something concrete to show.
func wingetOutcome(reason string) WingetUpdateResult {
	return WingetUpdateResult{
		Started: reason == wingetReasonStarted,
		NotYet:  reason == wingetReasonNotOffered,
		Command: wingetManualCommand(),
		Reason:  reason,
	}
}

// UpdateViaWinget updates a winget-managed install through winget. It checks
// that winget already offers version, starts a hidden helper that waits for
// this process to end, runs the upgrade and starts the app again, and quits.
// It never downloads anything itself.
func (a *App) UpdateViaWinget(version string) (WingetUpdateResult, error) {
	exe, ok := wingetManagedExe()
	if !ok {
		return wingetOutcome(wingetReasonNotManaged), errors.New("this copy of ST Reborn was not installed by winget")
	}
	res := a.runWingetUpgrade(exe, version)
	a.logger.Info("app update via winget", "version", version, "result", res.Reason, "exe", exe)
	return res, nil
}

// wingetFailMarkerName is the note the upgrade helper leaves in the app's
// config folder when winget exits with an error. The app it starts again reads
// it once, so the user learns that the update did not happen and how to run it.
const wingetFailMarkerName = "winget-upgrade-failed"

func wingetFailMarkerPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ST Reborn", wingetFailMarkerName), nil
}

// WingetUpgradeFailure reports a failed winget upgrade from the previous run.
type WingetUpgradeFailure struct {
	Failed   bool   `json:"failed"`
	ExitCode string `json:"exitCode"`
	Command  string `json:"command"`
}

// readWingetFailMarker reads and removes the marker. A marker that cannot be
// removed counts as absent, so a stuck file never reports on every start.
func readWingetFailMarker(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	if os.Remove(path) != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// ConsumeWingetUpgradeFailure tells the frontend, once, that the winget upgrade
// started by the previous run failed, with winget's exit code and the command
// to run by hand.
func (a *App) ConsumeWingetUpgradeFailure() WingetUpgradeFailure {
	p, err := wingetFailMarkerPath()
	if err != nil {
		return WingetUpgradeFailure{}
	}
	code, ok := readWingetFailMarker(p)
	if !ok {
		return WingetUpgradeFailure{}
	}
	a.logger.Warn("app update via winget failed in the previous run", "exitCode", code, "command", wingetManualCommand())
	return WingetUpgradeFailure{Failed: true, ExitCode: code, Command: wingetManualCommand()}
}

// wingetShortcutFlag remembers in app-state.json that the Start menu entry was
// made (or found), so an entry the user deletes stays deleted.
const wingetShortcutFlag = "wingetStartMenuShortcut"

type shortcutDecision int

const (
	shortcutSkip           shortcutDecision = iota // nothing to do
	shortcutCreate                                 // make the entry, then remember it
	shortcutRecordExisting                         // an entry is there already: only remember it
)

// decideWingetShortcut decides about the Start menu entry once per start. Only
// a winget-managed install gets one, and only once ever: after it was created,
// or found already there, the flag is set and a deleted entry is never
// recreated.
func decideWingetShortcut(managed, alreadyDone, exists bool) shortcutDecision {
	switch {
	case !managed || alreadyDone:
		return shortcutSkip
	case exists:
		return shortcutRecordExisting
	default:
		return shortcutCreate
	}
}

// startMenuShortcutPath is the per-user Start menu entry, in %APPDATA%.
func startMenuShortcutPath(appData string) string {
	if appData == "" {
		return ""
	}
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "ST Reborn.lnk")
}

// startMenuShortcutScript writes the .lnk through WScript.Shell, the same COM
// object the shortcut retargeter after a rename already uses (see
// shortcutRetargetScript). The icon is the one embedded in the exe.
func startMenuShortcutScript(lnk, exe string) string {
	return "$ErrorActionPreference='Stop';" +
		"New-Item -ItemType Directory -Force -Path " + psQuote(filepath.Dir(lnk)) + " | Out-Null;" +
		"$sh=New-Object -ComObject WScript.Shell;" +
		"$l=$sh.CreateShortcut(" + psQuote(lnk) + ");" +
		"$l.TargetPath=" + psQuote(exe) + ";" +
		"$l.WorkingDirectory=" + psQuote(filepath.Dir(exe)) + ";" +
		"$l.IconLocation=" + psQuote(exe+",0") + ";" +
		"$l.Description='ST Reborn';" +
		"$l.Save()"
}

// wingetUpgradeScript is the hidden PowerShell helper: wait for this process to
// end (the exe is locked while it runs), run the upgrade, leave the exit code
// in failMarker when winget reports an error, and start the app again from the
// same path either way, so the user is never left with nothing running.
func wingetUpgradeScript(pid int, winget, exe, failMarker string) string {
	quoted := make([]string, len(wingetUpgradeArgs))
	for i, a := range wingetUpgradeArgs {
		quoted[i] = psQuote(a)
	}
	return fmt.Sprintf("try { Wait-Process -Id %d -Timeout 60 } catch {}; Start-Sleep -Milliseconds 500; "+
		"$code = 0; try { & %s %s; $code = $LASTEXITCODE } catch { $code = 'not-started' }; "+
		"if ($code -ne 0) { try { New-Item -ItemType Directory -Force -Path %s | Out-Null; Set-Content -LiteralPath %s -Value $code } catch {} }; "+
		"Start-Process -FilePath %s",
		pid, psQuote(winget), strings.Join(quoted, " "),
		psQuote(filepath.Dir(failMarker)), psQuote(failMarker), psQuote(exe))
}

// ensureWingetShortcut gives a winget-managed install its Start menu entry,
// once. Called in the background at startup; every step is best effort.
func (a *App) ensureWingetShortcut() {
	exe, managed := wingetManagedExe()
	if !managed {
		return
	}
	lnk := startMenuShortcutPath(os.Getenv("APPDATA"))
	if lnk == "" {
		return
	}
	switch decideWingetShortcut(managed, a.GetAppFlag(wingetShortcutFlag), fileExists(lnk)) {
	case shortcutSkip:
		return
	case shortcutRecordExisting:
		a.logger.Info("winget install: Start menu entry already exists, leaving it alone", "shortcut", lnk)
		if err := a.SetAppFlag(wingetShortcutFlag); err != nil {
			a.logger.Info("winget install: could not remember the Start menu entry", "err", err)
		}
	case shortcutCreate:
		if err := createShortcut(lnk, exe); err != nil {
			a.logger.Info("winget install: could not create the Start menu entry, trying again next start", "shortcut", lnk, "err", err)
			return
		}
		a.logger.Info("winget install: created the Start menu entry", "shortcut", lnk, "target", exe)
		if err := a.SetAppFlag(wingetShortcutFlag); err != nil {
			a.logger.Info("winget install: could not remember the Start menu entry", "err", err)
		}
	}
}

// wingetManagedFlag is wingetManagedExe's yes/no, for log lines.
func wingetManagedFlag() bool {
	_, ok := wingetManagedExe()
	return ok
}
